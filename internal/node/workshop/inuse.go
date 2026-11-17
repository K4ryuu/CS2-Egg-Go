// SPDX-License-Identifier: GPL-3.0-or-later

package workshop

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// watchMarker: when the node started watching /proc
const watchMarker = ".watched"

// MarkInUse touches every stored version a process has open (the engine
// keeps mounted vpks open). With seed on, that's the only real use signal.
func (s Store) MarkInUse(proc string, now time.Time) {
	marker := filepath.Join(s.Dir, watchMarker)
	if _, err := os.Stat(marker); err != nil {
		os.WriteFile(marker, nil, 0o644)
	}
	prefix := s.Mount + "/" + AppID + "/"
	pids, _ := os.ReadDir(proc)
	for _, pid := range pids {
		if _, err := strconv.Atoi(pid.Name()); err != nil {
			continue
		}
		fdDir := filepath.Join(proc, pid.Name(), "fd")
		fds, _ := os.ReadDir(fdDir)
		for _, fd := range fds {
			target, _ := os.Readlink(filepath.Join(fdDir, fd.Name()))
			rest, ok := strings.CutPrefix(target, prefix)
			if parts := strings.SplitN(rest, "/", 3); ok && len(parts) == 3 {
				os.Chtimes(s.versionDir(parts[0], parts[1]), now, now)
			}
		}
	}
}

// idle: unused for keep_days, never counting from before the watch marker
func (s Store) idle(id, manifest string, now time.Time) bool {
	st, err := os.Stat(s.versionDir(id, manifest))
	if s.KeepDays <= 0 || err != nil {
		return false
	}
	last := st.ModTime()
	if w, err := os.Stat(filepath.Join(s.Dir, watchMarker)); err == nil && w.ModTime().After(last) {
		last = w.ModTime()
	}
	return now.Sub(last) >= time.Duration(s.KeepDays)*24*time.Hour
}
