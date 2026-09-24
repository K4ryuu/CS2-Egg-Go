// SPDX-License-Identifier: GPL-3.0-or-later

package addons

import (
	"os"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

// Versions is egg/versions.txt: one "Key=version" line per addon.
type Versions struct{ Path string }

// Get returns the recorded version or "".
func (v Versions) Get(key string) string {
	data, err := os.ReadFile(v.Path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+"="))
		}
	}
	return ""
}

// Set records a version, replacing the key's line or appending it.
func (v Versions) Set(key, val string) error {
	data, _ := os.ReadFile(v.Path)
	var out []string
	replaced := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, key+"=") {
			line = key + "=" + val
			replaced = true
		}
		out = append(out, line)
	}
	if !replaced {
		out = append(out, key+"="+val)
	}
	return os.WriteFile(v.Path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// Verdict of NeedsUpdate.
type Verdict int

const (
	Install   Verdict = iota // nothing recorded, or the release is newer
	UpToDate                 // same version
	Downgrade                // installed is newer than the release, keep it
)

// NeedsUpdate compares the release version with the recorded one.
func NeedsUpdate(current, latest string) Verdict {
	if current == "" {
		return Install
	}
	switch version.Compare(latest, current) {
	case 0:
		return UpToDate
	case -1:
		return Downgrade
	}
	return Install
}
