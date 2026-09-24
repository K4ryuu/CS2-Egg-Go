// SPDX-License-Identifier: GPL-3.0-or-later

package logx

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

// HardCapDays is the retention ceiling Rotate always enforces, even when
// maxDays is 0 or set higher: logs can never accumulate forever just because
// the config was left at its "disabled" value or misconfigured too high.
const HardCapDays = 365

// Rotate trims dir's *.log and *.log.gz files: older than the retention
// ceiling go, then the oldest go until maxFiles remain, then the oldest go
// until the total is under maxMB. A zero maxFiles or maxMB disables that
// limit; maxDays never fully disables, see HardCapDays.
func Rotate(dir string, maxDays, maxFiles, maxMB int, now time.Time) {
	type logFile struct {
		path string
		mod  time.Time
		size int64
	}
	ceiling := maxDays
	if ceiling <= 0 || ceiling > HardCapDays {
		ceiling = HardCapDays
	}
	var files []logFile
	var matches []string
	for _, pattern := range []string{"*.log", "*.log.gz"} {
		m, _ := filepath.Glob(filepath.Join(dir, pattern))
		matches = append(matches, m...)
	}
	for _, p := range matches {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if now.Sub(info.ModTime()) > time.Duration(ceiling)*24*time.Hour {
			os.Remove(p)
			continue
		}
		files = append(files, logFile{p, info.ModTime(), info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	drop := func() {
		os.Remove(files[0].path)
		files = files[1:]
	}
	for maxFiles > 0 && len(files) > maxFiles {
		drop()
	}
	if maxMB > 0 {
		var total int64
		for _, f := range files {
			total += f.size
		}
		for total > int64(maxMB)<<20 && len(files) > 0 {
			total -= files[0].size
			drop()
		}
	}
}
