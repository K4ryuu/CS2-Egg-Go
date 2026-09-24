// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync

import (
	"context"
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

// Module is the vpksync feature.
type Module struct {
	Log  *slog.Logger
	HTTP *http.Client

	cfg     Config
	central sync.RWMutex // write: steamcmd updates the central dir; read: a push runs

	mu      sync.Mutex
	queue   []string // containers waiting for a worker slot, in order
	volumes map[string]*sync.Mutex
	sem     chan struct{}
	last    map[string]string // last status sent per container, for doctor/status
	owed    map[string]*owed  // restarts deferred by the policy
}

// New returns the module with logging and HTTP wired.
func New(log *slog.Logger, client *http.Client) *Module {
	return &Module{Log: log, HTTP: client, volumes: map[string]*sync.Mutex{}, last: map[string]string{}, owed: map[string]*owed{}}
}

func (m *Module) Name() string { return "vpksync" }

func (m *Module) Describe() string {
	return "Centralized VPK sync: one CS2 install on the node, shared into every server"
}

func (m *Module) Questions() []module.Question {
	d := Defaults()
	return []module.Question{
		{Key: "steamcmd_dir", Prompt: "SteamCMD directory", Default: d.SteamcmdDir},
		{Key: "cs2_dir", Prompt: "Central CS2 install directory", Default: d.CS2Dir, Help: "Needs ~35 GB free"},
		{Key: "push_method", Prompt: "How VPK files reach the servers", Default: string(d.PushMethod), Kind: module.Choice, Choices: []module.Option{
			{Value: "symlink", Desc: "read-only bind mount + links, ~0 disk per server (kernel 5.2+)"},
			{Value: "hardlink", Desc: "same inodes, no extra disk, volumes must share the filesystem"},
			{Value: "copy", Desc: "a full copy per server, works anywhere, servers may write into it"},
		}},
		{Key: "max_workers", Prompt: "Parallel pushes", Default: strconv.Itoa(d.MaxWorkers), Kind: module.Int, Help: "How many servers can pull a VPK update from this node at once. Higher rolls out faster but loads the disk and network harder; the rest queue and see their position"},
		{Key: "auto_restart", Prompt: "Restart servers through Wings after a CS2 update", Default: "true", Kind: module.Bool},
		{Key: "restart_policy", Prompt: "When to restart after an update", Default: string(d.RestartPolicy), Kind: module.Choice, Choices: []module.Option{
			{Value: "immediate", Desc: "right after the push, players or not"},
			{Value: "empty", Desc: "once nobody is on (or after the delay below)"},
			{Value: "window", Desc: "once nobody is on, or inside a daily window (or after the delay below)"},
		}, When: restartOn},
		{Key: "restart_window", Prompt: "Restart window (local time)", Default: d.RestartWindow, Help: "HH:MM-HH:MM, may cross midnight", When: func(a map[string]any) bool { return restartOn(a) && a["restart_policy"] == "window" }},
		{Key: "max_delay_hours", Prompt: "Hours to wait at most before restarting anyway", Default: strconv.Itoa(d.MaxDelayHours), Kind: module.Int, Help: "0 = never force", When: heldRestart},
		{Key: "warn_minutes", Prompt: "Minutes of in-game warnings before a forced restart", Default: strconv.Itoa(d.WarnMinutes), Kind: module.Int, Help: "0 = restart without warning", When: heldRestart},
		{Key: "validate", Prompt: "Run SteamCMD with validate", Default: "false", Kind: module.Bool},
		{Key: "check_minutes", Prompt: "Minutes between CS2 update checks", Default: strconv.Itoa(d.CheckMinutes), Kind: module.Int},
	}
}

func restartOn(a map[string]any) bool { return a["auto_restart"] == true }

func heldRestart(a map[string]any) bool {
	return restartOn(a) && (a["restart_policy"] == "empty" || a["restart_policy"] == "window")
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
	m.sem = make(chan struct{}, m.cfg.MaxWorkers)
	os.MkdirAll(m.cfg.CS2Dir, 0o755)
	m.Log.Info("vpksync up", "cs2_dir", m.cfg.CS2Dir, "method", m.cfg.PushMethod, "workers", m.cfg.MaxWorkers, "build", BuildID(m.cfg.CS2Dir))

	c.ProvideStatus(m.Name(), func() any {
		m.mu.Lock()
		defer m.mu.Unlock()
		last := make(map[string]string, len(m.last))
		for k, v := range m.last {
			last[k] = v
		}
		pending := make(map[string]string, len(m.owed))
		for k, o := range m.owed {
			pending[k] = o.since.Format(time.RFC3339)
		}
		return status{Build: BuildID(m.cfg.CS2Dir), Last: last, Pending: pending}
	})
	events, unsub := c.Subscribe()
	defer unsub()
	go m.updateLoop(ctx, c)
	go m.restartLoop(ctx, c)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			switch ev.Kind {
			case core.Start:
				if m.cfg.PushMethod == Symlink {
					go m.mount(ev.Container)
				}
			case core.EggConnected:
				go m.job(ctx, c, ev.Container, false)
			case core.Die:
				m.forget(ev.Container.Name)
			}
		}
	}
}

