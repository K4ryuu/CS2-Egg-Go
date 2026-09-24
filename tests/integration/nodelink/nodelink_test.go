// SPDX-License-Identifier: GPL-3.0-or-later

// Package nodelink_test runs the real egg boot against a fake node on the
// volume socket, so what the node receives is what a live server sends.
package nodelink_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/boot"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// fakeNode listens on the volume socket and collects everything the egg
// sends after answering the hello.
type fakeNode struct {
	mu    sync.Mutex
	msgs  []proto.Message
	conns []net.Conn
	ln    net.Listener
}

// stop drops the listener and every open connection, the way a daemon
// restart does; the egg must notice and reconnect.
func (n *fakeNode) stop() {
	n.ln.Close()
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.conns {
		c.Close()
	}
	n.conns = nil
	n.msgs = nil
}

func listen(t *testing.T, root string, modules []string) *fakeNode {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "egg"), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(root, "egg", "cs2node.sock"))
	if err != nil {
		t.Fatal(err)
	}
	n := &fakeNode{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n.mu.Lock()
			n.conns = append(n.conns, conn)
			n.mu.Unlock()
			go func() {
				defer conn.Close()
				r := proto.NewReader(conn)
				if _, err := r.Next(); err != nil { // the egg's hello
					return
				}
				if err := proto.Encode(conn, &proto.Hello{Version: "test", Modules: modules}); err != nil {
					return
				}
				for {
					m, err := r.Next()
					if err != nil {
						return
					}
					n.mu.Lock()
					n.msgs = append(n.msgs, m)
					n.mu.Unlock()
				}
			}()
		}
	}()
	return n
}

// await waits for a message the predicate accepts.
func (n *fakeNode) await(t *testing.T, what string, ok func(proto.Message) bool) proto.Message {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		n.mu.Lock()
		for _, m := range n.msgs {
			if ok(m) {
				n.mu.Unlock()
				return m
			}
		}
		n.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	t.Fatalf("no %s; the node saw %d message(s): %+v", what, len(n.msgs), n.msgs)
	return nil
}

// runEgg boots the egg with startup as the server command and stops it when
// the test ends.
func runEgg(t *testing.T, root, startup string) {
	t.Helper()
	env := map[string]string{
		"STARTUP":           startup,
		"SRCDS_STOP_UPDATE": "1", // no steamcmd in a test
		"CONSOLE_LOG_LEVEL": "debug",
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { // drain the console
		buf := make([]byte, 4096)
		for {
			if _, err := outR.Read(buf); err != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		boot.Run(ctx, boot.Env{Root: root, Getenv: func(k string) string { return env[k] }, Stdin: inR, Stdout: outW})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			t.Error("the egg did not stop")
		}
		inW.Close()
		outW.Close()
	})
}

func volume(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cs2egg") // the socket path is capped at ~104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// The node must learn the map and the player count from the console, and
// get a first state right after the hello even on a quiet server.
func TestEggReportsStateToTheNode(t *testing.T) {
	root := volume(t)
	node := listen(t, root, []string{"guard"})
	runEgg(t, root, `sh -c '
		sleep 1
		echo "Host activate: Loading (de_dust2)"
		echo "Client 10 '"'"'K4ryuu'"'"' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL"
		echo "Client 11 '"'"'Bot'"'"' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL"
		read stop'`)

	first := node.await(t, "a state right after the hello", func(m proto.Message) bool {
		_, ok := m.(*proto.ServerState)
		return ok
	}).(*proto.ServerState)
	if !first.Up || first.Map != "" || first.Players != 0 {
		t.Fatalf("first state should be an empty running server: %+v", first)
	}
	full := node.await(t, "the map and both players", func(m proto.Message) bool {
		st, ok := m.(*proto.ServerState)
		return ok && st.Map == "de_dust2" && st.Players == 2
	}).(*proto.ServerState)
	if !full.Up {
		t.Fatalf("state: %+v", full)
	}
}

// A node restart drops the connection: the egg must reconnect and restate
// on its own, or the node's picture stays empty on a quiet server.
func TestEggRestatesAfterTheNodeRestarts(t *testing.T) {
	root := volume(t)
	node := listen(t, root, []string{"guard"})
	runEgg(t, root, `sh -c 'sleep 1; echo "Host activate: Loading (de_mirage)"; read stop'`)
	node.await(t, "the first map report", func(m proto.Message) bool {
		st, ok := m.(*proto.ServerState)
		return ok && st.Map == "de_mirage"
	})

	// the node goes away and comes back, as a daemon restart does
	node.stop()
	time.Sleep(500 * time.Millisecond)
	again := listen(t, root, []string{"guard"})
	again.await(t, "a restated map after the reconnect", func(m proto.Message) bool {
		st, ok := m.(*proto.ServerState)
		return ok && st.Map == "de_mirage"
	})
}

// A crash carries the console tail; a stop from the panel does not.
func TestCrashCarriesTheConsoleTail(t *testing.T) {
	root := volume(t)
	node := listen(t, root, nil)
	runEgg(t, root, `sh -c 'echo "Host activate: Loading (de_nuke)"; echo "plugin blew up"; echo "./game/cs2.sh: line 97: 1234 Aborted                 (core dumped) ${GAME_DEBUGGER} \"$GAMEROOT/game/bin/linuxsteamrt64/cs2\" \"$@\""; read stop'`)
	cr := node.await(t, "a crash report", func(m proto.Message) bool {
		_, ok := m.(*proto.Crash)
		return ok
	}).(*proto.Crash)
	if cr.Map != "de_nuke" || !strings.Contains(strings.Join(cr.Lines, "\n"), "plugin blew up") {
		t.Fatalf("crash: %+v", cr)
	}
}
