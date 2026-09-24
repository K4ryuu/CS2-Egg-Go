// SPDX-License-Identifier: GPL-3.0-or-later

// Package core is what every node module builds on: the running CS2
// containers, their lifecycle events, and a socket into each one's egg.
// Modules never see docker or sockets, only this.
package core

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// ErrNotConnected: the container's egg has no open socket connection.
var ErrNotConnected = errors.New("egg not connected")

// Handler receives one message from a container's egg.
type Handler func(container string, msg proto.Message)

// Core tracks containers and talks to their eggs.
type Core struct {
	docker  Docker
	cfg     nodeconfig.Config
	log     *slog.Logger
	modules []string // advertised in the node's hello

	// Passive cores (status, doctor) only watch docker; they never open
	// sockets in the volumes, those belong to the running daemon.
	Passive bool
	// ConfigPath is where cfg came from, for ConfigNow.
	ConfigPath string

	mu         sync.Mutex
	containers map[string]*entry
	// names is the last name each server reported, kept across restarts.
	// A container is up for a second or two before its egg connects and
	// asks the engine what the server is called, so "server up" would
	// otherwise never carry a name: the one from the last run is the same
	// server, unless somebody renamed it in between.
	names     map[string]string
	subs      map[int]chan Event
	nextSub   int
	handlers  map[proto.Type][]Handler
	providers map[string]func() any
	remote    *Status
	notifiers []chan Notice
}

type entry struct {
	c        Container
	sock     *sockServer
	egg      proto.Hello // the egg's hello (version, build stamp)
	state    proto.ServerState
	reported bool // the egg sent a server.state at all (old images never do)
	// why the container is expected to go away, and until when the guess
	// holds: a restart the node asked for, or a crash the egg reported on
	// its way out
	expect      string
	expectUntil time.Time
	crashedAt   time.Time
	crashCode   int
}

// setEgg remembers the egg's hello for status and doctor.
func (c *Core) setEgg(container string, h proto.Hello) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.containers[container]; ok {
		e.egg = h
		e.reported = false // a fresh connection: wait for its own state
	}
}

// Notice is a fact one module publishes for others (alerts, crashes,
// metrics) without knowing who listens.
type Notice struct {
	Kind      string
	Container string // empty for node-wide facts
	Server    string // the name players see, when the egg has reported one
	Fields    map[string]any
	Time      time.Time
}

// Notice kinds. Every publisher uses one of these, so a listener (and the
// alerts wizard) can enumerate them.
const (
	NoticeServerStart     = "server.start"     // container up
	NoticeServerStop      = "server.stop"      // container gone
	NoticeCrash           = "crash"            // exit_code, map, players
	NoticeCS2Update       = "cs2.update"       // from, to
	NoticePushFailed      = "push.failed"      // err
	NoticeRestartDeferred = "restart.deferred" // until, reason
	NoticeGuardBlock      = "guard.block"      // ip, minutes, rule, why
	NoticeNodeUpdate      = "node.update"      // from, to
	NoticeBackupDone      = "backup.done"      // path, bytes
	NoticeBackupFailed    = "backup.failed"    // err
	NoticeCleanupDone     = "cleanup.done"     // files, bytes
)

// NoticeKinds lists every kind with its one-line meaning, in display order.
var NoticeKinds = []struct{ Kind, Desc string }{
	{NoticeCrash, "a server crashed (exit code, map, players)"},
	{NoticeCS2Update, "the central CS2 install moved to a new build"},
	{NoticePushFailed, "a VPK push into a server volume failed"},
	{NoticeRestartDeferred, "a post-update restart waits for the window or an empty server"},
	{NoticeGuardBlock, "the host guard dropped an address"},
	{NoticeNodeUpdate, "cs2node updated itself"},
	{NoticeBackupDone, "a backup finished"},
	{NoticeBackupFailed, "a backup failed"},
	{NoticeCleanupDone, "a cleanup pass removed files from a volume"},
	{NoticeServerStart, "a server container came up"},
	{NoticeServerStop, "a server container went away"},
}

