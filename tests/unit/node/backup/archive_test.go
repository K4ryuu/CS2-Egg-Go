// SPDX-License-Identifier: GPL-3.0-or-later

package backup_test

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/backup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/bundles"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
)

func names(t *testing.T, archive string) map[string]string {
	f, err := os.Open(archive)
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

func write(t *testing.T, root, rel, body string) {
	p := filepath.Join(root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultRulesKeepPluginsAndConfigsOnly(t *testing.T) {
	vol, central, dir := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, vol, "game/csgo/cfg/server.cfg", "hostname x")
	write(t, vol, "game/csgo/addons/counterstrikesharp/plugins/A/A.dll", "dll")
	write(t, vol, "game/csgo/gameinfo.gi", "patched gameinfo")
	write(t, vol, "game/csgo/pak01_dir.vpk", "vpk")
	write(t, vol, "game/csgo/replays/match.dem", "demo")
	write(t, vol, "game/bin/linuxsteamrt64/cs2", "engine")
	write(t, vol, "steamapps/appmanifest_730.acf", "acf")
	write(t, vol, "egg/versions.txt", "Metamod=1")
	write(t, vol, "egg/cache/css.zip", "zip")
	os.Symlink(filepath.Join(vol, "egg/versions.txt"), filepath.Join(vol, "link.txt"))
	write(t, central, "game/bin/linuxsteamrt64/cs2", "engine")      // same size: central, skipped
	write(t, central, "game/csgo/gameinfo.gi", "original gameinfo") // different size: the server's own, kept

	rules := backup.Rules{Exclude: backup.DefaultExclude, Central: central}
	m, err := backup.Archive(dir, "srv-1", vol, rules, time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got := names(t, m.Path)
	for _, want := range []string{"game/csgo/cfg/server.cfg", "game/csgo/addons/counterstrikesharp/plugins/A/A.dll", "game/csgo/gameinfo.gi", "egg/versions.txt"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s missing from the backup", want)
		}
	}
	for _, bad := range []string{"game/csgo/pak01_dir.vpk", "game/csgo/replays/match.dem", "game/bin/linuxsteamrt64/cs2", "steamapps/appmanifest_730.acf", "egg/cache/css.zip", "link.txt"} {
		if _, ok := got[bad]; ok {
			t.Errorf("%s must not be in the backup", bad)
		}
	}
	if m.Files != 4 || m.Bytes == 0 || filepath.Base(m.Path) != "20260909-030000.tar.gz" {
		t.Fatalf("meta: %+v", m)
	}
	if list := backup.List(dir, "srv-1"); len(list) != 1 || list[0].Files != 4 {
		t.Fatalf("list: %+v", list)
	}
}

func TestIncludeNarrowsAndRestorePutsFilesBack(t *testing.T) {
	vol, dir := t.TempDir(), t.TempDir()
	write(t, vol, "game/csgo/cfg/server.cfg", "cfg")
	write(t, vol, "game/csgo/addons/x.so", "so")
	rules := backup.Rules{Include: []string{"game/csgo/cfg"}, Exclude: backup.DefaultExclude}
	m, err := backup.Archive(dir, "srv-1", vol, rules, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := names(t, m.Path); len(got) != 1 || got["game/csgo/cfg/server.cfg"] != "cfg" {
		t.Fatalf("include: %v", got)
	}
	os.WriteFile(filepath.Join(vol, "game/csgo/cfg/server.cfg"), []byte("broken"), 0o644)
	n, err := backup.Restore(m.Path, vol, core.Owner{UID: os.Getuid(), GID: os.Getgid()})
	if err != nil || n != 1 {
		t.Fatalf("restore: %v %d", err, n)
	}
	if data, _ := os.ReadFile(filepath.Join(vol, "game/csgo/cfg/server.cfg")); string(data) != "cfg" {
		t.Fatalf("restored content: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(vol, "game/csgo/addons/x.so")); string(data) != "so" {
		t.Fatal("restore must not delete other files")
	}
}

func TestRetentionSharedWithCrashBundles(t *testing.T) {
	dir := t.TempDir()
	vol := t.TempDir()
	write(t, vol, "a.txt", "a")
	base := time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, err := backup.Archive(dir, "srv-1", vol, backup.Rules{}, base.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if n := bundles.Prune(dir, 2, 0, base.Add(72*time.Hour)); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	if left := backup.List(dir, ""); len(left) != 2 || !left[0].Time.Equal(base.Add(48*time.Hour)) {
		t.Fatalf("left: %+v", left)
	}
}
