// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync_test

import (
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/vpksync"
)

func at(h, m int) time.Time { return time.Date(2026, 9, 9, h, m, 0, 0, time.Local) }

func TestWindowParsesAndWrapsMidnight(t *testing.T) {
	w, err := vpksync.ParseWindow("23:00-05:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		h, m int
		open bool
	}{{23, 0, true}, {2, 30, true}, {4, 59, true}, {5, 0, false}, {12, 0, false}, {22, 59, false}} {
		if w.Open(at(tc.h, tc.m)) != tc.open {
			t.Fatalf("%02d:%02d open=%v", tc.h, tc.m, !tc.open)
		}
	}
	day, _ := vpksync.ParseWindow("04:00-06:00")
	if !day.Open(at(4, 0)) || day.Open(at(6, 0)) || day.Open(at(3, 59)) {
		t.Fatal("same-day window edges")
	}
	for _, bad := range []string{"", "4-6", "25:00-06:00", "04:00-04:00", "04:00"} {
		if _, err := vpksync.ParseWindow(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if err := (vpksync.Config{CS2Dir: "/a", SteamcmdDir: "/b", PushMethod: "copy", MaxWorkers: 1, CheckMinutes: 1, RestartPolicy: "window", RestartWindow: "nope"}).Check(); err == nil {
		t.Fatal("window policy with a bad window accepted")
	}
}

func TestDecideFollowsThePolicy(t *testing.T) {
	since := at(10, 0)
	base := vpksync.Config{MaxDelayHours: 12, RestartWindow: "04:00-06:00"}
	cases := []struct {
		name    string
		policy  vpksync.Policy
		players int
		known   bool
		now     time.Time
		want    vpksync.Decision
	}{
		{"immediate ignores players", vpksync.Immediate, 10, true, at(10, 1), vpksync.Restart},
		{"unknown state restarts (old image)", vpksync.Empty, 0, false, at(10, 1), vpksync.Restart},
		{"empty: players on = wait", vpksync.Empty, 3, true, at(10, 1), vpksync.Wait},
		{"empty: nobody on = restart", vpksync.Empty, 0, true, at(10, 1), vpksync.Restart},
		{"empty: max delay = countdown", vpksync.Empty, 3, true, at(22, 0), vpksync.Countdown},
		{"window: players, outside = wait", vpksync.InWindow, 3, true, at(13, 0), vpksync.Wait},
		{"window: players, inside = countdown", vpksync.InWindow, 3, true, at(4, 30).Add(24 * time.Hour), vpksync.Countdown},
		{"window: empty outside = restart", vpksync.InWindow, 0, true, at(13, 0), vpksync.Restart},
	}
	for _, tc := range cases {
		cfg := base
		cfg.RestartPolicy = tc.policy
		if got := vpksync.Decide(cfg, since, tc.players, tc.known, tc.now); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	noForce := base
	noForce.RestartPolicy, noForce.MaxDelayHours = vpksync.Empty, 0
	if vpksync.Decide(noForce, since, 1, true, at(10, 0).Add(100*time.Hour)) != vpksync.Wait {
		t.Fatal("max_delay_hours 0 must never force")
	}
}
