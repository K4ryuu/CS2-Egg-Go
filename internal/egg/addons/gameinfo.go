// SPDX-License-Identifier: GPL-3.0-or-later

package addons

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// GameinfoLine is the exact line the egg has always inserted (indentation
// matches Valve's own entries).
func GameinfoLine(addon string) string { return "            Game    " + addon }

var (
	lowViolence = regexp.MustCompile(`Game_LowViolence`)
	metamodLine = regexp.MustCompile(`Game.*csgo/addons/metamod`)
	backupsLine = regexp.MustCompile(`Game.*csgo/backups`)
	tokenless   = regexp.MustCompile(`(RequireLoginForDedicatedServers"\s*)"([0-9])"`)
)

// hasGamePath reports whether "Game <addon>" is already listed.
func hasGamePath(content, addon string) bool {
	return regexp.MustCompile(`Game[[:blank:]]*` + regexp.QuoteMeta(addon)).MatchString(content)
}

// addGamePath inserts the addon after the Game_LowViolence line.
func addGamePath(content, addon string) (string, bool) {
	if hasGamePath(content, addon) {
		return content, false
	}
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if lowViolence.MatchString(l) {
			lines = append(lines[:i+1], append([]string{GameinfoLine(addon)}, lines[i+1:]...)...)
			return strings.Join(lines, "\n"), true
		}
	}
	return content, false
}

// metamod needs to sit right after backups if backups exists, not the other
// way round - it's not an addon, just the engine's dump path, so don't treat
// it like a stray one or it gets shoved below metamod again next boot
func metamodFirst(content string) (string, bool) {
	if !metamodLine.MatchString(content) {
		return content, false
	}
	lines := strings.Split(content, "\n")
	lv, mm, bk := -1, -1, -1
	for i, l := range lines {
		if lv < 0 && lowViolence.MatchString(l) {
			lv = i
		}
		if mm < 0 && metamodLine.MatchString(l) {
			mm = i
		}
		if bk < 0 && backupsLine.MatchString(l) {
			bk = i
		}
	}
	if lv < 0 || mm < 0 {
		return content, false
	}
	anchor := lv
	if bk >= 0 {
		anchor = bk
	}
	if mm == anchor+1 {
		return content, false
	}
	var out []string
	for i, l := range lines {
		if i == mm {
			continue
		}
		out = append(out, l)
		if i == anchor {
			out = append(out, GameinfoLine("csgo/addons/metamod"))
		}
	}
	return strings.Join(out, "\n"), true
}

// tokenlessValue returns the current RequireLoginForDedicatedServers value.
func tokenlessValue(content string) string {
	m := tokenless.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return m[2]
}

func setTokenless(content, value string) string {
	return tokenless.ReplaceAllString(content, `${1}"`+value+`"`)
}

// Gameinfo edits game/csgo/gameinfo.gi with backup, verify and restore.
type Gameinfo struct{ Path string }

var errNoGameinfo = errors.New("gameinfo.gi not found")

func (g Gameinfo) edit(apply func(string) (string, bool), verify func(string) bool) (changed bool, err error) {
	data, err := os.ReadFile(g.Path)
	if err != nil {
		return false, errNoGameinfo
	}
	out, changed := apply(string(data))
	if !changed {
		return false, nil
	}
	if !verify(out) {
		return false, errors.New("verification failed, file untouched")
	}
	bak := g.Path + ".bak"
	if err := os.WriteFile(bak, data, 0o644); err != nil {
		return false, fmt.Errorf("backup: %w", err)
	}
	if err := os.WriteFile(g.Path, []byte(out), 0o644); err != nil {
		os.Rename(bak, g.Path)
		return false, err
	}
	os.Remove(bak)
	return true, nil
}

// Add lists addon under Game_LowViolence unless it is there already.
func (g Gameinfo) Add(addon string) (bool, error) {
	return g.edit(func(c string) (string, bool) { return addGamePath(c, addon) },
		func(c string) bool { return hasGamePath(c, addon) })
}

// MetamodFirst reorders the metamod entry when needed.
func (g Gameinfo) MetamodFirst() (bool, error) {
	return g.edit(metamodFirst, func(c string) bool { return metamodLine.MatchString(c) })
}

// SetTokenless sets RequireLoginForDedicatedServers: allow=true writes "0".
func (g Gameinfo) SetTokenless(allow bool) (bool, error) {
	want := "1"
	if allow {
		want = "0"
	}
	return g.edit(func(c string) (string, bool) {
		if tokenlessValue(c) == want {
			return c, false
		}
		return setTokenless(c, want), true
	}, func(c string) bool { return tokenlessValue(c) == want })
}

// HasSharp reports whether ModSharp is listed (used for the compat warning).
func (g Gameinfo) HasSharp() bool {
	data, err := os.ReadFile(g.Path)
	return err == nil && regexp.MustCompile(`Game[[:space:]]*sharp`).Match(data)
}