// Notify publishes a notice to every OnNotify listener. Each listener gets
// notices in order on its own goroutine; a listener 256 behind drops them
// rather than blocking the publisher.
func (c *Core) Notify(kind, container string, fields map[string]any) {
	n := Notice{Kind: kind, Container: container, Fields: fields, Time: time.Now()}
	c.mu.Lock()
	if e, ok := c.containers[container]; ok {
		n.Server = e.state.Name // so a listener can say which server, not which uuid
	}
	if n.Server == "" {
		n.Server = c.names[container] // what it was called last time it ran
	}
	c.mu.Unlock()
	c.notify(n)
}

// notify fans one ready-made notice out to the listeners.
func (c *Core) notify(n Notice) {
	c.mu.Lock()
	chans := slices.Clone(c.notifiers)
	c.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- n:
		default:
			c.log.Warn("notice listener too slow, notice dropped", "kind", n.Kind, "container", n.Container)
		}
	}
}

// OnNotify registers a listener for every notice.
func (c *Core) OnNotify(fn func(Notice)) {
	ch := make(chan Notice, 256)
	c.mu.Lock()
	c.notifiers = append(c.notifiers, ch)
	c.mu.Unlock()
	go func() {
		for n := range ch {
			fn(n)
		}
	}()
}

// State is the last server.state the container's egg reported; reported
// is false until one arrived (an old image never sends any).
func (c *Core) State(container string) (st proto.ServerState, reported bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remote != nil {
		for _, cs := range c.remote.Containers {
			if cs.Name == container {
				return proto.ServerState{Map: cs.Map, Players: cs.Players, Up: cs.Up}, cs.Reported
			}
		}
		return proto.ServerState{}, false
	}
	e, ok := c.containers[container]
	if !ok {
		return proto.ServerState{}, false
	}
	return e.state, e.reported
}

// Exec types one command into the container's server console.
func (c *Core) Exec(container, cmd string) error {
	return c.Send(container, &proto.ConsoleExec{Cmd: cmd})
}

// New builds a core over docker with the node config.
func New(docker Docker, cfg nodeconfig.Config, modules []string, log *slog.Logger) *Core {
	return &Core{
		docker: docker, cfg: cfg, log: log, modules: modules,
		containers: map[string]*entry{}, subs: map[int]chan Event{}, handlers: map[proto.Type][]Handler{},
	}
}

// Disable stops advertising a module to eggs (it failed to start).
func (c *Core) Disable(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.modules = slices.DeleteFunc(slices.Clone(c.modules), func(m string) bool { return m == name })
}

// Config decodes a module's section of the node config into v.
func (c *Core) Config(section string, v any) error {
	_, err := c.cfg.Section(section, v)
	return err
}

// ConfigNow re-reads the config file before decoding the section, so a
// setting a CLI command changed takes effect without a daemon restart.
// Falls back to the loaded config when the path is unknown (tests).
func (c *Core) ConfigNow(section string, v any) error {
	if c.ConfigPath == "" {
		return c.Config(section, v)
	}
	cfg, err := nodeconfig.Load(c.ConfigPath)
	if err != nil {
		return err
	}
	_, err = cfg.Section(section, v)
	return err
}

// Containers is a snapshot of the running matching containers.
func (c *Core) Containers() []Container {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Container, 0, len(c.containers))
	for _, e := range c.containers {
		out = append(out, e.c)
	}
	return out
}

// Subscribe returns a channel of lifecycle events and a cancel function.
// Slow subscribers drop events: the channel is buffered, never blocking.
func (c *Core) Subscribe() (<-chan Event, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextSub
	c.nextSub++
	ch := make(chan Event, 64)
	c.subs[id] = ch
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(ch)
		}
	}
}

// Handle registers fn for messages of type t from any egg.
func (c *Core) Handle(t proto.Type, fn Handler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[t] = append(c.handlers[t], fn)
}

// Send delivers msg to the container's egg.
func (c *Core) Send(container string, msg proto.Message) error {
	c.mu.Lock()
	e, ok := c.containers[container]
	c.mu.Unlock()
	if !ok || e.sock == nil {
		return ErrNotConnected
	}
	return e.sock.send(msg)
}

// Connected reports whether the container's egg is on the socket. A
// passive core answers from the daemon's snapshot.
func (c *Core) Connected(container string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remote != nil {
		for _, cs := range c.remote.Containers {
			if cs.Name == container {
				return cs.Connected
			}
		}
		return false
	}
	e, ok := c.containers[container]
	return ok && e.sock != nil && e.sock.connected()
}

