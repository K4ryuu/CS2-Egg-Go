// SPDX-License-Identifier: GPL-3.0-or-later

package logx_test

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

func mkLog(t *testing.T, dir, name string, ageDays int, now time.Time, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := now.AddDate(0, 0, -ageDays)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
}

// TestCompressLeavesTodayAlone: the rotation boundary is the calendar day,
// not a rolling 24h window, so today's file stays plain even seconds before
// midnight.
func TestCompressLeavesTodayAlone(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 15, 23, 59, 0, 0, time.UTC)
	mkLog(t, dir, "2026-06-15.log", 0, now, "today")
	mkLog(t, dir, "2026-06-14.log", 1, now, "yesterday")

	logx.Compress(dir, now)

	if _, err := os.Stat(filepath.Join(dir, "2026-06-15.log")); err != nil {
		t.Fatalf("today's file must stay uncompressed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-06-14.log")); !os.IsNotExist(err) {
		t.Fatalf("yesterday's plain file should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-06-14.log.gz")); err != nil {
		t.Fatalf("yesterday's file should be compressed: %v", err)
	}
}

// TestCompressAgedFileIsReadableGzip verifies the .gz actually holds the
// original content and the plain file it replaces is removed.
func TestCompressAgedFileIsReadableGzip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	mkLog(t, dir, "2026-06-01.log", 14, now, "[12:00:00] [info] hello\n")

	logx.Compress(dir, now)

	f, err := os.Open(filepath.Join(dir, "2026-06-01.log.gz"))
	if err != nil {
		t.Fatalf("compressed file missing: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("not a valid gzip stream: %v", err)
	}
	defer gz.Close()
	got, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("reading gzip content: %v", err)
	}
	if string(got) != "[12:00:00] [info] hello\n" {
		t.Fatalf("content changed by compression: %q", got)
	}
}

// TestRotateHardCapAppliesEvenWhenMaxDaysIsZero: max_days=0 used to mean
// "disabled". It must now still be bounded by logx.HardCapDays, so a
// misconfigured or default-left retention setting can never grow the log
// directory forever.
func TestRotateHardCapAppliesEvenWhenMaxDaysIsZero(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	mkLog(t, dir, "ancient.log", logx.HardCapDays+10, now, "x")
	mkLog(t, dir, "recent.log", 1, now, "x")

	logx.Rotate(dir, 0, 0, 0, now)

	if _, err := os.Stat(filepath.Join(dir, "ancient.log")); !os.IsNotExist(err) {
		t.Fatalf("file past the hard cap must be removed even with max_days=0: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "recent.log")); err != nil {
		t.Fatalf("recent file must survive: %v", err)
	}
}

// TestRotateCountsCompressedFiles: Rotate's age/count/size accounting must
// include .log.gz files, or compression would let retention limits leak.
func TestRotateCountsCompressedFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	mkLog(t, dir, "2026-01-01.log.gz", 30, now, "x")
	mkLog(t, dir, "2026-02-01.log", 1, now, "x")

	logx.Rotate(dir, 7, 0, 0, now)

	if _, err := os.Stat(filepath.Join(dir, "2026-01-01.log.gz")); !os.IsNotExist(err) {
		t.Fatalf("aged compressed file must still be dropped by max_days: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-02-01.log")); err != nil {
		t.Fatalf("recent file must survive: %v", err)
	}
}
