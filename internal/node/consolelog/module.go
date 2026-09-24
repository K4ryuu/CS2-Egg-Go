// SPDX-License-Identifier: GPL-3.0-or-later

// Package consolelog is the node module that persists every server's
// console tab output (the egg's own messages and the game server's own
// lines, merged) to host disk: one directory per server, a file per day,
// gzip-compressed once a day old. A server needs the panel's Log File
// Persistence variable on and this module installed; without either, the
// egg keeps nothing on disk for it, this module never falls back to
// writing inside the container.
package consolelog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// MaxLineLen bounds one console line written to disk. The container is
// hostile input, not a trusted source: a socket client can send anything.
const MaxLineLen = 4000

// Config is the "consolelog" section of the node config.
type Config struct {
	Enabled  bool   `json:"enabled"`
	Dir      string `json:"dir"`
	KeepDays int    `json:"keep_days"`
}

// Defaults is what the wizard offers. Off by default: this can be a lot of
// disk across many busy servers, and most installs will not want it.
func Defaults() Config {
	return Config{Enabled: false, Dir: "/var/lib/cs2node/logs", KeepDays: 30}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	switch {
	case c.Dir == "" || c.Dir[0] != '/':
		return errors.New("dir must be an absolute path")
	case c.KeepDays < 0:
		return errors.New("keep_days cannot be negative")
	}
	return nil
}

// Module is the console-log feature.
type Module struct {
	Log *slog.Logger
	Now func() time.Time

	cfg  Config
	mu   sync.Mutex
	last map[string]time.Time // per container, last line written, for status
}

// New returns the module with logging wired.
func New(log *slog.Logger) *Module {
	return &Module{Log: log, Now: time.Now, last: map[string]time.Time{}}
}

func (m *Module) Name() string { return "consolelog" }

func (m *Module) Describe() string {
	return "Console log persistence: every server's console tab, saved to host disk, gzip-compressed after a day"
}

func (m *Module) Questions() []module.Question {
	d := Defaults()
	return []module.Question{
		{Key: "dir", Prompt: "Where console logs are kept", Default: d.Dir, Help: "one subfolder per server, a file per day inside it"},
		{Key: "keep_days", Prompt: "Days to keep a server's log files", Default: strconv.Itoa(d.KeepDays), Kind: module.Int,
			Help: fmt.Sprintf("gzip-compressed after a day; always capped at %d regardless of this value", logx.HardCapDays)},
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
	m.Log.Info("consolelog up", "dir", m.cfg.Dir, "keep_days", m.cfg.KeepDays)
	c.ProvideStatus(m.Name(), func() any {
		m.mu.Lock()
		defer m.mu.Unlock()
		out := make(map[string]string, len(m.last))
		for k, v := range m.last {
			out[k] = v.Format(time.RFC3339)
		}
		return out
	})
	c.Handle(proto.TypeLogLine, func(container string, msg proto.Message) {
		l := msg.(*proto.LogLine)
		m.write(container, l)
	})
	m.maintain() // catch up on anything left uncompressed or unpruned since the daemon last ran
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			m.maintain()
		}
	}
}

// write persists one line. container is the transport's own identity for
// this connection, never something the message payload supplied, so it is
// safe to use as a path element the way crashes.Bundle already does.
func (m *Module) write(container string, l *proto.LogLine) {
	now := m.Now()
	WriteLine(m.cfg.Dir, container, core.Clean(l.Kind, 32), core.Clean(l.Msg, MaxLineLen), now)
	m.mu.Lock()
	m.last[container] = now
	m.mu.Unlock()
}

// WriteLine persists one already-sanitized line for container under dir,
// one file per day, the same shape logx.FileSink writes anywhere else.
func WriteLine(dir, container, kind, msg string, now time.Time) {
	sink := &logx.FileSink{Dir: filepath.Join(dir, container), Now: func() time.Time { return now }}
	sink.Write(kind, msg)
}

// maintain compresses yesterday-and-older files and enforces retention for
// every server directory on disk, not only ones this run has written to,
// so a stopped server's leftover logs still get cleaned up.
func (m *Module) maintain() { Maintain(m.cfg.Dir, m.cfg.KeepDays, m.Now()) }

// Maintain compresses yesterday-and-older files and enforces keepDays
// retention (see logx.HardCapDays for the ceiling that always applies) for
// every server subdirectory under dir.
func Maintain(dir string, keepDays int, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		logx.Compress(sub, now)
		logx.Rotate(sub, keepDays, 0, 0, now)
	}
}

// Doctor checks the config and reports the dir.
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "Console log"
	cfg := Defaults()
	if err := c.Config(m.Name(), &cfg); err != nil {
		return []doctor.Check{doctor.Failf(s, "config section unreadable: "+err.Error())}
	}
	if err := cfg.Check(); err != nil {
		return []doctor.Check{doctor.Failf(s, "config: "+err.Error())}
	}
	st, err := os.Stat(cfg.Dir)
	if err != nil {
		return []doctor.Check{doctor.Warnf(s, cfg.Dir+" missing (the daemon creates it on start)")}
	}
	if !st.IsDir() {
		return []doctor.Check{doctor.Failf(s, cfg.Dir+" is not a directory")}
	}
	entries, _ := os.ReadDir(cfg.Dir)
	return []doctor.Check{doctor.Ok(s, fmt.Sprintf("%d server director(y/ies) under %s", len(entries), cfg.Dir))}
}
