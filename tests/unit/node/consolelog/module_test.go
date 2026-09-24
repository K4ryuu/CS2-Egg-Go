// SPDX-License-Identifier: GPL-3.0-or-later

package consolelog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/consolelog"
)

func TestConfigCheck(t *testing.T) {
	cases := []struct {
		name string
		cfg  consolelog.Config
		ok   bool
	}{
		{"defaults", consolelog.Defaults(), true},
		{"relative dir", consolelog.Config{Dir: "logs", KeepDays: 1}, false},
		{"empty dir", consolelog.Config{Dir: "", KeepDays: 1}, false},
		{"negative keep_days", consolelog.Config{Dir: "/var/lib/cs2node/logs", KeepDays: -1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Check()
			if (err == nil) != c.ok {
				t.Fatalf("Check() = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestWriteLineOneFilePerServerPerDay(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	consolelog.WriteLine(dir, "srv-1", "info", "hello", now)
	consolelog.WriteLine(dir, "srv-1", "server", "de_dust2 loaded", now)
	consolelog.WriteLine(dir, "srv-2", "server", "other server", now)

	data, err := os.ReadFile(filepath.Join(dir, "srv-1", "2026-06-15.log"))
	if err != nil {
		t.Fatalf("srv-1 log missing: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "[info] hello") || !strings.Contains(got, "[server] de_dust2 loaded") {
		t.Fatalf("srv-1 content: %q", got)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "srv-2", "2026-06-15.log")); err != nil {
		t.Fatalf("srv-2 log missing: %v", err)
	}
}

func TestMaintainCoversEveryServerDirNotJustOnesWrittenThisRun(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	mk := func(container, name string, age int) {
		sub := filepath.Join(dir, container)
		os.MkdirAll(sub, 0o755)
		p := filepath.Join(sub, name)
		os.WriteFile(p, []byte("x"), 0o644)
		mt := now.AddDate(0, 0, -age)
		os.Chtimes(p, mt, mt)
	}
	// srv-1: yesterday's file should compress, an ancient one should be pruned
	mk("srv-1", "2026-06-14.log", 1)
	mk("srv-1", "2026-01-01.log", 200)
	// srv-2: this run never wrote to it, Maintain must still find it on disk
	mk("srv-2", "2026-06-01.log", 14)

	consolelog.Maintain(dir, 30, now)

	if _, err := os.Stat(filepath.Join(dir, "srv-1", "2026-06-14.log.gz")); err != nil {
		t.Fatalf("srv-1 yesterday's file must be compressed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "srv-1", "2026-01-01.log")); !os.IsNotExist(err) {
		t.Fatalf("srv-1 file past keep_days must be pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "srv-2", "2026-06-01.log.gz")); err != nil {
		t.Fatalf("srv-2 (untouched this run) must still be maintained: %v", err)
	}
}
