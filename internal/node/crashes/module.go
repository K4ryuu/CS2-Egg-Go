// SPDX-License-Identifier: GPL-3.0-or-later

package crashes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// Config is the "crashes" section of the node config.
type Config struct {
	Enabled   bool   `json:"enabled"`
	Dir       string `json:"dir"`
	KeepCount int    `json:"keep_count"` // per server, 0 = unlimited
	KeepDays  int    `json:"keep_days"`  // 0 = unlimited
}

// Defaults is what the wizard offers.
func Defaults() Config {
	return Config{Enabled: true, Dir: "/var/lib/cs2node/crashes", KeepCount: 20, KeepDays: 30}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	switch {
	case c.Dir == "" || c.Dir[0] != '/':
		return errors.New("dir must be an absolute path")
	case c.KeepCount < 0 || c.KeepDays < 0:
		return errors.New("keep_count and keep_days cannot be negative")
	}
	return nil
}

// Module is the crashes feature.
type Module struct {
	Log *slog.Logger
	Now func() time.Time

	cfg    Config
	mu     sync.Mutex
	last   map[string]Meta
	lastAt map[string]time.Time // per container, for the rate limit
}

// New returns the module with logging wired.
func New(log *slog.Logger) *Module {
	return &Module{Log: log, Now: time.Now, last: map[string]Meta{}, lastAt: map[string]time.Time{}}
}

func (m *Module) Name() string { return "crashes" }

func (m *Module) Describe() string {
	return "Crash bundles: console tail, dumps and logs of every crash, kept outside the volume"
}

func (m *Module) Questions() []module.Question {
	d := Defaults()
	return []module.Question{
		{Key: "dir", Prompt: "Where bundles are kept", Default: d.Dir, Help: "one subfolder per server"},
		{Key: "keep_count", Prompt: "Bundles to keep per server", Default: strconv.Itoa(d.KeepCount), Kind: module.Int, Help: "0 = unlimited"},
		{Key: "keep_days", Prompt: "Days to keep a bundle", Default: strconv.Itoa(d.KeepDays), Kind: module.Int, Help: "0 = unlimited"},
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
	if err := os.MkdirAll(m.cfg.Dir, 0o755); err != nil {
		return err
	}
	for _, meta := range List(m.cfg.Dir, "") { // newest first, so the first per server wins
		if _, ok := m.last[meta.Container]; !ok {
			m.last[meta.Container] = meta
		}
	}
	m.Log.Info("crashes up", "dir", m.cfg.Dir, "keep_count", m.cfg.KeepCount, "keep_days", m.cfg.KeepDays)
	c.ProvideStatus(m.Name(), func() any {
		m.mu.Lock()
		defer m.mu.Unlock()
		out := make(map[string]Meta, len(m.last))
		for k, v := range m.last {
			out[k] = v
		}
		return out
	})
	c.Handle(proto.TypeCrash, func(container string, msg proto.Message) {
		cr := msg.(*proto.Crash)
		// one bundle a minute per server: the socket is the container's,
		// a loop of fake crash reports must not walk the volume nonstop
		m.mu.Lock()
		if last, ok := m.lastAt[container]; ok && m.Now().Sub(last) < time.Minute {
			m.mu.Unlock()
			m.Log.Warn("crash report ignored, one came less than a minute ago", "container", container)
			return
		}
		m.lastAt[container] = m.Now()
		m.mu.Unlock()
		volume := ""
		for _, cont := range c.Containers() {
			if cont.Name == container {
				volume = cont.Volume
			}
		}
		go m.bundle(container, volume, cr) // the volume walk takes seconds, never stall the socket
	})
	<-ctx.Done()
	return ctx.Err()
}

func (m *Module) bundle(container, volume string, cr *proto.Crash) {
	meta, err := Bundle(m.cfg.Dir, container, volume, cr, m.Now())
	if err != nil {
		m.Log.Warn("crash bundle failed", "container", container, "err", err)
		return
	}
	m.mu.Lock()
	m.last[container] = meta
	m.mu.Unlock()
	m.Log.Info("crash bundled", "container", container, "exit_code", cr.ExitCode, "map", cr.Map, "players", cr.Players, "files", len(meta.Files), "path", meta.Path)
	if n := Prune(m.cfg.Dir, m.cfg.KeepCount, m.cfg.KeepDays, m.Now()); n > 0 {
		m.Log.Info("old crash bundles removed", "count", n)
	}
}

// Doctor checks the bundle dir and reports the last crash per server.
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "Crashes"
	cfg := Defaults()
	if err := c.Config(m.Name(), &cfg); err != nil {
		return []doctor.Check{doctor.Failf(s, "config section unreadable: "+err.Error())}
	}
	if err := cfg.Check(); err != nil {
		return []doctor.Check{doctor.Failf(s, "config: "+err.Error())}
	}
	if st, err := os.Stat(cfg.Dir); err != nil {
		return []doctor.Check{doctor.Warnf(s, cfg.Dir+" missing (the daemon creates it on start)")}
	} else if !st.IsDir() {
		return []doctor.Check{doctor.Failf(s, cfg.Dir+" is not a directory")}
	}
	all := List(cfg.Dir, "")
	out := []doctor.Check{doctor.Ok(s, fmt.Sprintf("%d bundle(s) under %s", len(all), cfg.Dir))}
	seen := map[string]bool{}
	for _, meta := range all {
		if seen[meta.Container] {
			continue
		}
		seen[meta.Container] = true
		if time.Since(meta.Time) < 24*time.Hour {
			out = append(out, doctor.Warnf(s, fmt.Sprintf("%s crashed %s ago (exit %d, %s): cs2node crash %s", meta.Container, time.Since(meta.Time).Round(time.Minute), meta.ExitCode, meta.Map, meta.Container)))
		}
	}
	return out
}
