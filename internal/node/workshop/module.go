// SPDX-License-Identifier: GPL-3.0-or-later

package workshop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
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
	PruneHours   int  `json:"prune_hours"`   // 0 = only by hand
}

// PruneDelay holds the first prune back until the core has listed the
// running servers.
var PruneDelay = time.Minute

// Defaults is what the wizard offers.
func Defaults() Config {
	return Config{Enabled: true, Dir: "/srv/cs2-workshop", Mount: "/tmp/cs2-workshop", Seed: true, KeepDays: 7, CheckMinutes: 360, PruneHours: 24}
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
	case c.PruneHours < 0:
		return errors.New("prune_hours cannot be negative")
	}
	return nil
}

// Module is the workshop cache.
type Module struct {
	Log  *slog.Logger
	HTTP *http.Client

	cfg     Config
	store   Store
	mu      sync.Mutex
	storeMu sync.RWMutex // syncs share the store, a prune takes it alone
	// upstream is what Steam last said is current, per item. Staleness is
	// decided against the store when a server asks, so the first server to
	// fetch an update spares every other one the download.
	upstream map[string]Detail
	checked  time.Time
	checking chan struct{} // one Steam check at a time
	last     map[string]Report
	syncing  map[string]bool // one sync per container at a time
}

// New returns the module with logging and HTTP wired.
func New(log *slog.Logger, client *http.Client) *Module {
	return &Module{Log: log, HTTP: client, upstream: map[string]Detail{}, checking: make(chan struct{}, 1), last: map[string]Report{}}
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
		{Key: "keep_days", Prompt: "Days to keep an addon version nothing uses", Default: strconv.Itoa(d.KeepDays), Kind: module.Int,
			Help: "with seed on, used means a running server had it loaded; 0 = keep forever"},
		{Key: "prune_hours", Prompt: "Hours between clearing out addon versions nothing uses", Default: strconv.Itoa(d.PruneHours), Kind: module.Int,
			Help: "with seed off, a server stopped in the panel still counts as using its addons; 0 = only by hand, with cs2node workshop prune"},
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
	m.store = Store{Dir: m.cfg.Dir, Mount: m.cfg.Mount, KeepDays: m.cfg.KeepDays}
	ctx, stop := context.WithCancel(ctx)
	var loops sync.WaitGroup
	defer func() { stop(); loops.Wait() }() // loops are gone once Run returns
	if m.cfg.Seed && m.cfg.KeepDays > 0 {
		m.markInUse() // before any sync judges what is idle
		loops.Go(func() { m.watchLoop(ctx) })
	}
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
			m.sync(ctx, c, container)
		}()
	})
	loops.Go(func() { m.updateLoop(ctx) })
	if m.cfg.PruneHours > 0 {
		loops.Go(func() { m.pruneLoop(ctx, c) })
	}
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
func (m *Module) sync(ctx context.Context, c *core.Core, container string) {
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
	// ask Steam now: an update since the last check would get linked in its old version
	fresh, cancel := context.WithTimeout(ctx, 5*time.Second)
	m.checkUpdates(fresh, time.Minute)
	m.checkNew(fresh, cont.Volume)
	cancel()
	m.storeMu.RLock()
	rep, err := m.store.Sync(cont.Volume, cont.Owner, m.cfg.Seed, m.upstreamNow())
	m.storeMu.RUnlock()
	if err != nil {
		reply.Err = err.Error()
		m.Log.Warn("workshop sync failed", "container", container, "err", err)
	}
	m.mu.Lock()
	m.last[container] = rep
	m.mu.Unlock()
	reply.Absorbed, reply.Linked, reply.Seeded, reply.Released = len(rep.Absorbed), len(rep.Linked), len(rep.Seeded), len(rep.Released)
	reply.Freed = rep.Freed
	if n := len(rep.Absorbed) + len(rep.Linked) + len(rep.Seeded) + len(rep.Released); n > 0 {
		m.Log.Info("workshop synced", "container", container, "absorbed", len(rep.Absorbed), "linked", len(rep.Linked),
			"seeded", len(rep.Seeded), "released", len(rep.Released), "freed_mib", rep.Freed>>20)
	}
	c.Send(container, reply)
}

// updateLoop asks Steam which items moved on, so `cs2node status` shows
// them before a server boots. Each sync asks again for itself.
func (m *Module) updateLoop(ctx context.Context) {
	t := time.NewTicker(time.Duration(m.cfg.CheckMinutes) * time.Minute)
	defer t.Stop()
	for {
		check, cancel := context.WithTimeout(ctx, 30*time.Second)
		m.checkUpdates(check, 0)
		cancel()
		if n := len(m.staleNow()); n > 0 {
			m.Log.Info("workshop items updated upstream, the first server to boot fetches the new version for everyone", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// watchLoop restarts the clock of whatever a server has open, every minute.
func (m *Module) watchLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.markInUse()
		}
	}
}

func (m *Module) markInUse() {
	if err := m.store.MarkInUse("/proc", time.Now()); err != nil {
		m.Log.Warn("workshop watch marker", "err", err)
	}
}

// pruneLoop clears what nothing used for keep_days: once after start, then
// every prune_hours. Stopped servers' volumes are read too.
func (m *Module) pruneLoop(ctx context.Context, c *core.Core) {
	t := time.NewTimer(PruneDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		live := c.Containers()
		running := map[string]bool{}
		for _, cont := range live {
			running[cont.Name] = true
		}
		m.storeMu.Lock()
		n, freed := m.store.Prune(live, StoppedVolumes(running), m.cfg.Seed, time.Now())
		m.storeMu.Unlock()
		if n > 0 {
			m.Log.Info("workshop pruned", "versions", n, "freed_mib", freed>>20)
		}
		t.Reset(time.Duration(m.cfg.PruneHours) * time.Hour)
	}
}

// checkUpdates refreshes upstream, unless the last check is younger than
// maxAge. On error the last answer stays.
func (m *Module) checkUpdates(ctx context.Context, maxAge time.Duration) {
	select {
	case m.checking <- struct{}{}:
		defer func() { <-m.checking }()
	case <-ctx.Done():
		return
	}
	m.mu.Lock()
	recent := time.Since(m.checked) < maxAge
	m.mu.Unlock()
	if recent {
		return
	}
	latest, err := Latest(ctx, m.HTTP, m.store.IDs())
	if err != nil {
		m.Log.Debug("workshop update check failed", "err", err)
		return
	}
	m.mu.Lock()
	maps.Copy(m.upstream, latest)
	m.checked = time.Now()
	m.mu.Unlock()
}

// checkNew asks Steam about the items a volume holds that the node has no
// answer for yet, so an outdated one is dropped instead of absorbed.
func (m *Module) checkNew(ctx context.Context, volume string) {
	ids := VolumeIDs(volume)
	m.mu.Lock()
	ids = slices.DeleteFunc(ids, func(id string) bool { _, ok := m.upstream[id]; return ok })
	m.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	latest, err := Latest(ctx, m.HTTP, ids)
	if err != nil {
		m.Log.Debug("workshop check of new items failed", "err", err)
		return
	}
	m.mu.Lock()
	maps.Copy(m.upstream, latest)
	m.mu.Unlock()
}

// staleNow lists the items whose newest stored version is behind what
// Steam last reported. It clears itself the moment one server has fetched
// the update and the node absorbed it.
func (m *Module) staleNow() map[string]bool {
	return m.store.Stale(m.upstreamNow())
}

func (m *Module) upstreamNow() map[string]Detail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.upstream)
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
