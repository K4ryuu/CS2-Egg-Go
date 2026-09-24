// SPDX-License-Identifier: GPL-3.0-or-later

package logx

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Compress gzips every dir/YYYY-MM-DD.log file whose date is before now's
// calendar day, then removes the plain file. Today's file, already-compressed
// files and names that are not a date are left alone. The .log.gz keeps the
// source file's mtime so Rotate's age accounting still applies to it.
func Compress(dir string, now time.Time) {
	today := dateOnly(now)
	matches, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	for _, p := range matches {
		base := strings.TrimSuffix(filepath.Base(p), ".log")
		day, err := time.ParseInLocation("2006-01-02", base, now.Location())
		if err != nil || !dateOnly(day).Before(today) {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := gzipFile(p, p+".gz", info.ModTime()); err == nil {
			os.Remove(p)
		}
	}
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func gzipFile(src, dst string, mtime time.Time) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	_, copyErr := io.Copy(gz, in)
	closeErr := gz.Close()
	if err := out.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil || closeErr != nil {
		os.Remove(dst)
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	os.Chtimes(dst, mtime, mtime)
	return nil
}
