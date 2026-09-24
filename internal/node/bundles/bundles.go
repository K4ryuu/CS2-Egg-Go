// SPDX-License-Identifier: GPL-3.0-or-later

// Package bundles is the on-disk layout crash bundles and backups share:
// <dir>/<server>/<stamp>.tar.gz next to <stamp>.json with the metadata, so
// a listing never opens an archive.
package bundles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is one bundle as found on disk. Raw is the whole sidecar for the
// owner to decode.
type Entry struct {
	Server string
	Path   string // the tar.gz
	Time   time.Time
	Raw    json.RawMessage
}

// Stamp names a bundle after its time.
func Stamp(t time.Time) string { return t.Format("20060102-150405") }

// MetaPath is the sidecar of an archive.
func MetaPath(archive string) string { return strings.TrimSuffix(archive, ".tar.gz") + ".json" }

// List reads every sidecar under dir (one subdir per server), newest
// first; server filters to one when set.
func List(dir, server string) []Entry {
	var out []Entry
	subs, _ := os.ReadDir(dir)
	for _, s := range subs {
		// server may be a short id: what the tables print has to be typeable
		if !s.IsDir() || (server != "" && !strings.HasPrefix(s.Name(), server)) {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(dir, s.Name()))
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, s.Name(), f.Name()))
			if err != nil {
				continue
			}
			var head struct {
				Time time.Time `json:"time"`
				Path string    `json:"path"`
			}
			if json.Unmarshal(data, &head) != nil || head.Path == "" {
				continue
			}
			out = append(out, Entry{Server: s.Name(), Path: head.Path, Time: head.Time, Raw: data})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// Prune keeps the newest keepCount bundles per server and none older than
// keepDays (0 = unlimited for either). Returns how many went.
func Prune(dir string, keepCount, keepDays int, now time.Time) int {
	removed := 0
	seen := map[string]int{}
	for _, e := range List(dir, "") { // newest first
		i := seen[e.Server]
		seen[e.Server] = i + 1
		old := keepDays > 0 && now.Sub(e.Time) > time.Duration(keepDays)*24*time.Hour
		if (keepCount > 0 && i >= keepCount) || old {
			Remove(e.Path)
			removed++
		}
	}
	return removed
}

// Remove deletes an archive and its sidecar.
func Remove(archive string) {
	os.Remove(archive)
	os.Remove(MetaPath(archive))
}

// WriteMeta writes the sidecar for archive.
func WriteMeta(archive string, meta any) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(MetaPath(archive), data, 0o644)
}
