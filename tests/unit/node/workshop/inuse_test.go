// SPDX-License-Identifier: GPL-3.0-or-later

package workshop_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/workshop"
)

// stored puts realACF's item in a fresh store with keep_days 7, last used
// the given number of days ago. The volume it came from links it.
func stored(t *testing.T, daysAgo int) (workshop.Store, string) {
	t.Helper()
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop", KeepDays: 7}
	vol := volume(t, realACF)
	if _, err := store.Sync(vol, me(), true, nil); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-time.Duration(daysAgo) * 24 * time.Hour)
	if err := os.Chtimes(versionDir(store, v1), when, when); err != nil {
		t.Fatal(err)
	}
	return store, vol
}

func versionDir(store workshop.Store, manifest string) string {
	return filepath.Join(store.Dir, workshop.AppID, "3070290869", manifest)
}

func usedLately(t *testing.T, store workshop.Store) bool {
	t.Helper()
	st, err := os.Stat(versionDir(store, v1))
	if err != nil {
		t.Fatal(err)
	}
	return time.Since(st.ModTime()) < time.Hour
}

// the engine keeps a mounted map or addon vpk open; seen from the host the
// fd points at the path inside the container, under the mount
func TestAnOpenVPKCountsAsUse(t *testing.T) {
	store, _ := stored(t, 30)
	proc := t.TempDir()
	fd := filepath.Join(proc, "4242", "fd")
	if err := os.MkdirAll(fd, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Symlink("/dev/null", filepath.Join(fd, "0"))
	os.Symlink(store.LinkTarget("3070290869", v1, "3070290869_dir.vpk"), filepath.Join(fd, "7"))
	os.MkdirAll(filepath.Join(proc, "sys"), 0o755)
	store.MarkInUse(proc, time.Now())
	if !usedLately(t, store) {
		t.Fatal("an open vpk must restart the clock")
	}
}

func TestSeedLetsGoOfWhatNobodyUsed(t *testing.T) {
	store, vol := stored(t, 8)
	// seed off: the link is the use, nothing changes
	if rep, err := store.Sync(vol, me(), false, nil); err != nil || len(rep.Released) != 0 {
		t.Fatalf("seed off: %+v %v", rep, err)
	}
	rep, err := store.Sync(vol, me(), true, nil)
	if err != nil || len(rep.Released) != 1 {
		t.Fatalf("unused for 8 days with keep_days 7: %+v %v", rep, err)
	}
	assertNotInstalled(t, vol)
	if rep, err := store.Sync(t.TempDir(), me(), true, nil); err != nil || len(rep.Seeded) != 0 {
		t.Fatalf("an unused addon is not handed out: %+v %v", rep, err)
	}
}

// the node only knows what was used since it started watching
func TestWatchStartIsAGracePeriod(t *testing.T) {
	store, vol := stored(t, 30)
	store.MarkInUse(t.TempDir(), time.Now())
	if rep, err := store.Sync(vol, me(), true, nil); err != nil || len(rep.Released) != 0 {
		t.Fatalf("dropped right after the watch began: %+v %v", rep, err)
	}
}

func TestOwnDownloadCountsAsUse(t *testing.T) {
	store, _ := stored(t, 30)
	rep, err := store.Sync(volume(t, realACF), me(), true, nil)
	if err != nil || len(rep.Linked) != 1 {
		t.Fatalf("second copy: %+v %v", rep, err)
	}
	if !usedLately(t, store) {
		t.Fatal("a server downloaded it, that is use")
	}
}

func TestALinkIntoAPrunedVersionIsDropped(t *testing.T) {
	store, vol := stored(t, 0)
	os.RemoveAll(filepath.Dir(versionDir(store, v1)))
	rep, err := store.Sync(vol, me(), false, nil)
	if err != nil || len(rep.Released) != 1 {
		t.Fatalf("dangling links: %+v %v", rep, err)
	}
	assertNotInstalled(t, vol)
}

func TestPruneWithSeedGoesByUseNotLinks(t *testing.T) {
	store, vol := stored(t, 8)
	conts := []core.Container{{Volume: vol}}
	if n, _ := store.Prune(conts, nil, true, time.Now()); n != 0 {
		t.Fatal("a running server links it, a map vote could still pick it")
	}
	if usedLately(t, store) {
		t.Fatal("with seed on a link is no use")
	}
	if n, _ := store.Prune(nil, conts, false, time.Now()); n != 0 {
		t.Fatal("seed off: a stopped server's link holds it")
	}
	store, vol = stored(t, 8)
	if n, _ := store.Prune(nil, []core.Container{{Volume: vol}}, true, time.Now()); n != 1 {
		t.Fatal("only a stopped server links it and nobody used it")
	}
}
