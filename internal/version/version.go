// SPDX-License-Identifier: GPL-3.0-or-later

// Package version carries the build identity injected at link time and the
// version ordering every updater in the project relies on.
package version

import (
	"strconv"
	"strings"
)

// Set with -ldflags "-X github.com/K4ryuu/CS2-Egg-Go/internal/version.Version=..."
var (
	Version = "0.0.0-dev"
	Channel = "dev" // stable | beta | dev
	Commit  = ""
	Built   = "" // UTC build time, tells dev builds apart
)

// Copyright and License are the notice GPLv3 section 5 asks an interactive
// program to show. `cs2node version` prints them.
const (
	Copyright = `Copyright (C) 2024-2026 Kőrösfalvi "K4ryuu" Martin`
	License   = `License GPLv3+: GNU GPL version 3 or later <https://gnu.org/licenses/gpl.html>
This is free software: you are free to change and redistribute it.
There is NO WARRANTY, to the extent permitted by law.`
)

// Compare orders two version strings the way `sort -V` and semver agree on:
// a leading "v" is ignored, digit runs compare numerically, other runs
// lexically, and a "-prerelease" suffix sorts below the bare version.
// Returns -1, 0 or 1.
func Compare(a, b string) int {
	ca, pa := split(a)
	cb, pb := split(b)
	if c := compareChunks(ca, cb); c != 0 {
		return c
	}
	switch {
	case pa == "" && pb == "":
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}
	return compareChunks(pa, pb)
}

func split(v string) (core, pre string) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// compareChunks walks both strings as alternating digit / non-digit runs,
// dots only separate.
func compareChunks(a, b string) int {
	for {
		a, b = strings.TrimLeft(a, "."), strings.TrimLeft(b, ".")
		switch {
		case a == "" && b == "":
			return 0
		case a == "":
			return -1
		case b == "":
			return 1
		}
		var ta, tb string
		ta, a = chunk(a)
		tb, b = chunk(b)
		na, ea := strconv.Atoi(ta)
		nb, eb := strconv.Atoi(tb)
		c := strings.Compare(ta, tb)
		if ea == nil && eb == nil {
			c = cmpInt(na, nb)
		}
		if c != 0 {
			return c
		}
	}
}

func chunk(s string) (head, rest string) {
	digit := isDigit(s[0])
	i := 1
	for i < len(s) && s[i] != '.' && isDigit(s[i]) == digit {
		i++
	}
	return s[:i], s[i:]
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
