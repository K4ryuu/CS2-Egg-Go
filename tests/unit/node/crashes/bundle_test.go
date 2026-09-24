// SPDX-License-Identifier: GPL-3.0-or-later

package crashes_test

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/crashes"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

func entries(t *testing.T, path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		out[h.Name] = string(data)
	}
}

func TestBundleKeepsConsoleVersionsAndFreshFilesOnly(t *testing.T) {
	vol := t.TempDir()
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-time.Hour)
	mk := func(rel, body string, mtime time.Time) {
		p := filepath.Join(vol, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
		os.Chtimes(p, mtime, mtime)
	}
	mk("egg/versions.txt", "Metamod=2.0.0-git1350\n", old)
	mk("game/csgo/logs/fresh.log", "boom", now)
	mk("game/csgo/pak01_dir.vpk", "vpk", now)
	mk("game/csgo/old.log", "stale", old)
	mk("steamapps/appmanifest_730.acf", "acf", now)
	os.Symlink(filepath.Join(vol, "game/csgo/logs/fresh.log"), filepath.Join(vol, "link.log"))

	m, err := crashes.Bundle(dir, "srv-1", vol, &proto.Crash{ExitCode: 134, Lines: []string{"a", "Aborted (core dumped)"}, Map: "de_dust2", Players: 3}, now)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, m.Path)
	if got["console.txt"] != "a\nAborted (core dumped)\n" {
		t.Fatalf("console: %q", got["console.txt"])
	}
	if got["versions.txt"] != "Metamod=2.0.0-git1350\n" {
		t.Fatalf("versions: %q", got["versions.txt"])
	}
	if got["files/game/csgo/logs/fresh.log"] != "boom" {
		t.Fatalf("fresh log missing: %v", got)
	}
	for _, bad := range []string{"files/game/csgo/pak01_dir.vpk", "files/game/csgo/old.log", "files/steamapps/appmanifest_730.acf", "files/link.log"} {
		if _, ok := got[bad]; ok {
			t.Fatalf("%s must not be in the bundle", bad)
		}
	}
	if len(m.Files) != 1 || m.Files[0] != "game/csgo/logs/fresh.log" || m.Bytes == 0 || m.Map != "de_dust2" {
		t.Fatalf("meta: %+v", m)
	}
	list := crashes.List(dir, "")
	if len(list) != 1 || list[0].Path != m.Path {
		t.Fatalf("list: %+v", list)
	}
	text, err := crashes.Console(m.Path)
	if err != nil || text != got["console.txt"] {
		t.Fatalf("console read: %v %q", err, text)
	}
}

func TestPruneByCountAndAge(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if _, err := crashes.Bundle(dir, "srv-1", "", &proto.Crash{ExitCode: 1}, base.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	crashes.Bundle(dir, "srv-2", "", &proto.Crash{ExitCode: 1}, base.Add(-40*24*time.Hour))
	now := base.Add(4 * 24 * time.Hour)
	if n := crashes.Prune(dir, 2, 30, now); n != 3 {
		t.Fatalf("removed %d, want 3 (2 over count, 1 too old)", n)
	}
	left := crashes.List(dir, "")
	if len(left) != 2 || left[0].Container != "srv-1" || !left[0].Time.Equal(base.Add(3*24*time.Hour)) {
		t.Fatalf("left: %+v", left)
	}
	if n := crashes.Prune(dir, 0, 0, now); n != 0 {
		t.Fatal("zero limits must keep everything")
	}
}
