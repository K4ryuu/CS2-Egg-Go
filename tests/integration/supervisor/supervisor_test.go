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
	ready := make(chan struct{}, 1)
	s := &supervisor.Server{
		Cmd:   exec.Command("sh", "-c", "echo ready; read x; echo got:$x"),
		Stdin: r,
		Line: func(l string) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
			if l == "ready" {
				ready <- struct{}{}
			}
		},
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Run(context.Background()) }()
	// type only once the program said ready, so the echo lands after it
	waitReady(t, ready)
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
	ready := make(chan struct{}, 1)
	s := &supervisor.Server{
		Cmd:         exec.Command("sh", "-c", "echo ready; while read x; do echo cmd:$x; [ \"$x\" = quit ] && exit 0; done"),
		StopCommand: "quit",
		Grace:       2 * time.Second,
		Line: func(l string) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
			if l == "ready" {
				ready <- struct{}{}
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Run(ctx) }()
	waitReady(t, ready)
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
	for _, l := range lines {
		if l == "kickid 7" || l == "quit" {
			t.Fatalf("the egg's own command echoed to the console: %q", joined)
		}
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
	ready := make(chan struct{}, 1)
	s := &supervisor.Server{
		Cmd:   exec.Command("sh", "-c", "echo ready; read a; read b; echo done"),
		Stdin: r,
		Line: func(l string) {
			if l == "ready" {
				ready <- struct{}{}
			}
		},
		OnInput: func(cmd string) {
			mu.Lock()
			typed = append(typed, cmd)
			mu.Unlock()
		},
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Run(context.Background()) }()
	waitReady(t, ready)
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

// A child left holding the terminal must not keep the run alive once the
// server process itself has exited.
func TestRunEndsWhenTheProcessExitsEvenIfAChildHoldsTheTerminal(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	s := &supervisor.Server{
		Cmd: exec.Command("sh", "-c", "trap '' HUP; sleep 30 & echo bye"),
		Line: func(l string) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
		},
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Run(context.Background()) }()
	select {
	case <-errc:
	case <-time.After(5 * time.Second):
		t.Fatal("run waited on the leftover child")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(lines, "|") != "bye" {
		t.Fatalf("lines: %q", lines)
	}
}

// A server that goes silent after a stop and never exits is killed once
// it has been quiet for Idle; one that keeps talking is left alone.
func TestSilentServerIsKilledAfterAStop(t *testing.T) {
	r, w, _ := os.Pipe()
	ready := make(chan struct{}, 1)
	s := &supervisor.Server{
		Cmd:   exec.Command("sh", "-c", "echo ready; read x; for i in 1 2 3 4 5; do echo busy; sleep 0.2; done; sleep 60"),
		Stdin: r,
		Line: func(l string) {
			if l == "ready" {
				ready <- struct{}{}
			}
		},
		Idle: 500 * time.Millisecond,
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Run(context.Background()) }()
	// a slow exec under load would eat the Idle window before the first "busy"
	waitReady(t, ready)
	start := time.Now()
	w.Write([]byte("quit\n"))
	select {
	case <-errc:
	case <-time.After(10 * time.Second):
		t.Fatal("silent server was not killed")
	}
	if d := time.Since(start); d < time.Second {
		t.Fatalf("killed after %v, while it was still printing", d)
	}
}

// waitReady blocks until the child printed "ready". Exec under load can take
// seconds, so a fixed sleep before the first write or Send is only a guess.
func waitReady(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("shell never started")
	}
}

func TestIdleDoesNothingBeforeAStop(t *testing.T) {
	s := &supervisor.Server{Cmd: exec.Command("sh", "-c", "sleep 1.5; echo bye"), Line: func(string) {}, Idle: 300 * time.Millisecond}
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
}
