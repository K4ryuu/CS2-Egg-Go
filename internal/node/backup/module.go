// SPDX-License-Identifier: GPL-3.0-or-later

package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/bundles"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
)

// Config is the "backup" section of the node config.
type Config struct {
	Enabled   bool     `json:"enabled"`
	Dir       string   `json:"dir"`
	Hour      int      `json:"hour"`       // local hour of the daily run
	KeepDays  int      `json:"keep_days"`  // 0 = unlimited
	KeepCount int      `json:"keep_count"` // per server, 0 = unlimited
	Include   []string `json:"include"`    // empty = everything
	Exclude   []string `json:"exclude"`
}

// Defaults is what the wizard offers.
func Defaults() Config {
	return Config{Enabled: true, Dir: "/srv/cs2-backups", Hour: 5, KeepDays: 14, KeepCount: 7, Exclude: append([]string(nil), DefaultExclude...)}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	switch {
	case c.Dir == "" || c.Dir[0] != '/':
		return errors.New("dir must be an absolute path")
	case c.Hour < 0 || c.Hour > 23:
		return errors.New("hour must be 0..23")
	case c.KeepDays < 0 || c.KeepCount < 0:
		return errors.New("keep_days and keep_count cannot be negative")
	}
	return nil
}

// Rules builds the file rules, skipping what the vpksync install provides
// when that module is on.
func (c Config) Rules(central string) Rules {
	r := Rules{Exclude: c.Exclude, Central: central}
	for _, p := range c.Include {
		if p == "all" {
			return Rules{Exclude: c.Exclude, Central: central}
		}
		r.Include = append(r.Include, p)
	}
	return r
}

// Module is the backup feature.
type Module struct {
	Log *slog.Logger
	Now func() time.Time

	cfg     Config
	central string
	mu      sync.Mutex
	last    map[string]Meta
	running string // container being archived right now
}

// New returns the module with logging wired.
func New(log *slog.Logger) *Module {
	return &Module{Log: log, Now: time.Now, last: map[string]Meta{}}
}

func (m *Module) Name() string { return "backup" }

func (m *Module) Describe() string {
	return "Backups: a daily tar.gz per server outside the volume (plugins, configs, data; never VPKs or central files)"
}

func (m *Module) Questions() []module.Question {
	d := Defaults()
	return []module.Question{
		{Key: "dir", Prompt: "Where backups are kept", Default: d.Dir, Help: "one subfolder per server, outside the volumes"},
		{Key: "hour", Prompt: "Hour of the daily run (local time)", Default: strconv.Itoa(d.Hour), Kind: module.Int, Help: "0..23"},
		{Key: "keep_days", Prompt: "Days to keep a backup", Default: strconv.Itoa(d.KeepDays), Kind: module.Int, Help: "0 = unlimited"},
		{Key: "keep_count", Prompt: "Backups to keep per server", Default: strconv.Itoa(d.KeepCount), Kind: module.Int, Help: "0 = unlimited"},
		{Key: "include", Prompt: "Paths to back up", Default: "all", Kind: module.List, Help: "volume-relative dirs or globs, e.g. game/csgo/cfg, game/csgo/addons; all = everything the excludes leave"},
		{Key: "exclude", Prompt: "Paths to leave out", Default: strings.Join(d.Exclude, ", "), Kind: module.List, Help: "globs match the whole path, the file name, or a parent dir; files the central CS2 install provides are always left out"},
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
	m.central = centralDir(c)
	for _, meta := range List(m.cfg.Dir, "") {
		if _, ok := m.last[meta.Container]; !ok {
			m.last[meta.Container] = meta
		}
	}
	m.Log.Info("backup up", "dir", m.cfg.Dir, "hour", m.cfg.Hour, "keep_days", m.cfg.KeepDays, "keep_count", m.cfg.KeepCount)
	c.ProvideStatus(m.Name(), func() any {
		m.mu.Lock()
		defer m.mu.Unlock()
		last := make(map[string]Meta, len(m.last))
		for k, v := range m.last {
			last[k] = v
		}
		return status{Last: last, Running: m.running, NextRun: nextRun(m.Now(), m.cfg.Hour)}
	})
	for {
		next := nextRun(m.Now(), m.cfg.Hour)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(next)):
		}
		m.RunAll(ctx, c, "")
	}
}

