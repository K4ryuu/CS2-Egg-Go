// SPDX-License-Identifier: GPL-3.0-or-later

package cleanup_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/cleanup"
)

func touch(t *testing.T, path string, age time.Duration, size int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	os.Chtimes(path, mt, mt)
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func open(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func TestAgeAndPatternAndRecursion(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "game/csgo/old.dem"), 200*time.Hour, 10)
	touch(t, filepath.Join(root, "game/csgo/new.dem"), 1*time.Hour, 10)
	touch(t, filepath.Join(root, "game/csgo/sub/deep.dem"), 200*time.Hour, 10)
	touch(t, filepath.Join(root, "game/csgo/keep.txt"), 200*time.Hour, 10)
	res := cleanup.Run(open(t, root), []cleanup.Rule{{Name: "demos", Directories: []string{"./game/csgo"}, Patterns: []string{"*.dem"}, Hours: 168, Recursive: yes()}}, cleanup.Options{})
	if res.Files != 2 || res.Bytes != 20 || res.PerRule["demos"].Files != 2 {
		t.Fatalf("result: %+v", res)
	}
	if exists(filepath.Join(root, "game/csgo/old.dem")) || !exists(filepath.Join(root, "game/csgo/new.dem")) || !exists(filepath.Join(root, "game/csgo/keep.txt")) {
		t.Fatal("wrong files removed")
	}
	res = cleanup.Run(open(t, root), []cleanup.Rule{{Name: "flat", Directories: []string{"game/csgo"}, Patterns: []string{"*.dem"}, Hours: 0, Recursive: no()}}, cleanup.Options{})
	if res.Files != 1 || !exists(filepath.Join(root, "game/csgo/sub")) {
		t.Fatalf("non-recursive rule with hours 0 must remove only the root file: %+v", res)
	}
}

func TestMissingDirAndSymlinksAreSkipped(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "real/core"), 0, 5)
	os.Symlink(filepath.Join(root, "real/core"), filepath.Join(root, "real/core.1"))
	res := cleanup.Run(open(t, root), []cleanup.Rule{
		{Name: "gone", Directories: []string{"./nope"}, Patterns: []string{"*"}},
		{Name: "core", Directories: []string{"./real"}, Patterns: []string{"core", "core.[0-9]*"}},
	}, cleanup.Options{})
	if res.Files != 1 || len(res.Errors) != 0 {
		t.Fatalf("symlink must not count as a file: %+v", res)
	}
}

func TestDeleteParentDirProtectsRootAndNestedBundles(t *testing.T) {
	root := t.TempDir()
	crash := filepath.Join(root, "dumps/crashreport")
	touch(t, filepath.Join(crash, "loose.dmp"), 300*time.Hour, 1)          // parent is the rule root: single delete
	touch(t, filepath.Join(crash, "bundle-a/x.dmp"), 300*time.Hour, 1)     // leaf bundle: whole dir goes
	touch(t, filepath.Join(crash, "bundle-a/notes.txt"), 300*time.Hour, 1) // counted with it
	touch(t, filepath.Join(crash, "holder/y.dmp"), 300*time.Hour, 1)       // parent has a subdir: single delete
	touch(t, filepath.Join(crash, "holder/other/z.txt"), 300*time.Hour, 1) // must survive
	res := cleanup.Run(open(t, root), []cleanup.Rule{{Name: "crash", Directories: []string{"./dumps/crashreport"}, Patterns: []string{"*.dmp"}, Hours: 168, Recursive: yes(), DeleteParentDir: true}}, cleanup.Options{})
	if res.Files != 4 || res.PerRule["crash"].Files != 4 {
		t.Fatalf("want 4 files (loose, bundle x + notes, holder y): %+v", res)
	}
	if !exists(crash) || exists(filepath.Join(crash, "bundle-a")) || exists(filepath.Join(crash, "holder/y.dmp")) || !exists(filepath.Join(crash, "holder/other/z.txt")) {
		t.Fatal("root or nested bundle handling wrong")
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{5: "5 B", 2048: "2.00 KB", 1234567: "1.18 MB", 3 << 30: "3.00 GB"}
	for n, want := range cases {
		if got := cleanup.FormatSize(n); got != want {
			t.Errorf("%d: got %q want %q", n, got, want)
		}
	}
}

func TestRefusesPathsOutsideTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	touch(t, filepath.Join(outside, "secret.dem"), 0, 7)
	os.Symlink(outside, filepath.Join(root, "escape"))
	res := cleanup.Run(open(t, root), []cleanup.Rule{
		{Name: "absolute", Directories: []string{outside}, Patterns: []string{"*.dem"}},
		{Name: "dotdot", Directories: []string{"../"}, Patterns: []string{"*.dem"}},
		{Name: "symlinked", Directories: []string{"./escape"}, Patterns: []string{"*.dem"}},
	}, cleanup.Options{})
	if res.Files != 0 {
		t.Fatalf("nothing outside the root may be deleted: %+v", res)
	}
	if !exists(filepath.Join(outside, "secret.dem")) {
		t.Fatal("a file outside the root was deleted")
	}
	if len(res.Errors) != 2 {
		t.Fatalf("the absolute and the .. rule must both be reported: %v", res.Errors)
	}
}

func TestSkipLeavesFilesAlone(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "logs/open.log"), 100*time.Hour, 4)
	touch(t, filepath.Join(root, "logs/closed.log"), 100*time.Hour, 4)
	res := cleanup.Run(open(t, root), []cleanup.Rule{{Name: "logs", Directories: []string{"logs"}, Patterns: []string{"*.log"}, Hours: 1}},
		cleanup.Options{Skip: func(rel string) bool { return rel == "logs/open.log" }})
	if res.Files != 1 || res.Skipped != 1 {
		t.Fatalf("want one deleted and one skipped: %+v", res)
	}
	if !exists(filepath.Join(root, "logs/open.log")) {
		t.Fatal("a skipped file was deleted")
	}
}
