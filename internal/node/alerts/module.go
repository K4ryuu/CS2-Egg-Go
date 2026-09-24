// SPDX-License-Identifier: GPL-3.0-or-later

// Package alerts posts node notices (crashes, updates, blocks, backups) to
// a Discord webhook. Notices arriving close together go out as one message.
package alerts

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
)

// Config is the "alerts" section of the node config.
type Config struct {
	Enabled bool     `json:"enabled"`
	Webhook string   `json:"webhook"`
	Events  []string `json:"events"` // notice kinds to post; empty or "all" = every one
	Name    string   `json:"name"`   // node label in the message footer
	// Username and Avatar override the identity the webhook was created
	// with, so the posts say what they are instead of whatever the channel
	// owner happened to call the webhook.
	Username string `json:"username"`
	Avatar   string `json:"avatar_url"`
}

// Defaults is what the wizard offers.
func Defaults() Config {
	host, _ := os.Hostname()
	return Config{Enabled: true, Events: []string{"all"}, Name: host, Username: "cs2node"}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	if !strings.HasPrefix(c.Webhook, "https://") {
		return errors.New("webhook must be an https URL")
	}
	for _, e := range c.Events {
		if e != "all" && !slices.ContainsFunc(core.NoticeKinds, func(k struct{ Kind, Desc string }) bool { return k.Kind == e }) {
			return errors.New("unknown event " + e)
		}
	}
	if c.Avatar != "" && !strings.HasPrefix(c.Avatar, "https://") {
		return errors.New("avatar_url must be an https URL")
	}
	return nil
}

func (c Config) wants(kind string) bool {
	return len(c.Events) == 0 || slices.Contains(c.Events, "all") || slices.Contains(c.Events, kind)
}

// Module is the alerts feature.
type Module struct {
	Log        *slog.Logger
	HTTP       *http.Client
	FlushDelay time.Duration // how long to collect notices before one post

	cfg     Config
	mu      sync.Mutex
	pending []core.Notice
	timer   *time.Timer
	sent    int
	failed  int
}

// New returns the module with logging and HTTP wired.
func New(log *slog.Logger, client *http.Client) *Module {
	return &Module{Log: log, HTTP: client, FlushDelay: 3 * time.Second}
}

func (m *Module) Name() string { return "alerts" }

func (m *Module) Describe() string {
	return "Discord alerts: crashes, CS2 updates, guard blocks, backups, node updates"
}

func (m *Module) Questions() []module.Question {
	d := Defaults()
	kinds := make([]module.Option, 0, len(core.NoticeKinds))
	for _, k := range core.NoticeKinds {
		kinds = append(kinds, module.Option{Value: k.Kind, Desc: k.Desc})
	}
	return []module.Question{
		{Key: "webhook", Prompt: "Discord webhook URL", Help: "Server Settings > Integrations > Webhooks > New Webhook > Copy URL", Required: true},
		{Key: "events", Prompt: "Events to post", Default: "all", Kind: module.List, Choices: kinds},
		{Key: "name", Prompt: "Node name shown in the messages", Default: d.Name},
		{Key: "username", Prompt: "Name the bot posts under", Default: d.Username, Help: "overrides whatever the webhook was named when it was created"},
		{Key: "avatar_url", Prompt: "Avatar image URL", Help: "https link to a png or jpg; empty keeps the webhook's own picture"},
	}
}

// Run serves until ctx ends.
func (m *Module) Run(ctx context.Context, c *core.Core) error {
	m.cfg = Defaults()
	if err := c.Config(m.Name(), &m.cfg); err != nil {
		return err
	}
	if err := m.cfg.Check(); err != nil {
		return err
	}
	m.Log.Info("alerts up", "events", strings.Join(m.cfg.Events, ","))
	c.ProvideStatus(m.Name(), func() any {
		m.mu.Lock()
		defer m.mu.Unlock()
		return status{Sent: m.sent, Failed: m.failed, Events: m.cfg.Events}
	})
	c.OnNotify(func(n core.Notice) {
		if !m.cfg.wants(n.Kind) {
			return
		}
		m.mu.Lock()
		m.pending = append(m.pending, n)
		if m.timer == nil {
			m.timer = time.AfterFunc(m.FlushDelay, func() { m.flush(ctx) })
		}
		m.mu.Unlock()
	})
	<-ctx.Done()
	m.flush(context.Background()) // a stop notice may still be pending
	return ctx.Err()
}

// status is the alerts section of the control snapshot.
type status struct {
	Sent   int      `json:"sent"`
	Failed int      `json:"failed"`
	Events []string `json:"events"`
}

// flush posts everything collected, at most maxEmbeds per request.
func (m *Module) flush(ctx context.Context) {
	m.mu.Lock()
	batch := m.pending
	m.pending = nil
	m.timer = nil
	m.mu.Unlock()
	for len(batch) > 0 {
		n := min(len(batch), maxEmbeds)
		err := Post(ctx, m.HTTP, m.cfg, batch[:n])
		m.mu.Lock()
		if err != nil {
			m.failed++
			m.Log.Warn("discord post failed", "err", err, "notices", n)
		} else {
			m.sent++
		}
		m.mu.Unlock()
		batch = batch[n:]
	}
}

// Doctor checks the config and that Discord knows the webhook (a GET never
// posts anything).
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "Alerts"
	cfg := Defaults()
	if err := c.Config(m.Name(), &cfg); err != nil {
		return []doctor.Check{doctor.Failf(s, "config section unreadable: "+err.Error())}
	}
	if err := cfg.Check(); err != nil {
		return []doctor.Check{doctor.Failf(s, "config: "+err.Error())}
	}
	out := []doctor.Check{doctor.Ok(s, "events: "+strings.Join(cfg.Events, ", "))}
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if name, err := Lookup(ctx, client, cfg.Webhook); err != nil {
		out = append(out, doctor.Failf(s, "webhook: "+err.Error()))
	} else {
		out = append(out, doctor.Ok(s, "webhook ok ("+name+")"))
	}
	var st status
	if c.ModuleStatus(m.Name(), &st) && st.Failed > 0 {
		out = append(out, doctor.Warnf(s, "posts failed since start: "+strconv.Itoa(st.Failed)+" (journalctl -u cs2node | grep discord)"))
	}
	return out
}
