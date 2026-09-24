// SPDX-License-Identifier: GPL-3.0-or-later

package supervisor_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/supervisor"
)

// The server's own pty echoes typed input once (cooked mode), then the
// program answers: that is the single echo the panel must see.
func TestInputReachesTheProcessAndEchoesOnce(t *testing.T) {
	r, w, _ := os.Pipe()
	var mu sync.Mutex
	var lines []string
	s := &supervisor.Server{
		Cmd:   exec.Command("sh", "-c", "echo ready; read x; echo got:$x"),
		Stdin: r,
		Line: func(l string) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
		},
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Run(context.Background()) }()
	// type only once the program said ready, so the echo lands after it
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		ready := len(lines) > 0 && lines[0] == "ready"
		mu.Unlock()
		if ready || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.Write([]byte("ping\n"))
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit")
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(lines, "|")
	if joined != "ready|ping|got:ping" {
		t.Fatalf("lines: %q", joined)
	}
	if s.Stopping() {
		t.Fatal("ping is not a stop request")
	}
}

func TestTypedQuitMarksTheRunAsStopping(t *testing.T) {
	r, w, _ := os.Pipe()
	s := &supervisor.Server{Cmd: exec.Command("sh", "-c", "read x; echo bye"), Stdin: r, Line: func(string) {}}
	errc := make(chan error, 1)
	go func() { errc <- s.Run(context.Background()) }()
	time.Sleep(300 * time.Millisecond)
	w.Write([]byte("QUIT\n"))
	select {
	case <-errc:
	case <-time.After(5 * time.Second):
		t.Fatal("did not exit")
	}
	if !s.Stopping() {
		t.Fatal("a typed quit must mark the run as stopping")
	}
}

func TestSendInjectsACommandAndCancelStopsTheServer(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	s := &supervisor.Server{
		Cmd:         exec.Command("sh", "-c", "while read x; do echo cmd:$x; [ \"$x\" = quit ] && exit 0; done"),
		StopCommand: "quit",
		Grace:       2 * time.Second,
		Line: func(l string) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	if err := s.Send("kickid 7"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-errc:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop the server")
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(lines, "|")
	if !strings.Contains(joined, "cmd:kickid 7") || !strings.Contains(joined, "cmd:quit") {
		t.Fatalf("lines: %q", joined)
	}
	if err := s.Send("late"); err == nil {
		t.Fatal("Send after exit must fail")
	}
}

// OnInput is what turns a typed command into a log line, so it has to see
// what the operator types and nothing the egg injects itself.
func TestOnInputSeesTypedCommandsOnly(t *testing.T) {
	r, w, _ := os.Pipe()
	var mu sync.Mutex
	var typed []string
	s := &supervisor.Server{
		Cmd:   exec.Command("sh", "-c", "read a; read b; echo done"),
		Stdin: r,
		Line:  func(string) {},
		OnInput: func(cmd string) {
			mu.Lock()
			typed = append(typed, cmd)
			mu.Unlock()
		},
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Run(context.Background()) }()
	time.Sleep(300 * time.Millisecond)
	s.Send("kickid 3") // the guard, not a person
	w.Write([]byte("changelevel de_dust2\n"))
	select {
	case <-errc:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(typed) != 1 || typed[0] != "changelevel de_dust2" {
		t.Fatalf("want the typed line only, got %q", typed)
	}
}
