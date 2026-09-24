// SPDX-License-Identifier: GPL-3.0-or-later

// Package cleanup is the node module that deletes stale files in every
// server volume on a schedule, with one rule set for the whole node.
package cleanup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	engine "github.com/K4ryuu/CS2-Egg-Go/internal/cleanup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/configs"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// StateDir holds the running totals across daemon restarts.
var StateDir = "/var/lib/cs2node/cleanup"

// Config is the "cleanup" section of the node config.
type Config struct {
	Enabled    bool          `json:"enabled"`
	EveryHours int           `json:"every_hours"` // 0 = only on server start
	OnStart    bool          `json:"on_start"`    // also run when a server comes up
	KeepOpen   bool          `json:"keep_open"`   // leave files the server still holds open
	Rules      []engine.Rule `json:"rules"`
}

// Defaults is what the wizard offers: the same rules the egg has shipped
// since the bash version, on a six-hourly pass.
func Defaults() Config {
	return Config{Enabled: true, EveryHours: 6, OnStart: true, KeepOpen: true, Rules: configs.DefaultCleanupRules()}
}

// ContainerBases are the absolute paths an older cleanup.json may name for
// the server root itself.
var ContainerBases = []string{ContainerRoot}

// Seed writes the rule set into the config file on install, so the whole
// node's cleanup is visible and editable in one place.
func (m *Module) Seed() map[string]any {
	return map[string]any{"rules": configs.DefaultCleanupRules()}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	if c.EveryHours < 0 || c.EveryHours > 24*7 {
		return errors.New("every_hours must be 0..168")
	}
	for _, r := range c.Rules {
		switch {
		case r.Name == "":
			return errors.New("every rule needs a name")
		case len(r.Directories) == 0 || len(r.Patterns) == 0:
			return errors.New("rule " + r.Name + " needs directories and patterns")
		}
		for _, d := range r.Directories {
			if _, ok := engine.Clean(d, ContainerBases); !ok {
				return errors.New("rule " + r.Name + ": " + d + " is outside the server directory; paths are relative to the volume root")
			}
		}
	}
	return nil
}

// Module is the cleanup feature.
type Module struct {
	Log *slog.Logger
	Now func() time.Time

	cfg   Config
	mu    sync.Mutex
	stats Stats
	last  time.Time
	next  time.Time
}

// New returns the module with logging wired.
func New(log *slog.Logger) *Module {
	return &Module{Log: log, Now: time.Now, stats: Stats{Server: map[string]Stat{}}}
}

func (m *Module) Name() string { return "cleanup" }

func (m *Module) Describe() string {
	return "Cleanup: deletes demos, logs and crash dumps in every volume on a schedule, one rule set for the node"
}

func (m *Module) Questions() []module.Question {
	d := Defaults()
	return []module.Question{
		{Key: "every_hours", Prompt: "Hours between cleanup passes", Default: strconv.Itoa(d.EveryHours), Kind: module.Int,
			Help: "0 = only when a server starts. Files are deleted by the rules in /etc/cs2node/config.json, the same rules the egg used to run at boot"},
		{Key: "on_start", Prompt: "Also clean when a server starts", Default: "true", Kind: module.Bool,
			Help: "what the egg did on its own; this way it no longer holds up the boot"},
		{Key: "keep_open", Prompt: "Leave files the server still has open", Default: "true", Kind: module.Bool,
			Help: "deleting an open file frees no disk until the server restarts, so it is only noise; turn this off to delete anyway"},
	}
}

// Notes tells the operator what changes on the server side.
func (m *Module) Notes() []string {
	return []string{
		"Servers stop running their own cleanup at boot: the node takes it over and they start that much sooner.",
		"The rules live in /etc/cs2node/config.json under modules.cleanup.rules. A server's egg/configs/cleanup.json is only used when it has no node.",
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
	m.stats = LoadStats(StateDir)
	m.Log.Info("cleanup up", "rules", len(m.cfg.Rules), "every_hours", m.cfg.EveryHours, "on_start", m.cfg.OnStart)
	c.ProvideStatus(m.Name(), func() any { return m.Snapshot() })

	events, stop := c.Subscribe()
	defer stop()

	every := time.Duration(m.cfg.EveryHours) * time.Hour
	var tick <-chan time.Time
	var timer *time.Timer
	if every > 0 {
		timer = time.NewTimer(every)
		defer timer.Stop()
		tick = timer.C
		m.setNext(m.Now().Add(every))
	}
	// the core emits Start for everything already running, but the module
	// may have subscribed a moment too late; this sweep catches what it
	// missed and the debounce keeps it from cleaning anything twice
	var sweep <-chan time.Time
	if m.cfg.OnStart {
		sweep = time.After(10 * time.Second)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sweep:
			sweep = nil
			for _, cont := range c.Containers() {
				if cont.Volume != "" && m.due(cont.Name) {
					m.RunOne(c, cont)
				}
			}
		case ev := <-events:
			// the core repeats Start for every running container whenever it
			// reconnects to docker, so a pass that just ran is not repeated
			if m.cfg.OnStart && ev.Kind == core.Start && ev.Container.Volume != "" && m.due(ev.Container.Name) {
				m.RunOne(c, ev.Container)
			}
		case <-tick:
			m.RunAll(ctx, c, "")
			timer.Reset(every)
			m.setNext(m.Now().Add(every))
		}
	}
}

// StartDebounce is how long a server is left alone after a pass before a
// Start event can trigger another one.
const StartDebounce = 5 * time.Minute

