// SPDX-License-Identifier: GPL-3.0-or-later

// Package supervisor runs the game server under a pseudo terminal, feeds it
// the panel's console input plus injected commands, and hands every output
// line to the caller. It replaces script(1), the stdin FIFO and the stty
// dance of the bash entrypoint.
package supervisor

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
)

// Server is one server run.
type Server struct {
	Cmd   *exec.Cmd
	Stdin *os.File // the panel's console; nil = none
	// OnInput sees each line the operator types in the panel, before it
	// reaches the server. Commands the egg injects itself do not go here.
	OnInput func(cmd string)
	Line    func(string) // every output line, in order, without the newline
	// StopCommand is written when ctx is cancelled; the process gets Grace
	// to exit before it is killed.
	StopCommand string
	Grace       time.Duration

	mu       sync.Mutex
	ptmx     *os.File
	stopping atomic.Bool
	partial  []byte // stdin bytes since the last newline, to spot a typed "quit"
}

// Stopping reports whether a stop was asked for: the panel typed quit (or
// exit), or ctx was cancelled. An abort after that is the engine's exit,
// not a crash.
func (s *Server) Stopping() bool { return s.stopping.Load() }

// noteInput watches forwarded console input for a stop command and hands
// each finished line to OnInput.
func (s *Server) noteInput(b []byte, fromPanel bool) {
	for _, c := range b {
		if c != '\n' && c != '\r' {
			s.partial = append(s.partial, c)
			continue
		}
		line := strings.TrimSpace(string(s.partial))
		switch strings.ToLower(line) {
		case "quit", "exit":
			s.stopping.Store(true)
		}
		if line != "" && fromPanel && s.OnInput != nil {
			s.OnInput(line)
		}
		s.partial = s.partial[:0]
	}
}

// Send writes one console command to the server's stdin.
func (s *Server) Send(cmd string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ptmx == nil {
		return errors.New("server not running")
	}
	cmd = strings.TrimRight(cmd, "\r\n")
	s.noteInput([]byte(cmd+"\n"), false)
	_, err := io.WriteString(s.ptmx, cmd+"\n")
	return err
}

// Run starts the server and returns when it has exited. The returned error
// is the process's exit error, if any.
func (s *Server) Run(ctx context.Context) error {
	ptmx, err := pty.Start(s.Cmd)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ptmx = ptmx
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.ptmx = nil
		s.mu.Unlock()
		ptmx.Close()
	}()

	if s.Stdin != nil {
		// two ptys would echo a typed command: the panel's and the server's.
		// The engine keeps its own in cooked mode, so the panel side goes quiet.
		if restore, err := disableEcho(s.Stdin); err == nil {
			defer restore()
		}
		pty.InheritSize(s.Stdin, ptmx)
		go func() {
			// a blocked read on the panel tty cannot be interrupted; the
			// goroutine dies with the process
			buf := make([]byte, 4096)
			for {
				n, err := s.Stdin.Read(buf)
				if n > 0 {
					s.mu.Lock()
					p := s.ptmx
					s.noteInput(buf[:n], true)
					s.mu.Unlock()
					if p == nil {
						return
					}
					p.Write(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(ptmx)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			s.Line(strings.TrimRight(sc.Text(), "\r"))
		}
	}()

	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.stopping.Store(true)
			if s.StopCommand != "" {
				s.Send(s.StopCommand)
			}
			grace := s.Grace
			if grace == 0 {
				grace = 30 * time.Second
			}
			select {
			case <-stopped:
			case <-time.After(grace):
				s.Cmd.Process.Kill()
			}
		case <-stopped:
		}
	}()

	<-done // pty EOF: the process closed its terminal
	err = s.Cmd.Wait()
	close(stopped)
	return err
}
