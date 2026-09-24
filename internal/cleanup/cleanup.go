// SPDX-License-Identifier: GPL-3.0-or-later

// Package cleanup deletes stale server files by rule: directories, basename
// globs, age, and optionally whole per-crash bundle directories. Every path
// is resolved inside an os.Root, so a symlink planted in a server volume
// cannot steer a delete out of it.
package cleanup

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
)

// Rule is one cleanup target. It is also the JSON shape of an entry in the
// egg's cleanup.json "rules" and of the node module's "rules".
type Rule struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	Directories     []string `json:"directories"` // root-relative
	Patterns        []string `json:"patterns"`    // basename globs
	Hours           int      `json:"hours"`       // older than this; 0 = every run
	Recursive       *bool    `json:"recursive"`
	DeleteParentDir bool     `json:"delete_parent_dir,omitempty"`
	Enabled         *bool    `json:"enabled"`
}

// IsRecursive defaults to true when the key is absent.
func (r Rule) IsRecursive() bool { return r.Recursive == nil || *r.Recursive }

// IsEnabled defaults to true when the key is absent.
func (r Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// RuleStat is what one rule removed.
type RuleStat struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Result is what one run removed.
type Result struct {
	Files   int                 `json:"files"`
	Bytes   int64               `json:"bytes"`
	PerRule map[string]RuleStat `json:"per_rule,omitempty"`
	Errors  []string            `json:"-"`
	Skipped int                 `json:"skipped,omitempty"` // held open by the server
}

// Options tune one run.
type Options struct {
	// Now is the clock the age cutoff is measured from.
	Now time.Time
	// Bases are absolute paths that mean the root itself. A rule written
	// against one of them (older configs say "/home/container") is rewritten
	// to a relative path instead of being refused.
	Bases []string
	// Skip, when set, is asked about every root-relative candidate path. A
	// true answer leaves the file alone. The node uses it to keep files the
	// running server still holds open, because unlinking those frees no
	// disk until the server restarts.
	Skip func(rel string) bool
}

// Run applies every enabled rule inside root. Symlinks are never followed
// and never deleted as if they were their target.
func Run(root *os.Root, rules []Rule, opt Options) Result {
	res := Result{PerRule: map[string]RuleStat{}}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	fsys := root.FS()
	for _, r := range rules {
		if r.Name == "" || !r.IsEnabled() || len(r.Directories) == 0 || len(r.Patterns) == 0 {
			continue
		}
		for _, raw := range r.Directories {
			dir, ok := Clean(raw, opt.Bases)
			if !ok {
				res.Errors = append(res.Errors, "Rule "+r.Name+": path outside the server directory: "+raw)
				continue
			}
			if st, err := fs.Stat(fsys, dir); err != nil || !st.IsDir() {
				continue
			}
			for _, f := range matches(fsys, dir, r, opt) {
				if opt.Skip != nil && opt.Skip(f) {
					res.Skipped++
					continue
				}
				if r.DeleteParentDir {
					res.deleteBundle(root, fsys, f, dir, r.Name)
				} else {
					res.deleteFile(root, f, r.Name)
				}
			}
		}
	}
	return res
}

// Clean turns a configured directory into a root-relative path. An absolute
// path, or one that climbs out with "..", is refused unless it sits under
// one of bases, which name the root itself.
func Clean(dir string, bases []string) (string, bool) {
	d := strings.TrimSpace(dir)
	d = strings.TrimPrefix(d, "./")
	d = path.Clean(strings.ReplaceAll(d, `\`, "/"))
	if path.IsAbs(d) {
		for _, b := range bases {
			b = path.Clean(b)
			switch {
			case d == b:
				return ".", true
			case strings.HasPrefix(d, b+"/"):
				return strings.TrimPrefix(d, b+"/"), true
			}
		}
		return "", false
	}
	switch {
	case d == "." || d == "":
		return ".", true
	case d == "..", strings.HasPrefix(d, "../"):
		return "", false
	}
	return d, true
}

func matches(fsys fs.FS, dir string, r Rule, opt Options) []string {
	var out []string
	cutoff := opt.Now.Add(-time.Duration(r.Hours) * time.Hour)
	fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && !r.IsRecursive() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !matchAny(path.Base(p), r.Patterns) {
			return nil
		}
		if r.Hours > 0 {
			info, err := d.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				return nil
			}
		}
		out = append(out, p)
		return nil
	})
	return out
}

func matchAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

func (res *Result) deleteFile(root *os.Root, rel, rule string) {
	info, err := root.Lstat(rel)
	if err != nil {
		return
	}
	if err := root.Remove(rel); err != nil {
		res.Errors = append(res.Errors, "Failed to delete: "+rel)
		return
	}
	res.add(rule, 1, info.Size())
}

// deleteBundle removes the matched file's parent directory, but never the
// rule's root and never a directory that still holds other directories.
func (res *Result) deleteBundle(root *os.Root, fsys fs.FS, rel, ruleRoot, rule string) {
	parent := path.Dir(rel)
	if parent == ruleRoot || hasSubdir(fsys, parent) {
		res.deleteFile(root, rel, rule)
		return
	}
	files, bytes := tally(fsys, parent)
	if err := root.RemoveAll(parent); err != nil {
		res.Errors = append(res.Errors, "Failed to delete: "+parent)
		return
	}
	res.add(rule, files, bytes)
}

func (res *Result) add(rule string, files int, bytes int64) {
	res.Files += files
	res.Bytes += bytes
	s := res.PerRule[rule]
	s.Files += files
	s.Bytes += bytes
	res.PerRule[rule] = s
}

func hasSubdir(fsys fs.FS, dir string) bool {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return true // unreadable: play safe, single-file delete
	}
	for _, e := range entries {
		if e.IsDir() {
			return true
		}
	}
	return false
}

func tally(fsys fs.FS, dir string) (files int, bytes int64) {
	fs.WalkDir(fsys, dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				files++
				bytes += info.Size()
			}
		}
		return nil
	})
	return
}

// FormatSize renders bytes the way the console always did.
func FormatSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2f KB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
