// SPDX-License-Identifier: GPL-3.0-or-later

// Package maintenance is the node module that keeps the VPS itself tidy:
// dangling docker images, the build cache, old journal files, and a free-disk
// and pending-reboot watch. It never touches a server volume; that is cleanup.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// Config is the "maintenance" section of the node config.
type Config struct {
	Enabled         bool `json:"enabled"`
	EveryHours      int  `json:"every_hours"` // 0 = only on demand
	DryRun          bool `json:"dry_run"`     // report what would go, delete nothing
	Images          bool `json:"images"`
	BuildCache      bool `json:"build_cache"`
	BuildCacheHours int  `json:"build_cache_hours"` // keep cache newer than this
	Journal         bool `json:"journal"`
	JournalDays     int  `json:"journal_days"`
	DiskWarnPercent int  `json:"disk_warn_percent"` // 0 = no disk watch
}

// Defaults is what the wizard offers: weekly, every task on, and a dry run
// until the operator has read what it would have done.
func Defaults() Config {
	return Config{Enabled: true, EveryHours: 168, DryRun: true, Images: true, BuildCache: true, BuildCacheHours: 168,
		Journal: true, JournalDays: 14, DiskWarnPercent: 15}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	switch {
	case c.EveryHours < 0 || c.EveryHours > 24*90:
		return errors.New("every_hours must be 0..2160")
	case c.BuildCache && c.BuildCacheHours < 1:
		return errors.New("build_cache_hours must be at least 1")
	case c.Journal && (c.JournalDays < 1 || c.JournalDays > 3650):
		return errors.New("journal_days must be 1..3650")
	case c.DiskWarnPercent < 0 || c.DiskWarnPercent > 90:
		return errors.New("disk_warn_percent must be 0..90")
	}
	return nil
}

// Module is the maintenance feature.
type Module struct {
	Log    *slog.Logger
	Now    func() time.Time
	Exec   Exec
	Docker Engine // nil = connect on the first pass

	cfg          Config
	mu           sync.Mutex
	stats        Stats
	next         time.Time
	rebootSeen   bool
	diskWarnedAt time.Time
}

// New returns the module with logging wired.
func New(log *slog.Logger) *Module {
	return &Module{Log: log, Now: time.Now, Exec: RunCmd, stats: Stats{Task: map[string]TaskStat{}}}
}

func (m *Module) Name() string { return "maintenance" }

func (m *Module) Describe() string {
	return "Maintenance: prunes unused docker images and build cache, trims the journal, watches free disk and pending reboots"
}

func (m *Module) Questions() []module.Question {
	d := Defaults()
	on := func(a map[string]any) bool { return a["journal"] == true }
	return []module.Question{
		{Key: "every_hours", Prompt: "Hours between maintenance passes", Default: strconv.Itoa(d.EveryHours), Kind: module.Int,
			Help: "168 = weekly. 0 = only when you run: cs2node maintenance now"},
		{Key: "dry_run", Prompt: "Dry run first (report, delete nothing)", Default: "true", Kind: module.Bool,
			Help: "read `cs2node maintenance` for a pass or two, then turn this off"},
		{Key: "images", Prompt: "Prune dangling docker images", Default: "true", Kind: module.Bool,
			Help: "untagged <none> images no container uses; tagged images are never touched"},
		{Key: "build_cache", Prompt: "Prune the docker build cache", Default: "true", Kind: module.Bool},
		{Key: "journal", Prompt: "Trim old journal files", Default: "true", Kind: module.Bool},
		{Key: "journal_days", Prompt: "Days of journal to keep", Default: strconv.Itoa(d.JournalDays), Kind: module.Int, When: on},
		{Key: "disk_warn_percent", Prompt: "Alert when free disk drops below this percent", Default: strconv.Itoa(d.DiskWarnPercent), Kind: module.Int,
			Help: "0 = no disk watch. Checks / once an hour"},
	}
}

// Notes tells the operator what else is on offer.
func (m *Module) Notes() []string {
	return []string{
		"It starts as a dry run: the first passes only report what they would remove (cs2node maintenance).",
		"Unattended security upgrades are separate and opt-in: cs2node maintenance setup.",
	}
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

// SetConfig replaces the config without a core, for tests.
func (m *Module) SetConfig(c Config) { m.cfg = c }

// Cfg is the loaded config.
func (m *Module) Cfg() Config { return m.cfg }

// Run serves until ctx ends.
func (m *Module) Run(ctx context.Context, c *core.Core) error {
	if err := m.Configure(c); err != nil {
		return err
	}
	m.Log.Info("maintenance up", "every_hours", m.cfg.EveryHours, "dry_run", m.cfg.DryRun)
	c.ProvideStatus(m.Name(), func() any { return m.Snapshot() })

	var pass <-chan time.Time
	if every := time.Duration(m.cfg.EveryHours) * time.Hour; every > 0 {
		t := time.NewTicker(every)
		defer t.Stop()
		pass = t.C
		m.setNext(m.Now().Add(every))
	}
	watch := time.NewTicker(time.Hour)
	defer watch.Stop()
	m.Watch(c)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-watch.C:
			m.Watch(c)
		case <-pass:
			m.Pass(ctx, c, m.cfg.DryRun)
			m.setNext(m.Now().Add(time.Duration(m.cfg.EveryHours) * time.Hour))
		}
	}
}

