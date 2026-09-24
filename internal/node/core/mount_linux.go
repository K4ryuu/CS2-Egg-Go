// SPDX-License-Identifier: GPL-3.0-or-later

package core

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Mounted reports whether dst is a mount point in the container's mount
// namespace, read from the host side (no namespace switch, no hang risk).
func Mounted(pid int, dst string) bool {
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/mountinfo")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 4 && fields[4] == dst {
			return true
		}
	}
	return false
}

// BindReadOnly attaches src (host path) at dst inside the container's mount
// namespace, read-only. open_tree(OPEN_TREE_CLONE) from the host namespace
// yields a detached mount no namespace check applies to; move_mount then
// lands it after setns. Kernel 5.2+.
func BindReadOnly(pid int, src, dst string) error {
	if pid <= 0 {
		return fmt.Errorf("container not running")
	}
	if Mounted(pid, dst) {
		return nil
	}
	errc := make(chan error, 1)
	go func() {
		// the thread switches namespace for good: lock it and never unlock,
		// so the runtime discards it when this goroutine ends
		runtime.LockOSThread()
		errc <- func() error {
			tree, err := unix.OpenTree(unix.AT_FDCWD, src, unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
			if err != nil {
				return fmt.Errorf("open_tree: %w", err)
			}
			defer unix.Close(tree)
			ns, err := unix.Open("/proc/"+strconv.Itoa(pid)+"/ns/mnt", unix.O_RDONLY|unix.O_CLOEXEC, 0)
			if err != nil {
				return fmt.Errorf("open mnt ns: %w", err)
			}
			defer unix.Close(ns)
			// setns(CLONE_NEWNS) refuses a thread that shares fs state with
			// its siblings, which every Go thread does: unshare it first
			if err := unix.Unshare(unix.CLONE_FS); err != nil {
				return fmt.Errorf("unshare fs: %w", err)
			}
			if err := unix.Setns(ns, unix.CLONE_NEWNS); err != nil {
				return fmt.Errorf("setns: %w", err)
			}
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return fmt.Errorf("mkdir in container: %w", err)
			}
			if err := unix.MoveMount(tree, "", unix.AT_FDCWD, dst, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
				return fmt.Errorf("move_mount: %w", err)
			}
			if err := unix.Mount("none", dst, "none", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
				return fmt.Errorf("remount ro: %w", err)
			}
			return nil
		}()
	}()
	return <-errc
}
