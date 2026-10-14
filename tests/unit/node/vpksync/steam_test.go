// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/vpksync"
)

func ctime(t *testing.T, path string) syscall.Timespec {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return statCtime(fi.Sys().(*syscall.Stat_t))
}

// the update check runs every minute: an unchanged steamclient.so must not be
// rewritten each time
func TestCopyChangedSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.so"), filepath.Join(dir, "sdk64/steamclient.so")
	write(t, src, "client-v1")
	if err := vpksync.CopyChanged(src, dst); err != nil || read(dst) != "client-v1" {
		t.Fatalf("first copy: %v %q", err, read(dst))
	}
	before := ctime(t, dst)
	time.Sleep(20 * time.Millisecond)
	if err := vpksync.CopyChanged(src, dst); err != nil {
		t.Fatal(err)
	}
	if ctime(t, dst) != before {
		t.Fatal("unchanged file was rewritten")
	}
	write(t, src, "client-v2")
	future := time.Now().Add(time.Minute)
	os.Chtimes(src, future, future)
	if err := vpksync.CopyChanged(src, dst); err != nil || read(dst) != "client-v2" {
		t.Fatalf("changed copy: %v %q", err, read(dst))
	}
}

func TestWorldReadableLeavesCorrectModes(t *testing.T) {
	root := t.TempDir()
	file, exe := filepath.Join(root, "game/csgo/pak01_000.vpk"), filepath.Join(root, "game/cs2")
	write(t, file, "vpk")
	write(t, exe, "bin")
	os.Chmod(file, 0o600)
	os.Chmod(exe, 0o700)
	vpksync.WorldReadable(root)
	for path, want := range map[string]os.FileMode{file: 0o644, exe: 0o755} {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != want {
			t.Fatalf("%s mode %o, want %o", path, fi.Mode().Perm(), want)
		}
	}
	before := ctime(t, file)
	time.Sleep(20 * time.Millisecond)
	vpksync.WorldReadable(root)
	if ctime(t, file) != before {
		t.Fatal("chmod ran on a file that already had the right mode")
	}
}
