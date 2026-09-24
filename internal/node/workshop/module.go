// SPDX-License-Identifier: GPL-3.0-or-later

package workshop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// Config is the "workshop" section of the node config.
type Config struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir"`   // the node's copy of every item
	Mount   string `json:"mount"` // where Dir appears inside every container
	// Seed hands every cached item to every server, so an addon another
	// server already downloaded costs the next one nothing.
	Seed         bool `json:"seed"`
	KeepDays     int  `json:"keep_days"`     // versions nothing links to any more
	CheckMinutes int  `json:"check_minutes"` // how often Steam is asked what is current
}

// Defaults is what the wizard offers.
func Defaults() Config {
	return Config{Enabled: true, Dir: "/srv/cs2-workshop", Mount: "/tmp/cs2-workshop", Seed: true, KeepDays: 30, CheckMinutes: 360}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	switch {
	case c.Dir == "" || c.Dir[0] != '/':
		return errors.New("dir must be an absolute path")
	case c.Mount == "" || c.Mount[0] != '/':
		return errors.New("mount must be an absolute path")
	case c.KeepDays < 0:
		return errors.New("keep_days cannot be negative")
	case c.CheckMinutes < 5:
		return errors.New("check_minutes must be at least 5")
	}
	return nil
}

// Module is the workshop cache.
type Module struct {
	Log  *slog.Logger
	HTTP *http.Client

	cfg   Config
	store Store
	mu    sync.Mutex
	// upstream is what Steam last said is current, per item. Staleness is
	// decided against the store when a server asks, so the first server to
	// fetch an update spares every other one the download.
	upstream map[string]string
	last     map[string]Report
	syncing  map[string]bool // one sync per container at a time
}

// New returns the module with logging and HTTP wired.
func New(log *slog.Logger, client *http.Client) *Module {
	return &Module{Log: log, HTTP: client, upstream: map[string]string{}, last: map[string]Report{}}
}

func (m *Module) Name() string { return "workshop" }

func (m *Module) Describe() string {
	return "Workshop cache: maps, models, skins and every other workshop addon stored once per node instead of once per server"
}

func (m *Module) Questions() []module.Question {
	d := Defaults()
	return []module.Question{
		{Key: "dir", Prompt: "Where the shared workshop content lives", Default: d.Dir, Help: "maps, models, skins, anything a server or MultiAddonManager downloads; keep it on the same filesystem as the server volumes, then taking an addon over is instant"},
		{Key: "seed", Prompt: "Offer every cached addon to every server", Default: "true", Kind: module.Bool,
			Help: "an addon one server downloaded costs the next one nothing; turn it off to leave each server with only what it fetched itself"},
		{Key: "keep_days", Prompt: "Days to keep an addon version nothing uses", Default: strconv.Itoa(d.KeepDays), Kind: module.Int, Help: "0 = keep forever"},
		{Key: "check_minutes", Prompt: "Minutes between workshop update checks", Default: strconv.Itoa(d.CheckMinutes), Kind: module.Int},
	}
}

// Notes is what the installer prints once the module is on.
func (m *Module) Notes() []string {
	return []string{
		"Servers move into the cache on their next boot: what they downloaded goes to the node and comes back as links, so it stops counting against their disk.",
		"When an addon is updated on the workshop, one server fetches the new version and every other one is relinked to it without downloading.",
		"Leave MultiAddonManager's mm_addon_mount_download and AddonsManager's RedownloadAddonOnMount off: they re-download an addon that is already installed, which throws the sharing away.",
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
	os.Chmod(m.cfg.Dir, 0o755)
	m.store = Store{Dir: m.cfg.Dir, Mount: m.cfg.Mount}
	items := m.store.Items()
	m.Log.Info("workshop up", "dir", m.cfg.Dir, "mount", m.cfg.Mount, "items", len(items), "seed", m.cfg.Seed)

	c.ProvideStatus(m.Name(), func() any { return m.status() })
	// the mount must exist before the server process starts
	events, unsub := c.Subscribe()
	defer unsub()
	// the egg asks once per boot, after its hello and before the server runs
	// one sync per container at a time: a 34 byte line on the socket kicks
	// off a full store walk, and a container that sends it in a loop would
	// otherwise spawn one goroutine per line until the daemon falls over
	c.Handle(proto.TypeWorkshopSync, func(container string, _ proto.Message) {
		if !m.claim(container) {
			m.Log.Debug("workshop sync already running", "container", container)
			return
		}
		go func() {
			defer m.release(container)
			m.sync(c, container)
		}()
	})
	go m.updateLoop(ctx, c)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if ev.Kind == core.Start {
				go m.mount(ev.Container)
			}
		}
	}
}

func (m *Module) mount(cont core.Container) {
	if err := core.BindReadOnly(cont.PID, m.cfg.Dir, m.cfg.Mount); err != nil {
		m.Log.Warn("workshop mount failed", "container", cont.Name, "err", err)
	}
}

