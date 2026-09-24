// SPDX-License-Identifier: GPL-3.0-or-later

package cleanup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// openFiles lists the volume-relative paths the server process still holds
// open. Unlinking one of those frees no disk until the server restarts: the
// inode lives on behind the open descriptor, so the file only disappears
// from the listing and the quota stays exactly where it was.
//
// The paths in /proc/<pid>/fd are the container's own, so they are matched
// against the volume's mount point inside the container rather than the
// host path. A nil result means the process is gone or unreadable, in which
// case nothing is skipped.
func openFiles(pid int, volume string) map[string]struct{} {
	if pid <= 0 {
		return nil
	}
	dir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]struct{}{}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil || !filepath.IsAbs(target) {
			continue
		}
		// a deleted file the process still holds; nothing left to clean
		target = strings.TrimSuffix(target, " (deleted)")
		for _, base := range []string{volume, ContainerRoot} {
			if rel, ok := under(base, target); ok {
				out[rel] = struct{}{}
				break
			}
		}
	}
	return out
}

// ContainerRoot is where a server volume is mounted inside its container.
const ContainerRoot = "/home/container"

func under(base, target string) (string, bool) {
	if base == "" || !strings.HasPrefix(target, base+"/") {
		return "", false
	}
	return filepath.ToSlash(strings.TrimPrefix(target, base+"/")), true
}