func (m *Module) mount(cont core.Container) {
	if err := core.BindReadOnly(cont.PID, m.cfg.CS2Dir, MountDst); err != nil {
		m.Log.Warn("bind mount failed", "container", cont.Name, "err", err)
	}
}

// updateLoop runs the central CS2 update on a timer and pushes + restarts
// every server when the build changed.
func (m *Module) updateLoop(ctx context.Context, c *core.Core) {
	first := time.After(10 * time.Second)
	ticker := time.NewTicker(time.Duration(m.cfg.CheckMinutes) * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first:
		case <-ticker.C:
		}
		changed, err := m.centralUpdate(ctx)
		if err != nil {
			m.Log.Warn("central update failed", "err", err)
			continue
		}
		if changed.To == "" {
			continue
		}
		c.Notify(core.NoticeCS2Update, "", map[string]any{"from": changed.From, "to": changed.To})
		var wg sync.WaitGroup
		for _, cont := range c.Containers() {
			wg.Add(1)
			go func(cont core.Container) {
				defer wg.Done()
				if m.job(ctx, c, cont, true) && m.cfg.AutoRestart {
					m.owe(ctx, c, cont.Name)
				}
			}(cont)
		}
		wg.Wait()
	}
}

type buildChange struct{ From, To string } // To empty = no change

// centralUpdate runs steamcmd under the write lock and reports a build
// change.
func (m *Module) centralUpdate(ctx context.Context) (buildChange, error) {
	m.central.Lock()
	defer m.central.Unlock()
	if err := ensureSteamcmd(ctx, m.cfg.SteamcmdDir, m.HTTP, os.Stderr); err != nil {
		return buildChange{}, err
	}
	before := BuildID(m.cfg.CS2Dir)
	after, err := appUpdate(ctx, m.cfg.SteamcmdDir, m.cfg.CS2Dir, m.cfg.Validate, discard{})
	if err != nil {
		return buildChange{}, err
	}
	if after != before {
		m.Log.Info("CS2 updated", "from", before, "to", after)
		return buildChange{From: before, To: after}, nil
	}
	return buildChange{}, nil
}

// status is the vpksync section of the control snapshot.
type status struct {
	Build   string            `json:"build"`
	Last    map[string]string `json:"last"`
	Pending map[string]string `json:"pending"` // container -> since, restarts the policy holds back
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func (m *Module) restart(ctx context.Context, c *core.Core, uuid string) {
	c.ExpectStop(uuid, core.StopRestart) // so the stop notice says why, not "unknown"
	w, err := LoadWings(m.cfg.WingsConfig)
	if err != nil {
		m.Log.Warn("cannot restart servers", "err", err)
		return
	}
	if err := w.Restart(ctx, uuid); err != nil {
		m.Log.Warn("restart failed", "container", uuid, "err", err)
		return
	}
	m.Log.Info("restarted via Wings", "container", uuid)
}

// job verifies (and pushes when needed) one volume, reporting status to
// the egg. force skips the verify shortcut. Returns true on success.
func (m *Module) job(ctx context.Context, c *core.Core, cont core.Container, force bool) bool {
	if cont.Volume == "" {
		return false
	}
	send := func(state string, pos int) {
		m.mu.Lock()
		m.last[cont.Name] = state
		m.mu.Unlock()
		c.Send(cont.Name, &proto.VpkStatus{State: state, QueuePos: pos})
	}
	m.enqueue(c, cont.Name)
	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		m.dequeue(c, cont.Name)
		return false
	}
	defer func() { <-m.sem }()
	m.dequeue(c, cont.Name)

	vol := m.volumeLock(cont.Name)
	vol.Lock()
	defer vol.Unlock()

	if !m.central.TryRLock() {
		send(proto.StateUpdating, 0)
		m.central.RLock()
	}
	defer m.central.RUnlock()

	if BuildID(m.cfg.CS2Dir) == "" {
		m.Log.Warn("no central CS2 install yet", "container", cont.Name)
		send(proto.StateFailed, 0)
		return false
	}
	send(proto.StateVerifying, 0)
	if !force && Verify(m.cfg.CS2Dir, cont.Volume, m.cfg.PushMethod) {
		PruneStaleLinks(m.cfg.CS2Dir, cont.Volume)
		send(proto.StateDone, 0)
		return true
	}
	send(proto.StatePushing, 0)
	rep, err := Push(m.cfg.CS2Dir, cont.Volume, m.cfg.PushMethod, cont.Owner)
	if err != nil {
		m.Log.Warn("push failed", "container", cont.Name, "err", err)
		send(proto.StateFailed, 0)
		return false
	}
	m.Log.Info("pushed", "container", cont.Name, "copied", rep.Copied, "vpks", rep.Linked, "pruned", rep.Pruned)
	send(proto.StateDone, 0)
	return true
}

