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
	gameEntry   = regexp.MustCompile(`^\s*Game\s`)
	metamodLine = regexp.MustCompile(`Game.*csgo/addons/metamod`)
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

// metamodFirst moves the metamod entry directly under Game_LowViolence when
// another Game entry sits in between: Metamod must load before every addon.
func metamodFirst(content string) (string, bool) {
	if !metamodLine.MatchString(content) {
		return content, false
	}
	lines := strings.Split(content, "\n")
	lv, mm := -1, -1
	for i, l := range lines {
		if lv < 0 && lowViolence.MatchString(l) {
			lv = i
		}
		if mm < 0 && metamodLine.MatchString(l) {
			mm = i
		}
	}
	if lv < 0 || mm < 0 {
		return content, false
	}
	between := false
	for i := lv + 1; i < mm; i++ {
		if gameEntry.MatchString(lines[i]) {
			between = true
			break
		}
	}
	if !between {
		return content, false
	}
	var out []string
	for i, l := range lines {
		if metamodLine.MatchString(l) {
			continue
		}
		out = append(out, l)
		if i == lv {
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