func (c *Core) emit(ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.subs {
		select {
		case ch <- ev:
		default:
			c.log.Warn("subscriber too slow, event dropped", "event", ev.Kind.String(), "container", ev.Container.Name)
		}
	}
}

func (c *Core) dispatch(container string, msg proto.Message) {
	c.mu.Lock()
	if cr, ok := msg.(*proto.Crash); ok {
		if e, known := c.containers[container]; known {
			// the egg sends this on its way out, so the stop notice that
			// follows a moment later can say what happened
			e.crashedAt, e.crashCode = time.Now(), cr.ExitCode
		}
	}
	if st, ok := msg.(*proto.ServerState); ok {
		if e, known := c.containers[container]; known {
			e.reported = true
			e.state = *st // the egg's word, but the container can fake it
			e.state.Map = Clean(e.state.Map, 64)
			e.state.Name = Clean(e.state.Name, 64)
			c.remember(container, e.state.Name)
			e.state.Players = max(0, min(e.state.Players, 128))
		}
	}
	hs := append([]Handler(nil), c.handlers[msg.Type()]...)
	c.mu.Unlock()
	if cr, ok := msg.(*proto.Crash); ok {
		c.Notify(NoticeCrash, container, map[string]any{"exit_code": cr.ExitCode, "map": cr.Map, "players": cr.Players})
	}
	for _, h := range hs {
		h(container, msg)
	}
}

// Run follows docker until ctx ends: reconcile on every (re)connect, then
// the event stream; a dropped stream reconnects after 5 s.
func (c *Core) Run(ctx context.Context) error {
	defer c.closeAll()
	for {
		if err := c.reconcile(ctx); err != nil {
			c.log.Warn("docker not reachable, retrying", "err", err)
			if !sleep(ctx, 5*time.Second) {
				return ctx.Err()
			}
			continue
		}
		events, errc := c.docker.Events(ctx, c.cfg.Images)
	stream:
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case ev, ok := <-events:
				if !ok {
					break stream
				}
				c.handle(ev)
			case err := <-errc:
				c.log.Warn("docker event stream ended, reconnecting in 5s", "err", err)
				break stream
			}
		}
		if !sleep(ctx, 5*time.Second) {
			return ctx.Err()
		}
	}
}

// reconcile syncs the registry with what is running right now. Every
// running container gets a Start event, so modules must treat Start as
// idempotent.
func (c *Core) reconcile(ctx context.Context) error {
	running, err := c.docker.Running(ctx, c.cfg.Images)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, cont := range running {
		seen[cont.Name] = true
		c.handle(Event{Kind: Start, Container: cont})
	}
	c.mu.Lock()
	var gone []Container
	for name, e := range c.containers {
		if !seen[name] {
			gone = append(gone, e.c)
		}
	}
	c.mu.Unlock()
	for _, cont := range gone {
		c.handle(Event{Kind: Die, Container: cont})
	}
	return nil
}

func (c *Core) handle(ev Event) {
	switch ev.Kind {
	case Start:
		c.mu.Lock()
		e, known := c.containers[ev.Container.Name]
		if !known {
			e = &entry{}
			c.containers[ev.Container.Name] = e
		}
		e.c = ev.Container
		needSock := e.sock == nil && ev.Container.Volume != "" && !c.Passive
		c.mu.Unlock()
		// opening a volume socket is a dozen filesystem calls plus a
		// listen, and this lock answers Containers, Send and State for
		// every module: a reconcile over twenty containers used to hold it
		// across all of that, in turn
		if needSock {
			sock := c.openSocket(ev.Container)
			c.mu.Lock()
			if e.sock == nil {
				e.sock = sock
			} else if sock != nil {
				sock.close() // someone got there first
			}
			c.mu.Unlock()
		}
		if !known {
			c.log.Info("container up", "container", ev.Container.Name, "ports", ev.Container.Ports)
			c.Notify(NoticeServerStart, ev.Container.Name, map[string]any{"image": ev.Container.Image, "ports": ev.Container.Ports})
		}
		c.emit(Event{Kind: Start, Container: e.c})
	case Die:
		c.mu.Lock()
		e, known := c.containers[ev.Container.Name]
		if known {
			delete(c.containers, ev.Container.Name)
		}
		var reason, server string
		if known {
			reason = e.stopReason(ev.ExitCode, time.Now())
			server = e.state.Name // the entry is gone by the time we notify
			if server == "" {
				server = c.names[ev.Container.Name]
			}
		}
		c.mu.Unlock()
		if !known {
			return
		}
		if e.sock != nil {
			e.sock.close()
		}
		c.log.Info("container gone", "container", ev.Container.Name, "reason", reason, "exit", ev.ExitCode)
		c.notify(Notice{Kind: NoticeServerStop, Container: ev.Container.Name, Server: server, Time: time.Now(),
			Fields: map[string]any{"reason": reason, "exit_code": ev.ExitCode}})
		c.emit(Event{Kind: Die, Container: e.c})
	}
}