// Pass runs every enabled task once and folds the result into the totals.
func (m *Module) Pass(ctx context.Context, c *core.Core, dry bool) []Result {
	var res []Result
	if m.cfg.Images || m.cfg.BuildCache {
		if eng, err := m.engine(); err != nil {
			m.Log.Warn("maintenance: docker unreachable, skipping image tasks", "err", err)
		} else {
			if m.cfg.Images {
				res = append(res, pruneImages(ctx, eng, dry))
			}
			if m.cfg.BuildCache {
				res = append(res, pruneBuildCache(ctx, eng, m.cfg.BuildCacheHours, dry))
			}
		}
	}
	if m.cfg.Journal {
		res = append(res, vacuumJournal(ctx, m.Exec, m.cfg.JournalDays, dry))
	}
	var freed int64
	for _, r := range res {
		if r.Err != "" {
			m.Log.Warn("maintenance task failed", "task", r.Task, "err", r.Err)
			continue
		}
		m.Log.Info("maintenance task", "task", r.Task, "items", r.Items, "bytes", r.Bytes, "dry", dry)
		freed += r.Bytes
	}
	m.mu.Lock()
	Fold(&m.stats, m.Now(), res, dry)
	snap := m.stats
	m.mu.Unlock()
	if err := SaveStats(StateDir, snap); err != nil {
		m.Log.Warn("maintenance stats not saved", "err", err)
	}
	if !dry && freed > 0 && c != nil {
		c.Notify(core.NoticeMaintenanceDone, "", map[string]any{"bytes": freed})
	}
	return res
}

func (m *Module) engine() (Engine, error) {
	if m.Docker == nil {
		e, err := NewEngine()
		if err != nil {
			return nil, err
		}
		m.Docker = e
	}
	return m.Docker, nil
}

// diskWarnEvery keeps a node that stays full from alerting every hour.
const diskWarnEvery = 24 * time.Hour

// Watch is the hourly look at free disk and a pending reboot. Both only
// notify; neither fixes anything, a reboot in particular drops every player.
func (m *Module) Watch(c *core.Core) {
	if m.cfg.DiskWarnPercent > 0 {
		if free, err := FreePercent("/"); err == nil && free < float64(m.cfg.DiskWarnPercent) && m.Now().Sub(m.diskWarnedAt) >= diskWarnEvery {
			m.diskWarnedAt = m.Now()
			m.Log.Warn("free disk is low", "free_percent", int(free), "threshold", m.cfg.DiskWarnPercent)
			c.Notify(core.NoticeDiskLow, "", map[string]any{"free_percent": int(free)})
		}
	}
	pending := RebootRequired()
	if pending && !m.rebootSeen {
		m.Log.Warn("a reboot is required to finish an upgrade")
		c.Notify(core.NoticeRebootRequired, "", nil)
	}
	m.rebootSeen = pending
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
	return Status{Stats: m.stats, NextRun: m.next, EveryHours: m.cfg.EveryHours, DryRun: m.cfg.DryRun}
}

// Doctor checks the settings, the host and what the last pass did.
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "Maintenance"
	cfg := Defaults()
	if err := c.Config(m.Name(), &cfg); err != nil {
		return []doctor.Check{doctor.Failf(s, "config section unreadable: "+err.Error())}
	}
	if err := cfg.Check(); err != nil {
		return []doctor.Check{doctor.Failf(s, "config: "+err.Error())}
	}
	var out []doctor.Check
	switch {
	case cfg.DryRun:
		out = append(out, doctor.Warnf(s, "dry_run is on: passes report, nothing is deleted (set it to false when the report looks right)"))
	case cfg.EveryHours == 0:
		out = append(out, doctor.Ok(s, "live, only on demand (cs2node maintenance now)"))
	default:
		out = append(out, doctor.Ok(s, fmt.Sprintf("live, every %dh", cfg.EveryHours)))
	}
	if st := LoadStats(StateDir); !st.Last.IsZero() {
		out = append(out, doctor.Ok(s, fmt.Sprintf("last pass %s ago, %s freed in total", time.Since(st.Last).Round(time.Minute), ui.Size(st.Total()))))
	}
	if cfg.DiskWarnPercent > 0 {
		if free, err := FreePercent("/"); err == nil {
			if free < float64(cfg.DiskWarnPercent) {
				out = append(out, doctor.Warnf(s, fmt.Sprintf("only %d%% of / is free", int(free))))
			} else {
				out = append(out, doctor.Ok(s, fmt.Sprintf("%d%% of / is free", int(free))))
			}
		}
	}
	if RebootRequired() {
		out = append(out, doctor.Warnf(s, "a reboot is waiting to finish an upgrade ("+RebootFile+")"))
	}
	if _, err := os.Stat(aptDir); err == nil {
		if on, _ := activeUpgrades(ctx, m.Exec); on {
			out = append(out, doctor.Ok(s, "unattended upgrades are on"))
		} else {
			out = append(out, doctor.Warnf(s, "unattended upgrades are off (cs2node maintenance setup)"))
		}
	}
	return out
}

// Summary renders a result list as plain lines for the CLI.
func Summary(res []Result) []string {
	lines := make([]string, 0, len(res))
	for _, r := range res {
		var b strings.Builder
		b.WriteString(r.Task)
		switch {
		case r.Err != "":
			b.WriteString(": " + r.Err)
		case r.Dry && r.Items == 0 && r.Bytes == 0:
			b.WriteString(": " + r.Note) // sizes are only known for images
		case r.Dry:
			fmt.Fprintf(&b, ": would free up to %s (%d item(s)). %s", ui.Size(r.Bytes), r.Items, r.Note)
		default:
			fmt.Fprintf(&b, ": %s freed (%d item(s))", ui.Size(r.Bytes), r.Items)
		}
		lines = append(lines, b.String())
	}
	return lines
}
