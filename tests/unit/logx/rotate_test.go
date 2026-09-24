// SPDX-License-Identifier: GPL-3.0-or-later

package logx_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

func TestRotateByAgeCountAndSize(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	mk := func(name string, ageDays int, size int) {
		p := filepath.Join(dir, name)
		os.WriteFile(p, make([]byte, size), 0o644)
		mt := now.Add(-time.Duration(ageDays) * 24 * time.Hour)
		os.Chtimes(p, mt, mt)
	}
	mk("2026-01-01.log", 30, 10)     // too old
	mk("2026-02-01.log", 6, 600<<10) // oldest survivor by age, removed by size
	mk("2026-02-02.log", 5, 600<<10)
	mk("2026-02-03.log", 4, 100)
	mk("2026-02-04.log", 3, 100)
	mk("2026-02-05.log", 2, 100)
	mk("notes.txt", 40, 1)
	logx.Rotate(dir, 7, 4, 1, now)
	left, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	names := map[string]bool{}
	for _, p := range left {
		names[filepath.Base(p)] = true
	}
	// age drops 01-01; count 4 drops 02-01; size 1 MB drops 02-02 (600 KB + 300 B > 1 MB? no) -> keeps
	if names["2026-01-01.log"] || names["2026-02-01.log"] {
		t.Fatalf("age/count rotation wrong: %v", names)
	}
	if !names["2026-02-02.log"] || !names["2026-02-05.log"] {
		t.Fatalf("survivors wrong: %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal("non-log files must be left alone")
	}
	logx.Rotate(dir, 0, 0, 0, now)
	if left, _ := filepath.Glob(filepath.Join(dir, "*.log")); len(left) != 4 {
		t.Fatalf("zero limits must not delete: %d", len(left))
	}
}
