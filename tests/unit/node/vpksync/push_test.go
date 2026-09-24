// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/vpksync"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(path string) string { b, _ := os.ReadFile(path); return string(b) }

func central(t *testing.T) string {
	src := t.TempDir()
	write(t, filepath.Join(src, "game/csgo/pak01_000.vpk"), "vpk-a")
	write(t, filepath.Join(src, "game/csgo/maps/de_dust2.vpk"), "vpk-b")
	write(t, filepath.Join(src, "game/bin/linuxsteamrt64/cs2"), "binary")
	write(t, filepath.Join(src, "game/csgo/gameinfo.gi"), "central-gameinfo")
	write(t, filepath.Join(src, "game/csgo/cfg/server.cfg"), "central-cfg")
	write(t, filepath.Join(src, "steamapps/appmanifest_730.acf"), "\"AppState\"\n{\n\t\"buildid\"\t\t\"12345\"\n}\n")
	write(t, filepath.Join(src, "Steam/junk"), "x")
	return src
}

func TestSymlinkPushVerifyAndPrune(t *testing.T) {
	src := central(t)
	dst := t.TempDir()
	write(t, filepath.Join(dst, "game/csgo/gameinfo.gi"), "server-gameinfo")
	write(t, filepath.Join(dst, "game/csgo/cfg/server.cfg"), "server-cfg")
	write(t, filepath.Join(dst, "game/csgo/pak01_000.vpk"), "old-real-file")
	os.Symlink("/home/container/foreign.vpk", filepath.Join(dst, "game/csgo/foreign.vpk"))
	os.Symlink(vpksync.MountDst+"/game/csgo/gone.vpk", filepath.Join(dst, "game/csgo/gone.vpk"))

	rep, err := vpksync.Push(src, dst, vpksync.Symlink, core.Owner{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Linked != 2 || rep.Copied != 1 || rep.Pruned != 1 {
		t.Fatalf("report %+v", rep)
	}
	if link, _ := os.Readlink(filepath.Join(dst, "game/csgo/pak01_000.vpk")); link != vpksync.MountDst+"/game/csgo/pak01_000.vpk" {
		t.Fatalf("vpk link: %q", link)
	}
	if read(filepath.Join(dst, "game/csgo/gameinfo.gi")) != "server-gameinfo" || read(filepath.Join(dst, "game/csgo/cfg/server.cfg")) != "server-cfg" {
		t.Fatal("server-owned files must never be overwritten")
	}
	if read(filepath.Join(dst, "game/bin/linuxsteamrt64/cs2")) != "binary" {
		t.Fatal("binary not copied")
	}
	if _, err := os.Lstat(filepath.Join(dst, "game/csgo/foreign.vpk")); err != nil {
		t.Fatal("foreign links must stay")
	}
	if _, err := os.Lstat(filepath.Join(dst, "game/csgo/gone.vpk")); err == nil {
		t.Fatal("stale shared link must go")
	}
	if _, err := os.Stat(filepath.Join(dst, "Steam/junk")); err == nil {
		t.Fatal("Steam/ and steamapps/ never leave the central dir")
	}
	if !vpksync.Verify(src, dst, vpksync.Symlink) {
		t.Fatal("verify must pass right after a push")
	}
	os.Remove(filepath.Join(dst, "game/csgo/maps/de_dust2.vpk"))
	if vpksync.Verify(src, dst, vpksync.Symlink) {
		t.Fatal("a missing link must fail verify")
	}
	rep, _ = vpksync.Push(src, dst, vpksync.Symlink, core.Owner{})
	if rep.Linked != 1 || rep.Copied != 0 {
		t.Fatalf("second push must only restore the missing link: %+v", rep)
	}
	if vpksync.BuildID(src) != "12345" {
		t.Fatalf("buildid: %q", vpksync.BuildID(src))
	}
	if vpksync.CountVPKs(src) != 2 {
		t.Fatal("vpk count")
	}
}

func TestCopyAndHardlinkModes(t *testing.T) {
	src := central(t)
	dst := t.TempDir()
	rep, err := vpksync.Push(src, dst, vpksync.Copy, core.Owner{})
	if err != nil || rep.Linked != 2 {
		t.Fatalf("copy push: %+v %v", rep, err)
	}
	if read(filepath.Join(dst, "game/csgo/pak01_000.vpk")) != "vpk-a" || !vpksync.Verify(src, dst, vpksync.Copy) {
		t.Fatal("copy mode content or verify")
	}
	// an older copy than its source is stale
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(filepath.Join(dst, "game/csgo/pak01_000.vpk"), old, old)
	if vpksync.Verify(src, dst, vpksync.Copy) {
		t.Fatal("older copy must fail verify")
	}

	dst2 := t.TempDir()
	rep, err = vpksync.Push(src, dst2, vpksync.Hardlink, core.Owner{})
	if err != nil || rep.Linked != 2 || !vpksync.Verify(src, dst2, vpksync.Hardlink) {
		t.Fatalf("hardlink push: %+v %v", rep, err)
	}
	rep, _ = vpksync.Push(src, dst2, vpksync.Hardlink, core.Owner{})
	if rep.Linked != 0 {
		t.Fatal("hardlinks in place must not be redone")
	}
}
