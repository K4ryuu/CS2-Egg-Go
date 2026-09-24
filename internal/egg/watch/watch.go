// SPDX-License-Identifier: GPL-3.0-or-later

// Package watch keeps the egg's own picture of the running server from the
// console: current map, joined players, and the last lines for a crash
// report. It runs on every line, guard or no guard.
package watch

import (
	"regexp"
	"strings"
	"sync"

	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

var (
	reMap   = regexp.MustCompile(`Host activate: (?:Loading|Changelevel) \((.+?)\)`)
	reFull  = regexp.MustCompile(`Client ([0-9]+) '(.*)' signon state [A-Z_]+ -> SIGNONSTATE_FULL`)
	reDrop  = regexp.MustCompile(`^SV:  Dropped client '(.*)' from server`)
	reQuiet = regexp.MustCompile(`Server is hibernating`)
	// the engine's answer to a bare `hostname`, quoted or not, with the
	// "( def. ... )" tail some builds append
	reName = regexp.MustCompile(`^"?hostname"?\s*=\s*"?(.*?)"?\s*(?:\( def\..*)?$`)
)

// Watch is the tracker. Safe for concurrent use.
type Watch struct {
	mu      sync.Mutex
	state   proto.ServerState
	players map[string]bool // by name
	ring    []string
	next    int
	full    bool
}

// New keeps the last keep lines.
func New(keep int) *Watch {
	if keep < 1 {
		keep = 1
	}
	return &Watch{players: map[string]bool{}, ring: make([]string, keep), state: proto.ServerState{Up: true}}
}

// Feed records the line and reports whether the map or player count changed.
func (w *Watch) Feed(line string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ring[w.next] = line
	w.next = (w.next + 1) % len(w.ring)
	if w.next == 0 {
		w.full = true
	}
	// each pattern is evaluated once, not twice: this runs on every line
	before := w.state
	if m := reMap.FindStringSubmatch(line); m != nil {
		w.state.Map = clip(m[1], 64)
	} else if m := reFull.FindStringSubmatch(line); m != nil {
		if len(w.players) < maxPlayers {
			w.players[m[2]] = true
		}
	} else if m := reDrop.FindStringSubmatch(line); m != nil {
		delete(w.players, m[1])
	} else if reQuiet.MatchString(line) {
		w.players = map[string]bool{}
	} else if m := reName.FindStringSubmatch(line); m != nil {
		if n := clip(strings.TrimSpace(m[1]), 64); n != "" {
			w.state.Name = n
		}
	} else {
		return false
	}
	w.state.Players = len(w.players)
	return w.state != before
}

// State is the current picture.
func (w *Watch) State() proto.ServerState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// IsHostnameReply reports whether a line is the engine answering a bare
// `hostname`, so the caller can keep its own query out of the console.
func IsHostnameReply(line string) bool { return reName.MatchString(line) }

// SetName records a name taken from somewhere other than the console, such
// as server.cfg at boot. The console always wins when it answers.
func (w *Watch) SetName(name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if name = clip(strings.TrimSpace(name), 64); name == "" || name == w.state.Name {
		return false
	}
	w.state.Name = name
	return true
}

// SetUp records whether the server process is running.
func (w *Watch) SetUp(up bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.Up = up
}

// Recent returns the kept lines, oldest first, trimmed from the front so
// they fit in maxBytes (the socket message cap).
func (w *Watch) Recent(maxBytes int) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	if !w.full {
		out = append(out, w.ring[:w.next]...)
	} else {
		out = append(out, w.ring[w.next:]...)
		out = append(out, w.ring[:w.next]...)
	}
	total := 0
	for i := len(out) - 1; i >= 0; i-- {
		total += len(out[i]) + 8 // json overhead per line
		if total > maxBytes {
			return out[i+1:]
		}
	}
	return out
}

// maxPlayers bounds the name set: the console is the server's to fake.
const maxPlayers = 256

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
