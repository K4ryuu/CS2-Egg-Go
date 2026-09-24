// SPDX-License-Identifier: GPL-3.0-or-later

// Package crashes keeps a bundle per server crash: the console tail the egg
// sent, the files the crash left behind (dumps, logs) and the addon
// versions, as one tar.gz outside the volume.
package crashes

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/bundles"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

const (
	// Window before the crash in which a modified file counts as crash output.
	Window = 2 * time.Minute
	// MaxFile is the biggest file that goes into a bundle; a full core dump
	// of the engine is several GiB and would fill the disk.
	MaxFile = 512 << 20
	// MaxTotal caps one bundle's raw input: the volume is the server's to
	// fill, the node's disk is not.
	MaxTotal = 2 << 30
)

// skipDirs never hold crash output; they are also the biggest trees.
var skipDirs = map[string]bool{"steamapps": true, "Steam": true, "steamcmd": true, ".steam": true}

// Meta describes one bundle; it sits next to the tar.gz as JSON so the
// listing never opens archives.
type Meta struct {
	Container string    `json:"container"`
	Time      time.Time `json:"time"`
	ExitCode  int       `json:"exit_code"`
	Map       string    `json:"map,omitempty"`
	Players   int       `json:"players"`
	Files     []string  `json:"files"`   // volume-relative paths included
	Skipped   []string  `json:"skipped"` // too big
	Bytes     int64     `json:"bytes"`   // bundle size on disk
	Path      string    `json:"path"`    // the tar.gz
}

// Bundle writes <dir>/<container>/<stamp>.tar.gz (+ .json) for one crash
// reported by the egg. volume may be empty (unknown); then only the console
// goes in.
func Bundle(dir, container, volume string, cr *proto.Crash, now time.Time) (Meta, error) {
	sub := filepath.Join(dir, container)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return Meta{}, err
	}
	m := Meta{Container: container, Time: now, ExitCode: cr.ExitCode, Map: cr.Map, Players: cr.Players, Path: filepath.Join(sub, bundles.Stamp(now)+".tar.gz")}
	tmp := m.Path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return Meta{}, err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	write := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: now}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	fail := func(err error) (Meta, error) {
		tw.Close()
		gz.Close()
		f.Close()
		os.Remove(tmp)
		return Meta{}, err
	}
	if err := write("console.txt", []byte(strings.Join(cr.Lines, "\n")+"\n")); err != nil {
		return fail(err)
	}
	if volume != "" {
		// reads go through os.Root: a symlink in the volume must not hand
		// root a file from outside it
		root, err := os.OpenRoot(volume)
		if err != nil {
			return fail(err)
		}
		defer root.Close()
		if v, err := root.ReadFile("egg/versions.txt"); err == nil && len(v) < 64<<10 {
			if err := write("versions.txt", v); err != nil {
				return fail(err)
			}
		}
		var total int64
		for _, rel := range recentFiles(volume, now.Add(-Window)) {
			st, err := root.Lstat(rel)
			if err != nil || !st.Mode().IsRegular() {
				continue
			}
			if st.Size() > MaxFile || total+st.Size() > MaxTotal {
				m.Skipped = append(m.Skipped, rel)
				continue
			}
			src, err := root.Open(rel)
			if err != nil {
				continue
			}
			total += st.Size()
			hdr := &tar.Header{Name: "files/" + rel, Mode: 0o644, Size: st.Size(), ModTime: st.ModTime()}
			if err := tw.WriteHeader(hdr); err != nil {
				src.Close()
				return fail(err)
			}
			if _, err := io.CopyN(tw, src, st.Size()); err != nil {
				src.Close()
				return fail(fmt.Errorf("%s: %w", rel, err))
			}
			src.Close()
			m.Files = append(m.Files, rel)
		}
	}
	var info strings.Builder
	fmt.Fprintf(&info, "container: %s\ntime: %s\nexit_code: %d\nmap: %s\nplayers: %d\n", container, now.Format(time.RFC3339), cr.ExitCode, cr.Map, cr.Players)
	for _, s := range m.Skipped {
		fmt.Fprintf(&info, "skipped (over %d MiB): %s\n", MaxFile>>20, s)
	}
	if err := write("info.txt", []byte(info.String())); err != nil {
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
	return m, bundles.WriteMeta(m.Path, m)
}

// recentFiles lists regular files under volume modified since t, skipping
// VPKs, links, sockets and the Steam trees. ponytail: a stat walk of the
// whole volume, a few seconds on a 35 GB install; fine for a crash.
func recentFiles(volume string, since time.Time) []string {
	var out []string
	filepath.WalkDir(volume, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != volume && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasSuffix(d.Name(), ".vpk") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		rel, _ := filepath.Rel(volume, path)
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// List reads every bundle's meta under dir, newest first; container filters
// to one server when set.
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

// Prune keeps the newest keepCount bundles per server and none older than
// keepDays (0 = unlimited for either).
func Prune(dir string, keepCount, keepDays int, now time.Time) int {
	return bundles.Prune(dir, keepCount, keepDays, now)
}

// Console returns console.txt from a bundle.
func Console(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", errors.New("no console.txt in " + path)
		}
		if err != nil {
			return "", err
		}
		if hdr.Name == "console.txt" {
			data, err := io.ReadAll(tr)
			return string(data), err
		}
	}
}