// sync answers the egg's boot request: the server is not running yet, so
// the volume can be reshaped safely.
func (m *Module) sync(c *core.Core, container string) {
	var cont core.Container
	for _, x := range c.Containers() {
		if x.Name == container {
			cont = x
		}
	}
	reply := &proto.WorkshopReady{}
	if cont.Volume == "" {
		reply.Err = "volume unknown"
		c.Send(container, reply)
		return
	}
	m.mount(cont) // the links are useless without it
	stale := m.staleNow()
	rep, err := m.store.Sync(cont.Volume, cont.Owner, m.cfg.Seed, stale)
	if err != nil {
		reply.Err = err.Error()
		m.Log.Warn("workshop sync failed", "container", container, "err", err)
	} else {
		m.mu.Lock()
		m.last[container] = rep
		m.mu.Unlock()
		reply.Absorbed, reply.Linked, reply.Seeded, reply.Released = len(rep.Absorbed), len(rep.Linked), len(rep.Seeded), len(rep.Released)
		reply.Freed = rep.Freed
		if n := len(rep.Absorbed) + len(rep.Linked) + len(rep.Seeded) + len(rep.Released); n > 0 {
			m.Log.Info("workshop synced", "container", container, "absorbed", len(rep.Absorbed), "linked", len(rep.Linked),
				"seeded", len(rep.Seeded), "released", len(rep.Released), "freed_mib", rep.Freed>>20)
		}
	}
	c.Send(container, reply)
}

// updateLoop asks Steam which items moved on, so a stale one is handed back
// to its servers as a real file instead of failing to update against a
// read-only link.
func (m *Module) updateLoop(ctx context.Context, c *core.Core) {
	t := time.NewTicker(time.Duration(m.cfg.CheckMinutes) * time.Minute)
	defer t.Stop()
	for {
		m.checkUpdates(ctx, c)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Module) checkUpdates(ctx context.Context, c *core.Core) {
	seen := map[string]bool{}
	var ids []string
	for _, it := range m.store.Items() {
		if !seen[it.ID] {
			seen[it.ID] = true
			ids = append(ids, it.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	details, err := Details(ctx, m.HTTP, ids)
	if err != nil {
		m.Log.Debug("workshop update check failed", "err", err)
		return
	}
	m.mu.Lock()
	for _, id := range ids {
		if d, ok := details[id]; ok && !d.Missing && d.Manifest != "" {
			m.upstream[id] = d.Manifest
		}
	}
	m.mu.Unlock()
	if n := len(m.staleNow()); n > 0 {
		m.Log.Info("workshop items updated upstream, the first server to boot fetches the new version for everyone", "count", n)
	}
}

// staleNow lists the items whose newest stored version is behind what
// Steam last reported. It clears itself the moment one server has fetched
// the update and the node absorbed it.
func (m *Module) staleNow() map[string]bool {
	m.mu.Lock()
	upstream := make(map[string]string, len(m.upstream))
	for k, v := range m.upstream {
		upstream[k] = v
	}
	m.mu.Unlock()
	stale := map[string]bool{}
	all := m.store.NewestAll() // one walk, not one per tracked item
	for id, want := range upstream {
		newest, have := all[id]
		// a stored version with no usable id cannot be compared with what
		// Steam reports, so it is never out of date, it is just old
		if have && ValidManifest(newest.Manifest) && newest.Manifest != want {
			stale[id] = true
		}
	}
	return stale
}

// status is the workshop section of the control snapshot.
type Status struct {
	Dir    string `json:"dir"`
	Items  int    `json:"items"`
	Bytes  int64  `json:"bytes"`
	Stale  int    `json:"stale"`
	Seed   bool   `json:"seed"`
	Shared int64  `json:"shared"` // bytes the servers do not hold because of the links
}

func (m *Module) status() Status {
	st := Status{Dir: m.cfg.Dir, Seed: m.cfg.Seed}
	ids := map[string]bool{}
	for _, it := range m.store.Items() {
		st.Bytes += it.Bytes
		ids[it.ID] = true
	}
	st.Items = len(ids)
	st.Stale = len(m.staleNow())
	m.mu.Lock()
	for _, rep := range m.last {
		st.Shared += rep.Freed
	}
	m.mu.Unlock()
	return st
}

// Doctor checks the store and the mounts.
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "Workshop"
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
	store := Store{Dir: cfg.Dir, Mount: cfg.Mount}
	var bytes int64
	ids := map[string]bool{}
	for _, it := range store.Items() {
		bytes += it.Bytes
		ids[it.ID] = true
	}
	out := []doctor.Check{doctor.Ok(s, fmt.Sprintf("%d item(s), %.1f GiB under %s", len(ids), float64(bytes)/(1<<30), cfg.Dir))}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(cfg.Dir, &fs); err == nil {
		free := uint64(fs.Bavail) * uint64(fs.Bsize)
		if free < 5<<30 {
			out = append(out, doctor.Warnf(s, fmt.Sprintf("only %.1f GiB free under %s", float64(free)/float64(1<<30), cfg.Dir)))
		}
	}
	for _, cont := range c.Containers() {
		switch {
		case cont.PID <= 0:
		case !core.Mounted(cont.PID, cfg.Mount):
			out = append(out, doctor.Warnf(s, cont.Name+": shared workshop dir not mounted at "+cfg.Mount+" (kernel < 5.2, or the daemon was not running when the server started)"))
		default:
			out = append(out, doctor.Ok(s, cont.Name+": mount in place, "+strconv.Itoa(len(Used(store, cont.Volume)))+" linked item(s)"))
		}
	}
	return out
}

// claim reserves the one sync slot a container gets. The egg asks once per
// boot; anything past that is either a retry or a flood, and both are
// answered by the sync already in flight.
func (m *Module) claim(container string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.syncing == nil {
		m.syncing = map[string]bool{}
	}
	if m.syncing[container] {
		return false
	}
	m.syncing[container] = true
	return true
}

func (m *Module) release(container string) {
	m.mu.Lock()
	delete(m.syncing, container)
	m.mu.Unlock()
}
