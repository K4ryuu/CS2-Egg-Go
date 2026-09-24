// SPDX-License-Identifier: GPL-3.0-or-later

// Package volume_test proves that nothing the node writes into a server
// volume as root can be steered outside it by a symlink the container user
// planted. One test per writer.
package volume_test

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/addoncache"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/backup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/crashes"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/vpksync"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/workshop"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

func me() core.Owner { return core.Owner{UID: os.Getuid(), GID: os.Getgid()} }

// outside is the directory an attacker points symlinks at; anything landing
// there is a failed test.
func outside(t *testing.T) string {
	dir := t.TempDir()
	t.Cleanup(func() {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			t.Errorf("escaped into %s: %s", dir, e.Name())
		}
	})
	return dir
}

func TestPushNeverFollowsAVolumeSymlink(t *testing.T) {
	src, vol, out := t.TempDir(), t.TempDir(), outside(t)
	os.MkdirAll(filepath.Join(src, "game/csgo"), 0o755)
	os.WriteFile(filepath.Join(src, "game/csgo/pak01_dir.vpk"), []byte("vpk"), 0o644)
	os.WriteFile(filepath.Join(src, "game/bin"), []byte("bin"), 0o644)
	os.Symlink(out, filepath.Join(vol, "game")) // game/ -> outside
	for _, method := range []vpksync.Method{vpksync.Copy, vpksync.Symlink, vpksync.Hardlink} {
		if _, err := vpksync.Push(src, vol, method, me()); err == nil {
			t.Errorf("%s push through a symlinked game/ must fail", method)
		}
	}
}

func TestDeliverNeverFollowsAVolumeSymlink(t *testing.T) {
	vol, out := t.TempDir(), outside(t)
	os.MkdirAll(filepath.Join(vol, "egg"), 0o755)
	os.Symlink(out, filepath.Join(vol, "egg/cache")) // egg/cache -> outside
	cached := filepath.Join(t.TempDir(), "css.zip")
	os.WriteFile(cached, []byte("zip"), 0o644)
	if _, err := addoncache.Deliver(cached, vol, me()); err == nil {
		t.Fatal("delivery through a symlinked egg/cache must fail")
	}
	if !addoncache.Allowed("https://github.com/x/y/releases/download/v1/a.zip") || addoncache.Allowed("https://evil.example/a.zip") || addoncache.Allowed("http://github.com/x") {
		t.Fatal("host allow list")
	}
}

func TestRestoreNeverFollowsAVolumeSymlink(t *testing.T) {
	src, vol, out := t.TempDir(), t.TempDir(), outside(t)
	os.MkdirAll(filepath.Join(src, "game/csgo/cfg"), 0o755)
	os.WriteFile(filepath.Join(src, "game/csgo/cfg/server.cfg"), []byte("cfg"), 0o644)
	m, err := backup.Archive(t.TempDir(), "srv", src, backup.Rules{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	os.Symlink(out, filepath.Join(vol, "game")) // game/ -> outside
	if _, err := backup.Restore(m.Path, vol, me()); err == nil {
		t.Fatal("restore through a symlinked game/ must fail")
	}
}

func TestArchiveAndBundleNeverReadThroughAVolumeSymlink(t *testing.T) {
	vol, secretDir := t.TempDir(), t.TempDir()
	secret := filepath.Join(secretDir, "shadow")
	os.WriteFile(secret, []byte("root:secret"), 0o644)
	os.MkdirAll(filepath.Join(vol, "egg"), 0o755)
	os.Symlink(secret, filepath.Join(vol, "egg/versions.txt"))
	os.Symlink(secretDir, filepath.Join(vol, "leak"))
	m, err := backup.Archive(t.TempDir(), "srv", vol, backup.Rules{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if m.Files != 0 {
		t.Fatalf("backup followed a symlink: %+v", m)
	}
	b, err := crashes.Bundle(t.TempDir(), "srv", vol, &proto.Crash{ExitCode: 1, Lines: []string{"x"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	text, _ := crashes.Console(b.Path)
	if text != "x\n" || len(b.Files) != 0 {
		t.Fatalf("bundle: %+v", b)
	}
	f, _ := os.Open(b.Path)
	defer f.Close()
	buf := make([]byte, 1<<20)
	n, _ := f.Read(buf)
	if containsSecret(buf[:n]) {
		t.Fatal("bundle carries the file behind the versions.txt symlink")
	}
}

func containsSecret(gz []byte) bool {
	// gzip of a tiny archive: scanning the raw bytes is enough, the secret
	// would appear literally in a stored block
	return len(gz) > 0 && string(gz) != "" && contains(gz, []byte("root:secret"))
}

func contains(b, sub []byte) bool {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return true
		}
	}
	return false
}

func TestCacheRefusesHostsOutsideTheAllowList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	defer srv.Close()
	c := addoncache.Open(t.TempDir(), srv.Client(), time.Hour)
	// the cache itself downloads what it is told; the module gates on Allowed
	if _, err := c.File(context.Background(), srv.URL+"/a.zip"); err != nil {
		t.Fatal(err)
	}
	if addoncache.Allowed(srv.URL + "/a.zip") {
		t.Fatal("a plain http test server must not be allowed")
	}
}

// The workshop store is bind-mounted read-only into every container, so a
// file the daemon copies into it is a file the attacker can then read.
//
// What this test pins: a link pointing out of the volume is never absorbed
// and never followed, and a real absorb still happens alongside it. What it
// does NOT stage is the race the fix is really for, where a container swaps
// a checked regular file for a symlink between the enumeration and the
// copy. That window is closed by construction instead: the copy reads
// through the same os.Root the enumeration used, so a link out of the
// volume is refused whenever it appears. A unit test cannot hold the two
// halves of that race open.
func TestWorkshopAbsorbRefusesASymlinkOutOfTheVolume(t *testing.T) {
	vol := t.TempDir()
	store := t.TempDir()
	secret := filepath.Join(t.TempDir(), "shadow")
	os.WriteFile(secret, []byte("root:$6$hash"), 0o600)

	item := filepath.Join(vol, "game/bin/linuxsteamrt64/steamapps/workshop/content/730/3070293560")
	os.MkdirAll(item, 0o755)
	os.WriteFile(filepath.Join(item, "meta.vpk"), make([]byte, 32), 0o644)
	// the file was a regular file when it was enumerated; now it is a link
	os.Symlink(secret, filepath.Join(item, "escape.vpk"))
	acf := filepath.Join(vol, "game/bin/linuxsteamrt64/steamapps/workshop/appworkshop_730.acf")
	os.MkdirAll(filepath.Dir(acf), 0o755)
	os.WriteFile(acf, []byte(`"AppWorkshop" { "WorkshopItemsInstalled" { "3070293560" { "manifest" "111" } } }`), 0o644)

	s := workshop.Store{Dir: store}
	s.Sync(vol, core.Owner{UID: os.Getuid(), GID: os.Getgid()}, false, nil)

	// the sync has to have actually absorbed something, or this test
	// proves nothing at all
	absorbed := 0
	var leaked []string
	filepath.WalkDir(store, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			absorbed++
		}
		if err != nil || d.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(p); strings.Contains(string(b), "root:$6$hash") {
			leaked = append(leaked, p)
		}
		return nil
	})
	if len(leaked) > 0 {
		t.Fatalf("a symlink out of the volume was followed into the shared store: %v", leaked)
	}
	if absorbed == 0 {
		t.Fatal("nothing reached the store, so the test proves nothing")
	}

}