func (c *Core) closeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.containers {
		if e.sock != nil {
			e.sock.close()
		}
	}
	for id, ch := range c.subs {
		delete(c.subs, id)
		close(ch)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// clean makes text from a container safe to print in a root operator's
// terminal and bounds its length.
//
// Clean strips control bytes from egg-reported text and truncates by rune
// so a multi-byte character is never cut in half. A server name is whatever
// the container says it is, and it is printed by `cs2node status` and
// `cs2node top`: an escape sequence there can restyle the terminal, hide
// lines, or draw a convincing fake of another server's row.
func Clean(s string, max int) string {
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			continue
		}
		if n == max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// Stop reasons, as named in a server.stop notice.
const (
	StopCrash    = "crash"   // the egg reported one, or the exit says so
	StopRestart  = "restart" // the node asked for it
	StopStopped  = "stopped" // a clean shutdown
	StopKilled   = "killed"  // SIGKILL: out of memory, or a forced stop
	StopUnknown  = "unknown" // docker did not say and nothing else knows
	restartGuess = 5 * time.Minute
	crashGuess   = 60 * time.Second
)

// ExpectStop tells the core why a container is about to go away, so the
// stop notice can say "restart" rather than guessing from an exit code.
// The claim expires on its own: a restart that never happens must not
// relabel a crash an hour later.
func (c *Core) ExpectStop(container, why string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.containers[container]; ok {
		e.expect, e.expectUntil = why, time.Now().Add(restartGuess)
	}
}

// stopReason names why the container went away, best evidence first: what
// the node itself asked for, then what the egg reported on its way out,
// then what the exit code implies.
func (e *entry) stopReason(exit int, now time.Time) string {
	switch {
	case e.expect != "" && now.Before(e.expectUntil):
		return e.expect
	case !e.crashedAt.IsZero() && now.Sub(e.crashedAt) < crashGuess:
		return StopCrash
	case exit == 137:
		return StopKilled
	case e.reported:
		// CS2 exits on a segfault on EVERY clean stop, so the exit code
		// says nothing here. The egg can tell the two apart, because it
		// knows whether it asked the server to quit, and it sends a crash
		// report when it did not. It sent none, so this was a stop.
		return StopStopped
	}
	return StopReasonFor(exit)
}

// StopReasonFor is what the exit code alone implies, for a container whose
// egg never reported: an old image, or one that died before it connected.
func StopReasonFor(exit int) string {
	switch {
	case exit == 0, exit == 143, exit == 15:
		return StopStopped
	case exit == 137:
		return StopKilled
	case exit < 0:
		return StopUnknown
	case exit > 128:
		return StopCrash
	}
	return StopCrash
}

// maxNames bounds the remembered names. A node runs a few dozen servers;
// this is only here so a long-lived daemon on a busy panel cannot grow the
// map forever as servers are created and deleted.
const maxNames = 512

// remember keeps a server's name for the next time it starts. Call with the
// lock held.
func (c *Core) remember(container, name string) {
	if name == "" || container == "" {
		return
	}
	if c.names == nil {
		c.names = map[string]string{}
	}
	if _, known := c.names[container]; !known && len(c.names) >= maxNames {
		for k := range c.names { // drop an arbitrary one: any is as good
			delete(c.names, k)
			break
		}
	}
	c.names[container] = name
}