func (m *Module) volumeLock(name string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.volumes[name]
	if !ok {
		l = &sync.Mutex{}
		m.volumes[name] = l
	}
	return l
}

// enqueue registers a waiter and tells every waiter its position, so an
// egg sees a live queue position instead of silence on a saturated pool.
func (m *Module) enqueue(c *core.Core, name string) {
	m.mu.Lock()
	m.queue = append(m.queue, name)
	m.mu.Unlock()
	m.announce(c)
}

func (m *Module) dequeue(c *core.Core, name string) {
	m.mu.Lock()
	for i, n := range m.queue {
		if n == name {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	m.announce(c)
}

func (m *Module) announce(c *core.Core) {
	m.mu.Lock()
	q := append([]string(nil), m.queue...)
	for _, n := range q {
		m.last[n] = proto.StateQueued
	}
	m.mu.Unlock()
	for i, n := range q {
		c.Send(n, &proto.VpkStatus{State: proto.StateQueued, QueuePos: i + 1})
	}
}

// Doctor checks the central install and the servers.
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "VPK sync"
	cfg := Defaults()
	var out []doctor.Check
	if err := c.Config(m.Name(), &cfg); err != nil {
		return append(out, doctor.Failf(s, "config section unreadable: "+err.Error()))
	}
	if err := cfg.Check(); err != nil {
		return append(out, doctor.Failf(s, "config: "+err.Error()))
	}
	if _, err := os.Stat(cfg.SteamcmdDir + "/steamcmd.sh"); err != nil {
		out = append(out, doctor.Warnf(s, "steamcmd not installed yet at "+cfg.SteamcmdDir+" (the daemon installs it on its first check)"))
	} else {
		out = append(out, doctor.Ok(s, "steamcmd at "+cfg.SteamcmdDir))
	}
	switch id, n := BuildID(cfg.CS2Dir), CountVPKs(cfg.CS2Dir); {
	case id == "":
		out = append(out, doctor.Failf(s, "no CS2 build in "+cfg.CS2Dir+" (first update pending or failed, see: journalctl -u cs2node)"))
	case n == 0:
		out = append(out, doctor.Failf(s, "build "+id+" in "+cfg.CS2Dir+" but no VPK files"))
	default:
		out = append(out, doctor.Ok(s, fmt.Sprintf("central install build %s, %d VPK files", id, n)))
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(cfg.CS2Dir, &st); err == nil {
		free := uint64(st.Bavail) * uint64(st.Bsize)
		if free < 10<<30 {
			out = append(out, doctor.Warnf(s, fmt.Sprintf("only %.1f GiB free under %s", float64(free)/float64(1<<30), cfg.CS2Dir)))
		} else {
			out = append(out, doctor.Ok(s, fmt.Sprintf("%.0f GiB free under %s", float64(free)/float64(1<<30), cfg.CS2Dir)))
		}
	}
	if cfg.AutoRestart {
		if _, err := LoadWings(cfg.WingsConfig); err != nil {
			out = append(out, doctor.Warnf(s, "auto restart on, but: "+err.Error()))
		} else {
			out = append(out, doctor.Ok(s, "Wings API reachable from the config"))
		}
	}
	var remote status
	c.ModuleStatus(m.Name(), &remote)
	conts := c.Containers()
	if len(conts) == 0 {
		out = append(out, doctor.Warnf(s, "no running CS2 container matches the configured images"))
	}
	for _, cont := range conts {
		switch {
		case cont.Volume == "":
			out = append(out, doctor.Warnf(s, cont.Name+": no /home/container volume"))
		case !c.Connected(cont.Name):
			out = append(out, doctor.Warnf(s, cont.Name+": egg not connected (old image, or the server is still booting)"))
		default:
			last := remote.Last[cont.Name]
			if last == "" {
				last = "no push yet"
			}
			out = append(out, doctor.Ok(s, cont.Name+": egg connected, last status "+last))
		}
		if cfg.PushMethod == Symlink && cont.PID > 0 && !core.Mounted(cont.PID, MountDst) {
			out = append(out, doctor.Warnf(s, cont.Name+": shared dir not mounted at "+MountDst+" (kernel < 5.2, or the daemon was not running when the server started)"))
		}
		if _, err := os.Stat(cont.Volume + "/steamapps"); err == nil {
			out = append(out, doctor.Warnf(s, cont.Name+": steamapps/ in the volume, this server fell back to SteamCMD at some point"))
		}
	}
	return out
}