// Due reports whether a pass that last ran at last should run again now. A
// server with no pass yet is always due.
func Due(last, now time.Time) bool {
	return last.IsZero() || now.Sub(last) >= StartDebounce
}

func (m *Module) due(container string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Due(m.stats.Server[container].LastRun, m.Now())
}

// RunAll cleans every running server (or one), in turn.
func (m *Module) RunAll(ctx context.Context, c *core.Core, only string) []Run {
	var done []Run
	for _, cont := range c.Containers() {
		if only != "" && cont.Name != only {
			continue
		}
		if cont.Volume == "" || ctx.Err() != nil {
			continue
		}
		if r, ok := m.RunOne(c, cont); ok {
			done = append(done, r)
		}
	}
	return done
}

// RunOne cleans one server's volume and folds the result into the totals.
func (m *Module) RunOne(c *core.Core, cont core.Container) (Run, bool) {
	root, err := os.OpenRoot(cont.Volume)
	if err != nil {
		m.Log.Warn("cleanup skipped", "container", cont.Name, "err", err)
		return Run{}, false
	}
	defer root.Close()
	opt := engine.Options{Now: m.Now(), Bases: append([]string{cont.Volume}, ContainerBases...)}
	if m.cfg.KeepOpen {
		if held := openFiles(cont.PID, cont.Volume); held != nil {
			opt.Skip = func(rel string) bool { _, ok := held[rel]; return ok }
		}
	}
	start := time.Now()
	res := engine.Run(root, m.cfg.Rules, opt)
	run := Run{Container: cont.Name, Time: opt.Now, Seconds: time.Since(start).Seconds(),
		Files: res.Files, Bytes: res.Bytes, Skipped: res.Skipped, PerRule: res.PerRule}
	for _, e := range res.Errors {
		m.Log.Warn("cleanup error", "container", cont.Name, "err", e)
	}
	m.mu.Lock()
	m.last = opt.Now
	Fold(&m.stats, run)
	m.mu.Unlock()
	SaveStats(StateDir, m.Snapshot().Stats)
	if res.Files > 0 {
		m.Log.Info("cleanup done", "container", cont.Name, "files", res.Files, "bytes", res.Bytes, "skipped", res.Skipped)
		c.Notify(core.NoticeCleanupDone, cont.Name, map[string]any{"files": res.Files, "bytes": res.Bytes})
	}
	return run, true
}

func (m *Module) setNext(t time.Time) {
	m.mu.Lock()
	m.next = t
	m.mu.Unlock()
}

// Snapshot is the module's slice of `cs2node status`.
func (m *Module) Snapshot() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{Stats: m.stats.clone(), LastRun: m.last, NextRun: m.next, Rules: len(m.cfg.Rules), EveryHours: m.cfg.EveryHours}
}

// Status is the cleanup section of the control snapshot.
type Status struct {
	Stats      Stats     `json:"stats"`
	LastRun    time.Time `json:"last_run"`
	NextRun    time.Time `json:"next_run"`
	Rules      int       `json:"rules"`
	EveryHours int       `json:"every_hours"`
}

// Configure loads the config outside Run (CLI use).
func (m *Module) Configure(c *core.Core) error {
	m.cfg = Defaults()
	if err := c.Config(m.Name(), &m.cfg); err != nil {
		return err
	}
	m.stats = LoadStats(StateDir)
	return m.cfg.Check()
}

// Cfg is the loaded config.
func (m *Module) Cfg() Config { return m.cfg }

// Doctor checks the rules and what the last passes removed.
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "Cleanup"
	cfg := Defaults()
	if err := c.Config(m.Name(), &cfg); err != nil {
		return []doctor.Check{doctor.Failf(s, "config section unreadable: "+err.Error())}
	}
	if err := cfg.Check(); err != nil {
		return []doctor.Check{doctor.Failf(s, "config: "+err.Error())}
	}
	enabled := 0
	for _, r := range cfg.Rules {
		if r.IsEnabled() {
			enabled++
		}
	}
	out := []doctor.Check{}
	switch {
	case enabled == 0:
		out = append(out, doctor.Warnf(s, "no enabled rule, nothing will ever be deleted"))
	case cfg.EveryHours == 0 && !cfg.OnStart:
		out = append(out, doctor.Warnf(s, fmt.Sprintf("%d rule(s), but every_hours is 0 and on_start is off: the module never runs", enabled)))
	case cfg.EveryHours == 0:
		out = append(out, doctor.Ok(s, fmt.Sprintf("%d rule(s), on server start only", enabled)))
	default:
		out = append(out, doctor.Ok(s, fmt.Sprintf("%d rule(s), every %dh", enabled, cfg.EveryHours)))
	}
	st := LoadStats(StateDir)
	if st.Total.Files > 0 {
		out = append(out, doctor.Ok(s, fmt.Sprintf("%d file(s) and %s removed so far", st.Total.Files, ui.Size(st.Total.Bytes))))
	}
	for _, cont := range c.Containers() {
		switch v, ok := st.Server[cont.Name]; {
		case !ok:
			out = append(out, doctor.Warnf(s, cont.Name+": no cleanup pass yet"))
		case time.Since(v.LastRun) > 48*time.Hour && cfg.EveryHours > 0:
			out = append(out, doctor.Warnf(s, fmt.Sprintf("%s: last pass %s ago", cont.Name, time.Since(v.LastRun).Round(time.Hour))))
		default:
			out = append(out, doctor.Ok(s, fmt.Sprintf("%s: %d file(s), %s removed, last pass %s ago",
				cont.Name, v.Files, ui.Size(v.Bytes), time.Since(v.LastRun).Round(time.Minute))))
		}
	}
	return out
}
