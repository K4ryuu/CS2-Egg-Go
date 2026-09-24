// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
)

// Report is what one push did.
type Report struct {
	Copied, Linked, Pruned int
}

// skipped never leaves the central dir: the server owns these
func skipped(rel string) bool {
	switch {
	case rel == "steamapps" || strings.HasPrefix(rel, "steamapps/"):
	case rel == "Steam" || strings.HasPrefix(rel, "Steam/"):
	case rel == "game/csgo/cfg" || strings.HasPrefix(rel, "game/csgo/cfg/"):
	case rel == "game/csgo/gameinfo.gi":
	default:
		return false
	}
	return true
}

// Push syncs src (the central install) into dst (a volume). Regular files
// are copied when size or mtime differ, VPKs go per method, ownership of
// existing files is never touched, gameinfo.gi and cfg files only land when
// absent, then game/ is chowned to the volume owner.
//
// Every write goes through os.Root: the volume is the container user's,
// and a symlink planted in it must never steer root outside.
func Push(src, dst string, method Method, owner core.Owner) (Report, error) {
	var rep Report
	root, err := os.OpenRoot(dst)
	if err != nil {
		return rep, err
	}
	defer root.Close()
	var firstErr error
	var touched []string // everything this push created, to chown at the end
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		if rel == "." {
			return nil
		}
		if skipped(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			if _, err := root.Stat(rel); err != nil {
				if err := root.MkdirAll(rel, 0o755); err != nil {
					note(err)
					return filepath.SkipDir
				}
				touched = append(touched, rel) // we created it, so we own it
			}
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err == nil {
				if cur, err := root.Readlink(rel); err != nil || cur != link {
					root.Remove(rel)
					note(root.Symlink(link, rel))
					touched = append(touched, rel)
				}
			}
		case strings.HasSuffix(rel, ".vpk"):
			linked, err := placeVPK(root, path, rel, method, owner)
			note(err)
			if linked {
				rep.Linked++
				touched = append(touched, rel)
			}
		default:
			copied, err := copyIfChanged(root, path, rel)
			note(err)
			if copied {
				rep.Copied++
				touched = append(touched, rel)
			}
		}
		return nil
	})
	note(err)
	// files the server owns: only when absent, never overwritten
	if _, err := root.Stat("game/csgo/gameinfo.gi"); err != nil {
		if _, err := copyIfChanged(root, filepath.Join(src, "game/csgo/gameinfo.gi"), "game/csgo/gameinfo.gi"); err == nil {
			rep.Copied++
			touched = append(touched, "game/csgo/gameinfo.gi")
		}
	}
	if entries, err := os.ReadDir(filepath.Join(src, "game/csgo/cfg")); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || (!strings.HasSuffix(name, ".cfg") && !strings.HasSuffix(name, ".vcfg")) {
				continue
			}
			if _, err := root.Stat("game/csgo/cfg/" + name); err != nil {
				if _, err := copyIfChanged(root, filepath.Join(src, "game/csgo/cfg", name), "game/csgo/cfg/"+name); err == nil {
					rep.Copied++
					touched = append(touched, "game/csgo/cfg/"+name)
				}
			}
		}
	}
	rep.Pruned = pruneStaleLinks(root, src)
	chownPaths(root, touched, owner)
	return rep, firstErr
}

// placeVPK reports whether the file was (re)placed.
func placeVPK(root *os.Root, srcPath, rel string, method Method, owner core.Owner) (bool, error) {
	dir := filepath.Dir(rel)
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	switch method {
	case Symlink:
		want := filepath.Join(MountDst, rel)
		if cur, err := root.Readlink(rel); err == nil && cur == want {
			return false, nil
		}
		root.RemoveAll(rel)
		if err := root.Symlink(want, rel); err != nil {
			return false, err
		}
		root.Lchown(rel, owner.UID, owner.GID)
		return true, nil
	case Hardlink:
		if sameInode(srcPath, root, rel) {
			return false, nil
		}
		root.RemoveAll(rel)
		if d, err := root.Open(dir); err == nil {
			same := sameDevice(srcPath, d)
			if same {
				// the link shares the central inode: never chown it, the
				// central file stays root's (and read-only to the server)
				err = unix.Linkat(unix.AT_FDCWD, srcPath, int(d.Fd()), filepath.Base(rel), 0)
			}
			d.Close()
			if same && err == nil {
				return true, nil
			}
		}
		// cross-filesystem: copy once, later pushes see it as current
		return copyVPK(root, srcPath, rel, owner)
	default:
		return copyVPK(root, srcPath, rel, owner)
	}
}

func copyVPK(root *os.Root, srcPath, rel string, owner core.Owner) (bool, error) {
	copied, err := copyIfChanged(root, srcPath, rel)
	if copied {
		root.Chmod(rel, 0o644)
		root.Chown(rel, owner.UID, owner.GID)
	}
	return copied, err
}

