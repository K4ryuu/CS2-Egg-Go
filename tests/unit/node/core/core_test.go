// SPDX-License-Identifier: GPL-3.0-or-later

package core_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/nodeclient"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// fakeDocker feeds scripted containers and events.
type fakeDocker struct {
	running []core.Container
	events  chan core.Event
}

func (f *fakeDocker) Running(context.Context, []string) ([]core.Container, error) {
	return f.running, nil
}
func (f *fakeDocker) Inspect(_ context.Context, id string) (core.Container, error) {
	for _, c := range f.running {
		if c.ID == id {
			return c, nil
		}
	}
	return core.Container{}, os.ErrNotExist
}
func (f *fakeDocker) Events(ctx context.Context, _ []string) (<-chan core.Event, <-chan error) {
	return f.events, make(chan error)
}

// unix socket paths are capped at ~104 bytes; t.TempDir() is longer on macOS
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cs2v")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func waitFor(t *testing.T, ch <-chan core.Event, kind core.EventKind) core.Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %v event", kind)
		}
	}
}

func TestSocketLifecycleAndMessageRouting(t *testing.T) {
	vol := shortTempDir(t)
	abc := core.Container{ID: "1", Name: "abc", Image: "sples1/k4ryuu-cs2:dev", Volume: vol, Ports: []uint16{27015}}
	fd := &fakeDocker{running: []core.Container{abc}, events: make(chan core.Event)}
	cfg := nodeconfig.Default()
	c := core.New(fd, cfg, []string{"vpksync", "guard"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	events, unsub := c.Subscribe()
	defer unsub()

	// the handler runs on the socket's own goroutine, so the slice needs a
	// lock: the test polls it from here
	var gotMu sync.Mutex
	var got []proto.Message
	hits := func() []proto.Message {
		gotMu.Lock()
		defer gotMu.Unlock()
		return append([]proto.Message(nil), got...)
	}
	c.Handle(proto.TypeGuardHit, func(container string, m proto.Message) {
		if container == "abc" {
			gotMu.Lock()
			got = append(got, m)
			gotMu.Unlock()
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitFor(t, events, core.Start)
	sock := core.SocketPath(vol)
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not created: %v", err)
	}
	if err := c.Send("abc", &proto.VpkStatus{State: proto.StateQueued}); err != core.ErrNotConnected {
		t.Fatalf("send before connect must fail with ErrNotConnected, got %v", err)
	}

	cl, err := nodeclient.Connect(ctx, filepath.Join(vol, "egg", nodeclient.SocketName), 2*time.Second, proto.Hello{BootID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if !cl.HasModule("vpksync") || !cl.HasModule("guard") {
		t.Fatalf("node hello modules: %v", cl.Node.Modules)
	}
	waitFor(t, events, core.EggConnected)
	if !c.Connected("abc") {
		t.Fatal("core must report the egg as connected")
	}

	// node -> egg
	go func() {
		c.Send("abc", &proto.VpkStatus{State: proto.StateQueued, QueuePos: 2})
		c.Send("abc", &proto.VpkStatus{State: proto.StateDone})
	}()
	var states []string
	v, err := cl.AwaitVerdict(ctx, func(state string, pos int) { states = append(states, state) })
	if err != nil || !v.Managed || len(states) != 1 || states[0] != "queued" {
		t.Fatalf("verdict %+v err %v states %v", v, err, states)
	}

	// egg -> node
	cl.Send(&proto.GuardHit{IP: "1.2.3.4", Minutes: 30, Rule: "stray_no_connection"})
	deadline := time.Now().Add(2 * time.Second)
	for len(hits()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := hits(); len(got) != 1 || got[0].(*proto.GuardHit).IP != "1.2.3.4" {
		t.Fatalf("handler not called: %v", got)
	}

	// container dies: socket removed, Die event
	fd.running = nil
	fd.events <- core.Event{Kind: core.Die, Container: abc}
	waitFor(t, events, core.Die)
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("socket file must be removed on die")
	}
	if len(c.Containers()) != 0 {
		t.Fatal("registry must be empty")
	}
}

func TestNoNodeWithinTheWindow(t *testing.T) {
	start := time.Now()
	_, err := nodeclient.Connect(context.Background(), filepath.Join(t.TempDir(), "nope.sock"), 700*time.Millisecond, proto.Hello{})
	if err != nodeclient.ErrNoNode {
		t.Fatalf("want ErrNoNode, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("must give up around the timeout")
	}
}

func TestVerdictWithoutVpksyncIsStandalone(t *testing.T) {
	vol := shortTempDir(t)
	abc := core.Container{ID: "1", Name: "abc", Image: "x", Volume: vol}
	fd := &fakeDocker{running: []core.Container{abc}, events: make(chan core.Event)}
	c := core.New(fd, nodeconfig.Default(), []string{"guard"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	cl, err := nodeclient.Connect(ctx, core.SocketPath(vol), 2*time.Second, proto.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	v, err := cl.AwaitVerdict(ctx, nil)
	if err != nil || v.Managed || v.Failed {
		t.Fatalf("guard-only node must leave the egg standalone: %+v %v", v, err)
	}
}

// A node restart leaves every egg reconnecting. The core must forget the
// old picture on the new hello and take the fresh one, so map and player
// counts never stay stale or empty.
func TestServerStateIsPerConnection(t *testing.T) {
	vol := shortTempDir(t)
	cont := core.Container{ID: "1", Name: "abc", Image: "sples1/k4ryuu-cs2:dev", Volume: vol}
	fd := &fakeDocker{running: []core.Container{cont}, events: make(chan core.Event)}
	c := core.New(fd, nodeconfig.Default(), []string{"metrics"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	events, unsub := c.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, events, core.Start)

	if _, reported := c.State("abc"); reported {
		t.Fatal("nothing reported yet")
	}
	sock := filepath.Join(vol, "egg", nodeclient.SocketName)
	cl, err := nodeclient.Connect(ctx, sock, 2*time.Second, proto.Hello{BootID: "b1", Built: "2026-09-11T09:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, events, core.EggConnected)
	cl.Send(&proto.ServerState{Map: "de_dust2", Players: 7, Up: true})
	waitState(t, c, func(st proto.ServerState, reported bool) bool {
		return reported && st.Players == 7 && st.Map == "de_dust2"
	})

	// the snapshot the CLI reads must carry the same facts, build stamps
	// included: status and top render from this, never from the core
	snap := c.Status("2.0.0", "2026-09-11T11:00Z")
	if len(snap.Containers) != 1 {
		t.Fatalf("snapshot: %+v", snap)
	}
	cs := snap.Containers[0]
	if snap.Built != "2026-09-11T11:00Z" || !cs.Connected || !cs.Reported || cs.Map != "de_dust2" || cs.Players != 7 || !cs.Up {
		t.Fatalf("snapshot must carry the reported state: %+v", cs)
	}
	if cs.EggBuilt != "2026-09-11T09:00Z" {
		t.Fatalf("snapshot must carry the egg build stamp, got %q", cs.EggBuilt)
	}

	// the egg reconnects (node restart, or its own): the old numbers go
	cl.Close()
	waitFor(t, events, core.EggGone)
	cl2, err := nodeclient.Connect(ctx, sock, 2*time.Second, proto.Hello{BootID: "b2"})
	if err != nil {
		t.Fatal(err)
	}
	defer cl2.Close()
	waitFor(t, events, core.EggConnected)
	waitState(t, c, func(_ proto.ServerState, reported bool) bool { return !reported })
	cl2.Send(&proto.ServerState{Map: "de_mirage", Players: 2, Up: true})
	waitState(t, c, func(st proto.ServerState, reported bool) bool {
		return reported && st.Map == "de_mirage" && st.Players == 2
	})

	// a container may lie: the node clamps what it stores
	cl2.Send(&proto.ServerState{Map: strings.Repeat("x", 500), Players: 9999, Up: true})
	waitState(t, c, func(st proto.ServerState, _ bool) bool { return len(st.Map) == 64 && st.Players == 128 })
}

func waitState(t *testing.T, c *core.Core, ok func(proto.ServerState, bool) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st, reported := c.State("abc"); ok(st, reported) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, reported := c.State("abc")
	t.Fatalf("state never matched: %+v reported=%v", st, reported)
}

// A container started from a tag that was repulled since shows docker's
// image id instead of the name. It has to be inspected, not skipped, or the
// guard stops covering its ports until someone restarts it.
func TestImageMatching(t *testing.T) {
	images := []string{"sples1/cs2-egg-go", "ghcr.io/k4ryuu/cs2-egg-go", "sples1/k4ryuu-cs2:dev"}
	for _, tc := range []struct {
		image string
		match bool
		why   string
	}{
		{"ghcr.io/k4ryuu/cs2-egg-go:latest", true, "our image, any tag"},
		{"docker.io/sples1/cs2-egg-go:beta", true, "the other registry"},
		{"sples1/k4ryuu-cs2:dev", true, "the old repo's dev tag, during the migration"},
		{"sples1/k4ryuu-cs2:latest", false, "the bash egg is not ours to manage"},
		{"itzdabbzz/cs2:latest", false, "somebody else's egg entirely"},
	} {
		if got := core.ImageMatches(tc.image, images); got != tc.match {
			t.Errorf("%s: %q matched=%v", tc.why, tc.image, got)
		}
	}
	for _, id := range []string{
		"sha256:9f2c0a1b3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8",
		"9f2c0a1b3d4e",
	} {
		if !core.LooksLikeID(id) {
			t.Errorf("%q is an image id and must be inspected, not skipped", id)
		}
	}
	for _, name := range []string{"ghcr.io/k4ryuu/cs2-egg-go:latest", "cs2", "abc", ""} {
		if core.LooksLikeID(name) {
			t.Errorf("%q is a name, not an id", name)
		}
	}
}

// The tables print short ids, so the commands have to take them back.
func TestPickNameAcceptsAPrefix(t *testing.T) {
	names := []string{"59e9667c-9616-4ba9-be7e-3deca6c8806c", "7c1d0a2e-3f44-4b7a-9d21-0e5c8f6a1b2c", "7c1d0a2e-0000-0000-0000-000000000000"}
	full, err := core.PickName(names, "59e9667c")
	if err != nil || full != names[0] {
		t.Fatalf("a unique prefix must resolve: %q %v", full, err)
	}
	if full, err := core.PickName(names, names[1]); err != nil || full != names[1] {
		t.Fatalf("the full id must still work: %q %v", full, err)
	}
	if _, err := core.PickName(names, "7c1d0a2e"); err == nil {
		t.Fatal("a prefix that fits two servers must be refused, not guessed")
	}
	if _, err := core.PickName(names, "nope"); err == nil {
		t.Fatal("an unknown id must be an error")
	}
	if got, err := core.PickName(names, ""); err != nil || got != "" {
		t.Fatalf("no filter stays no filter: %q %v", got, err)
	}
}

// A server name is whatever the container says it is, and root reads it in
// cs2node status and cs2node top. An escape sequence there can restyle the
// terminal or paint a convincing fake of another server's row.
func TestReportedStateCannotReachTheTerminalRaw(t *testing.T) {
	vol := shortTempDir(t)
	cont := core.Container{ID: "1", Name: "abc", Image: "sples1/k4ryuu-cs2:dev", Volume: vol}
	fd := &fakeDocker{running: []core.Container{cont}, events: make(chan core.Event)}
	c := core.New(fd, nodeconfig.Default(), []string{"metrics"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	events, unsub := c.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, events, core.Start)

	cl, err := nodeclient.Connect(ctx, core.SocketPath(vol), 2*time.Second, proto.Hello{BootID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	evil := "\x1b[2J\x1b[1;31mdeadbeef  ALL GOOD\x07" + strings.Repeat("x", 200)
	cl.Send(&proto.ServerState{Name: evil, Map: evil, Players: 9999, Up: true})

	deadline := time.Now().Add(2 * time.Second)
	var st proto.ServerState
	for time.Now().Before(deadline) {
		if s, reported := c.State("abc"); reported {
			st = s
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, field := range []string{st.Name, st.Map} {
		if strings.ContainsAny(field, "\x1b\x07\r\n") {
			t.Fatalf("control bytes survived into the snapshot: %q", field)
		}
		if n := len([]rune(field)); n == 0 || n > 64 {
			t.Fatalf("field not bounded: %d runes", n)
		}
	}
	if st.Players > 128 {
		t.Fatalf("player count not clamped: %d", st.Players)
	}
}

// "Server down" with nothing else said is not worth a message. The node
// knows more than it used to admit: what it asked for, what the egg
// reported on its way out, and what the exit code implies.
func TestStopReasonNamesWhatHappened(t *testing.T) {
	cases := []struct {
		name string
		exit int
		want string
	}{
		{"clean exit", 0, core.StopStopped},
		{"sigterm from wings", 143, core.StopStopped},
		{"sigkill", 137, core.StopKilled},
		{"segfault", 139, core.StopCrash},
		{"abort", 134, core.StopCrash},
		{"docker said nothing", -1, core.StopUnknown},
	}
	for _, tc := range cases {
		if got := core.StopReasonFor(tc.exit); got != tc.want {
			t.Errorf("%s (exit %d): got %q want %q", tc.name, tc.exit, got, tc.want)
		}
	}
}

// The name only helps if it reaches the listener. Every notice about a
// server carries it, including the stop notice, which fires after the
// container is already out of the registry.
func TestNoticesCarryTheServerName(t *testing.T) {
	vol := shortTempDir(t)
	cont := core.Container{ID: "1", Name: "59e9667c-9616-4ba9-be7e-3deca6c8806c", Image: "sples1/k4ryuu-cs2:dev", Volume: vol}
	fd := &fakeDocker{running: []core.Container{cont}, events: make(chan core.Event)}
	c := core.New(fd, nodeconfig.Default(), []string{"alerts"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var mu sync.Mutex
	seen := map[string]string{}
	c.OnNotify(func(n core.Notice) {
		mu.Lock()
		seen[n.Kind] = n.Server
		mu.Unlock()
	})
	events, unsub := c.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, events, core.Start)

	cl, err := nodeclient.Connect(ctx, core.SocketPath(vol), 2*time.Second, proto.Hello{BootID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	cl.Send(&proto.ServerState{Name: "Retake #1", Map: "de_dust2", Players: 3, Up: true})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := c.State(cont.Name); ok && st.Name == "Retake #1" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.Notify(core.NoticeCleanupDone, cont.Name, map[string]any{"files": 1, "bytes": int64(2)})
	cl.Close()

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := seen[core.NoticeCleanupDone] != ""
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	if got := seen[core.NoticeCleanupDone]; got != "Retake #1" {
		mu.Unlock()
		t.Fatalf("a module notice lost the server name: %q", got)
	}
	mu.Unlock()

	// a container is up a second or two before its egg connects and can
	// say what the server is called, so "server up" has to fall back on
	// the name from the last run. Restart it for real: the entry is thrown
	// away and rebuilt with no state at all.
	fd.events <- core.Event{Kind: core.Die, Container: cont, ExitCode: 0}
	waitFor(t, events, core.Die)
	fd.events <- core.Event{Kind: core.Start, Container: cont}
	waitFor(t, events, core.Start)
	if st, ok := c.State(cont.Name); ok && st.Name != "" {
		t.Fatalf("the new entry should have no state of its own yet: %+v", st)
	}
	c.Notify(core.NoticeServerStart, cont.Name, map[string]any{"ports": cont.Ports})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := seen[core.NoticeServerStart] != ""
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := seen[core.NoticeServerStart]; got != "Retake #1" {
		t.Fatalf("a start notice before the egg reports must use the remembered name, got %q", got)
	}
}
