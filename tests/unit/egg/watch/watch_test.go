// SPDX-License-Identifier: GPL-3.0-or-later

package watch_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/watch"
)

func TestMapAndPlayersFromConsoleLines(t *testing.T) {
	w := watch.New(100)
	if w.Feed("some noise") {
		t.Fatal("noise must not change state")
	}
	if !w.Feed("Host activate: Loading (de_dust2)") || w.State().Map != "de_dust2" {
		t.Fatal("map load")
	}
	if !w.Feed("Client 10 'K4ryuu -'' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL") || w.State().Players != 1 {
		t.Fatal("join")
	}
	if w.Feed("Client 10 'K4ryuu -'' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL") {
		t.Fatal("same player again is not a change")
	}
	w.Feed("Client 11 'Other' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL")
	if w.State().Players != 2 {
		t.Fatal("second join")
	}
	if !w.Feed("SV:  Dropped client 'Other' from server(29): NETWORK_DISCONNECT_TIMEDOUT") || w.State().Players != 1 {
		t.Fatal("leave")
	}
	if !w.Feed("Host activate: Changelevel (de_mirage)") || w.State().Map != "de_mirage" || w.State().Players != 1 {
		t.Fatal("changelevel keeps players")
	}
	if !w.Feed("Server is hibernating") || w.State().Players != 0 {
		t.Fatal("hibernating = empty")
	}
}

func TestRecentTrimsToTheByteBudget(t *testing.T) {
	w := watch.New(10)
	for i := 0; i < 10; i++ {
		w.Feed(strings.Repeat("x", 100))
	}
	if got := w.Recent(3 * 108); len(got) != 3 {
		t.Fatalf("budget: %d lines", len(got))
	}
}

func TestRecentKeepsTheLastLinesInOrder(t *testing.T) {
	w := watch.New(3)
	for i := 1; i <= 5; i++ {
		w.Feed(fmt.Sprintf("line %d", i))
	}
	got := w.Recent(1 << 20)
	if len(got) != 3 || got[0] != "line 3" || got[2] != "line 5" {
		t.Fatalf("recent: %v", got)
	}
	w2 := watch.New(3)
	w2.Feed("a")
	if r := w2.Recent(1 << 20); len(r) != 1 || r[0] != "a" {
		t.Fatalf("partial ring: %v", r)
	}
}

// The name players see is a convar, so the egg asks for it and reads the
// answer back. This is the real line a CS2 server prints.
func TestHostnameReply(t *testing.T) {
	cases := map[string]string{
		"hostname = K4ryuu ! Dev Server ! k4ryuu.com": "K4ryuu ! Dev Server ! k4ryuu.com",
		`"hostname" = "Retake #1"`:                    "Retake #1",
		`"hostname" = "Retake #1" ( def. "" )`:        "Retake #1",
		"hostname =  padded  ":                        "padded",
	}
	for line, want := range cases {
		w := watch.New(10)
		w.Feed(line)
		if got := w.State().Name; got != want {
			t.Errorf("%q\n got  %q\n want %q", line, got, want)
		}
		if !watch.IsHostnameReply(line) {
			t.Errorf("%q must be recognised as the answer to ask for", line)
		}
	}
	// the echoed command and ordinary console lines are not an answer
	for _, line := range []string{"hostname", "Host activate: Loading (de_dust2)", "[All Chat][x (2)]: hostname = lol"} {
		if watch.IsHostnameReply(line) {
			t.Errorf("%q must not count as a hostname answer", line)
		}
	}
}
