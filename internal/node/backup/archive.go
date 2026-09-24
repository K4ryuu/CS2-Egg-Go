// SPDX-License-Identifier: GPL-3.0-or-later

// Package backup writes a tar.gz of each server volume on a schedule, kept
// outside the volumes with per-server retention. Everything goes in except
// what can be re-fetched: VPKs, symlinks, files the central CS2 install
// provides, the Steam trees, demos.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/bundles"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
)

// DefaultExclude is what a backup never needs.
var DefaultExclude = []string{"*.vpk", "*.dem", "steamapps", "Steam", "steamcmd", ".steam", "temps", "egg/cache", "egg/cs2node.sock"}

// Rules pick the files. A pattern matches a volume-relative path when it
// matches the whole path, its basename, or names one of its parent dirs.
type Rules struct {
	Include []string // empty = everything
	Exclude []string
	Central string // the vpksync install; a file with the same path and size there is skipped
}

func matches(patterns []string, rel string) bool {
	base := filepath.Base(rel)
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

// Wants says whether a path goes in. For a dir, false means skip the tree.
func (r Rules) Wants(rel string, isDir bool, size int64) bool {
	if matches(r.Exclude, rel) {
		return false
	}
	if isDir {
		return true // includes are decided per file
	}
	if len(r.Include) > 0 && !matches(r.Include, rel) {
		return false
	}
	if r.Central != "" {
		if st, err := os.Stat(filepath.Join(r.Central, rel)); err == nil && st.Mode().IsRegular() && st.Size() == size {
			return false
		}
	}
	return true
}

// Meta describes one backup; it sits next to the archive as JSON.
type Meta struct {
	Container string    `json:"container"`
	Time      time.Time `json:"time"`
	Path      string    `json:"path"`
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`   // archive size
	Raw       int64     `json:"raw"`     // bytes before compression
	Changed   []string  `json:"changed"` // files that changed while being read (padded)
	Seconds   float64   `json:"seconds"` // how long it took
	Include   []string  `json:"include"` // rules used
	Exclude   []string  `json:"exclude"`
}

// Archive writes <dir>/<container>/<stamp>.tar.gz of volume under rules.
// Files are opened through os.Root: the volume is the server's, a symlink
// in it must never hand root a file from outside.
func Archive(dir, container, volume string, rules Rules, now time.Time) (Meta, error) {
	start := time.Now()
	root, err := os.OpenRoot(volume)
	if err != nil {
		return Meta{}, err
	}
	defer root.Close()
	sub := filepath.Join(dir, container)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return Meta{}, err
	}
	m := Meta{Container: container, Time: now, Path: filepath.Join(sub, bundles.Stamp(now)+".tar.gz"), Include: rules.Include, Exclude: rules.Exclude}
	tmp := m.Path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return Meta{}, err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	fail := func(err error) (Meta, error) {
		tw.Close()
		gz.Close()
		f.Close()
		os.Remove(tmp)
		return Meta{}, err
	}
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry, keep going
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if !rules.Wants(rel, true, 0) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // symlinks, sockets, devices
			return nil
		}
		info, err := d.Info()
		if err != nil || !rules.Wants(rel, false, info.Size()) {
			return nil
		}
		src, err := root.Open(rel)
		if err != nil {
			return nil
		}
		defer src.Close()
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		n, err := io.CopyN(tw, src, info.Size())
		if err == io.EOF { // shrank while we read it: pad so the archive stays valid
			if _, err := io.CopyN(tw, zeros{}, info.Size()-n); err != nil {
				return err
			}
			m.Changed = append(m.Changed, rel)
		} else if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		m.Files++
		m.Raw += info.Size()
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := gz.Close(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, m.Path); err != nil {
		return Meta{}, err
	}
	if st, err := os.Stat(m.Path); err == nil {
		m.Bytes = st.Size()
	}
	m.Seconds = time.Since(start).Seconds()
	return m, bundles.WriteMeta(m.Path, m)
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// Estimate is what the rules would include from a volume, uncompressed.
// Used to refuse a backup that would fill the disk, so it walks and stats
// but never opens a file.
func Estimate(volume string, rules Rules) int64 {
	root, err := os.OpenRoot(volume)
	if err != nil {
		return 0
	}
	defer root.Close()
	var total int64
	fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if !rules.Wants(rel, true, 0) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err == nil && rules.Wants(rel, false, info.Size()) {
			total += info.Size()
		}
		return nil
	})
	return total
}

// Restore unpacks archive over volume, files owned by owner. Existing files
// are overwritten, nothing is deleted. Writes go through os.Root, so an
// entry name or a symlink in the volume cannot land a file outside it.
func Restore(archive, volume string, owner core.Owner) (int, error) {
	root, err := os.OpenRoot(volume)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	f, err := os.Open(archive)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	tr := tar.NewReader(gz)
	n := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		rel := filepath.Clean(hdr.Name)
		if filepath.IsAbs(rel) || rel == "." || strings.HasPrefix(rel, "../") {
			return n, fmt.Errorf("archive entry escapes the volume: %q", hdr.Name)
		}
		if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return n, err
		}
		if st, err := root.Lstat(rel); err == nil && !st.Mode().IsRegular() {
			root.Remove(rel) // a link where the backup has a file: the file wins
		}
		out, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
		if err != nil {
			return n, err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return n, err
		}
		out.Close()
		root.Chown(rel, owner.UID, owner.GID)
		root.Chtimes(rel, hdr.ModTime, hdr.ModTime)
		n++
	}
}

// List reads every backup's meta under dir, newest first.
func List(dir, container string) []Meta {
	entries := bundles.List(dir, container)
	out := make([]Meta, 0, len(entries))
	for _, e := range entries {
		var m Meta
		if json.Unmarshal(e.Raw, &m) == nil {
			out = append(out, m)
		}
	}
	return out
}