// copyIfChanged mirrors rsync's quick check: size and mtime.
func copyIfChanged(root *os.Root, src, rel string) (bool, error) {
	si, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	if di, err := root.Stat(rel); err == nil && di.Mode().IsRegular() && di.Size() == si.Size() && di.ModTime().Equal(si.ModTime()) {
		return false, nil
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return false, err
	}
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	tmp := rel + ".cs2node-tmp"
	out, err := root.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, si.Mode().Perm())
	if err != nil {
		return false, err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		root.Remove(tmp)
		return false, err
	}
	root.Chtimes(tmp, si.ModTime(), si.ModTime())
	// keep the existing file's owner: rename would drop it, so copy the ids over
	if di, err := root.Lstat(rel); err == nil {
		if st, ok := di.Sys().(*syscall.Stat_t); ok {
			root.Chown(tmp, int(st.Uid), int(st.Gid))
		}
	}
	return true, root.Rename(tmp, rel)
}

// Verify reports whether every central VPK is current in dst.
func Verify(src, dst string, method Method) bool {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return false
	}
	defer root.Close()
	ok := true
	filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !ok {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		if skipped(rel) && d.IsDir() {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(rel, ".vpk") {
			return nil
		}
		switch method {
		case Symlink:
			cur, err := root.Readlink(rel)
			ok = err == nil && cur == filepath.Join(MountDst, rel)
		case Hardlink:
			same := false
			if dir, err := root.Open(filepath.Dir(rel)); err == nil {
				same = sameDevice(path, dir)
				dir.Close()
			}
			if same {
				ok = sameInode(path, root, rel)
			} else {
				ok = currentCopy(path, root, rel)
			}
		default:
			ok = currentCopy(path, root, rel)
		}
		return nil
	})
	return ok
}

// currentCopy: same size and the copy is not older than its source.
func currentCopy(src string, root *os.Root, rel string) bool {
	si, err := os.Stat(src)
	if err != nil {
		return false
	}
	di, err := root.Stat(rel)
	return err == nil && di.Mode().IsRegular() && di.Size() == si.Size() && !di.ModTime().Before(si.ModTime())
}

// PruneStaleLinks removes *.vpk symlinks under dst that point into the
// shared mount but whose central source is gone. Links pointing anywhere
// else are the server's business and stay.
func PruneStaleLinks(src, dst string) int {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return 0
	}
	defer root.Close()
	return pruneStaleLinks(root, src)
}

func pruneStaleLinks(root *os.Root, src string) int {
	removed := 0
	fs.WalkDir(root.FS(), "game", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink == 0 || !strings.HasSuffix(d.Name(), ".vpk") {
			return nil
		}
		link, err := root.Readlink(rel)
		if err != nil || !strings.HasPrefix(link, MountDst+"/") {
			return nil
		}
		if _, err := os.Stat(filepath.Join(src, strings.TrimPrefix(link, MountDst+"/"))); err != nil {
			if root.Remove(rel) == nil {
				removed++
			}
		}
		return nil
	})
	return removed
}

// chownTree hands a subtree to the owner, links included, nothing followed.
// chownPaths gives the server what this push created, and nothing else.
//
// It used to walk the whole of game/ and issue one lchown per entry, which
// on a 35 GB CS2 install is tens of thousands of syscalls per container per
// push, every push, even one that changed nothing. After a Valve update
// that runs for every server on the node at once.
func chownPaths(root *os.Root, rels []string, owner core.Owner) {
	if owner.UID == 0 && owner.GID == 0 || len(rels) == 0 {
		return
	}
	done := make(map[string]bool, len(rels)*2)
	for _, rel := range rels {
		// the parents too: MkdirAll may have created several levels
		for p := rel; p != "." && p != "/" && !done[p]; p = filepath.Dir(p) {
			done[p] = true
			root.Lchown(p, owner.UID, owner.GID)
		}
	}
}

func statT(path string) (*syscall.Stat_t, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return st, ok
}

func rootStatT(root *os.Root, rel string) (*syscall.Stat_t, bool) {
	fi, err := root.Stat(rel)
	if err != nil {
		return nil, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return st, ok
}

func sameInode(a string, root *os.Root, rel string) bool {
	sa, ok1 := statT(a)
	sb, ok2 := rootStatT(root, rel)
	return ok1 && ok2 && sa.Dev == sb.Dev && sa.Ino == sb.Ino
}

// sameDevice: the central file and an open volume dir sit on one filesystem.
func sameDevice(a string, dir *os.File) bool {
	sa, ok := statT(a)
	if !ok {
		return false
	}
	fi, err := dir.Stat()
	if err != nil {
		return false
	}
	sb, ok := fi.Sys().(*syscall.Stat_t)
	return ok && sa.Dev == sb.Dev
}

// CountVPKs counts VPK files under a central install.
func CountVPKs(dir string) int {
	n := 0
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".vpk") {
			n++
		}
		return nil
	})
	return n
}

var errNoBuild = errors.New("no build")

var buildLine = regexp.MustCompile(`"buildid"\s+"([0-9]+)"`)

// BuildID reads the installed CS2 build from the app manifest; "" when
// there is no install yet.
func BuildID(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "steamapps", "appmanifest_730.acf"))
	if err != nil {
		return ""
	}
	if m := buildLine.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// copyFile is a plain copy for the central dir (root's own, no volume).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