// RunAll backs up every running server (or one), in turn.
func (m *Module) RunAll(ctx context.Context, c *core.Core, only string) []Meta {
	var done []Meta
	for _, cont := range c.Containers() {
		if only != "" && cont.Name != only {
			continue
		}
		if cont.Volume == "" {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		// filling the node's disk is worse than missing a backup
		if need, free, ok := m.wouldFill(cont); ok {
			m.Log.Warn("backup skipped, not enough free space", "container", cont.Name,
				"needs_mib", need>>20, "free_mib", free>>20, "dir", m.cfg.Dir)
			c.Notify(core.NoticeBackupFailed, cont.Name, map[string]any{
				"err": fmt.Sprintf("needs about %.1f GiB, %.1f GiB free under %s", gib(need), gib(free), m.cfg.Dir)})
			continue
		}
		m.mu.Lock()
		m.running = cont.Name
		m.mu.Unlock()
		meta, err := Archive(m.cfg.Dir, cont.Name, cont.Volume, m.cfg.Rules(m.central), m.Now())
		m.mu.Lock()
		m.running = ""
		if err == nil {
			m.last[cont.Name] = meta
		}
		m.mu.Unlock()
		if err != nil {
			m.Log.Warn("backup failed", "container", cont.Name, "err", err)
			c.Notify(core.NoticeBackupFailed, cont.Name, map[string]any{"err": err.Error()})
			continue
		}
		m.Log.Info("backup done", "container", cont.Name, "files", meta.Files, "mib", meta.Bytes>>20, "seconds", int(meta.Seconds), "path", meta.Path)
		c.Notify(core.NoticeBackupDone, cont.Name, map[string]any{"path": meta.Path, "bytes": meta.Bytes, "files": meta.Files})
		done = append(done, meta)
	}
	if n := bundles.Prune(m.cfg.Dir, m.cfg.KeepCount, m.cfg.KeepDays, m.Now()); n > 0 {
		m.Log.Info("old backups removed", "count", n)
	}
	return done
}

// wouldFill estimates whether archiving this server would run the backup
// directory out of space. The estimate is what the rules would include,
// uncompressed, which is deliberately pessimistic: a tar.gz of plugin files
// lands well under it.
func (m *Module) wouldFill(cont core.Container) (need, free int64, yes bool) {
	need = Estimate(cont.Volume, m.cfg.Rules(m.central))
	if need == 0 {
		return 0, 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(m.cfg.Dir, &st); err != nil {
		return 0, 0, false // cannot tell, so do not stand in the way
	}
	free = int64(st.Bavail) * int64(st.Bsize)
	// leave a gigabyte behind: a full disk takes the whole node with it
	return need, free, need+(1<<30) > free
}

func gib(b int64) float64 { return float64(b) / (1 << 30) }

// Configure loads the config outside Run (CLI use).
func (m *Module) Configure(c *core.Core) error {
	m.cfg = Defaults()
	if err := c.Config(m.Name(), &m.cfg); err != nil {
		return err
	}
	m.central = centralDir(c)
	return m.cfg.Check()
}

// Cfg is the loaded config.
func (m *Module) Cfg() Config { return m.cfg }

// centralDir is the vpksync install dir when that module is enabled.
func centralDir(c *core.Core) string {
	var v struct {
		Enabled bool   `json:"enabled"`
		CS2Dir  string `json:"cs2_dir"`
	}
	if c.Config("vpksync", &v) == nil && v.Enabled {
		return v.CS2Dir
	}
	return ""
}

// nextRun is the next local time at hour after now.
func nextRun(now time.Time, hour int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
	}
	return t
}

// status is the backup section of the control snapshot.
type status struct {
	Last    map[string]Meta `json:"last"`
	Running string          `json:"running,omitempty"`
	NextRun time.Time       `json:"next_run"`
}

// Doctor checks the backup dir, its free space and how fresh the backups are.
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "Backup"
	cfg := Defaults()
	if err := c.Config(m.Name(), &cfg); err != nil {
		return []doctor.Check{doctor.Failf(s, "config section unreadable: "+err.Error())}
	}
	if err := cfg.Check(); err != nil {
		return []doctor.Check{doctor.Failf(s, "config: "+err.Error())}
	}
	st, err := os.Stat(cfg.Dir)
	switch {
	case err != nil:
		return []doctor.Check{doctor.Warnf(s, cfg.Dir+" missing (the daemon creates it on start)")}
	case !st.IsDir():
		return []doctor.Check{doctor.Failf(s, cfg.Dir+" is not a directory")}
	}
	var out []doctor.Check
	var fs syscall.Statfs_t
	if err := syscall.Statfs(cfg.Dir, &fs); err == nil {
		free := uint64(fs.Bavail) * uint64(fs.Bsize)
		if free < 5<<30 {
			out = append(out, doctor.Warnf(s, fmt.Sprintf("only %.1f GiB free under %s", float64(free)/float64(1<<30), cfg.Dir)))
		} else {
			out = append(out, doctor.Ok(s, fmt.Sprintf("%.0f GiB free under %s", float64(free)/float64(1<<30), cfg.Dir)))
		}
	}
	all := List(cfg.Dir, "")
	out = append(out, doctor.Ok(s, fmt.Sprintf("%d backup(s), daily at %02d:00, next %s", len(all), cfg.Hour, nextRun(time.Now(), cfg.Hour).Format("2006-01-02 15:04"))))
	newest := map[string]time.Time{}
	for _, meta := range all {
		if meta.Time.After(newest[meta.Container]) {
			newest[meta.Container] = meta.Time
		}
	}
	var need int64
	for _, cont := range c.Containers() {
		if cont.Volume != "" {
			need += Estimate(cont.Volume, cfg.Rules(centralDir(c)))
		}
		switch t, ok := newest[cont.Name]; {
		case !ok:
			out = append(out, doctor.Warnf(s, cont.Name+": no backup yet"))
		case time.Since(t) > 48*time.Hour:
			out = append(out, doctor.Warnf(s, fmt.Sprintf("%s: last backup %s ago", cont.Name, time.Since(t).Round(time.Hour))))
		default:
			out = append(out, doctor.Ok(s, fmt.Sprintf("%s: last backup %s ago", cont.Name, time.Since(t).Round(time.Minute))))
		}
	}
	// the archives are smaller than this, but a node that cannot even hold
	// the raw bytes is one bad night from a full disk
	if need > 0 {
		if err := syscall.Statfs(cfg.Dir, &fs); err == nil {
			free := int64(fs.Bavail) * int64(fs.Bsize)
			if need+(1<<30) > free {
				out = append(out, doctor.Warnf(s, fmt.Sprintf("the included files of every server are %.1f GiB and only %.1f GiB is free under %s: backups will be skipped rather than fill the disk. Narrow `include`, lower `keep_count`, or move `dir`",
					gib(need), gib(free), cfg.Dir)))
			}
		}
	}
	return out
}
