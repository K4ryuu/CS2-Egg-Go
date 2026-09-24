// SPDX-License-Identifier: GPL-3.0-or-later

// Package startup turns the panel's STARTUP template into the command the
// supervisor runs.
package startup

import (
	"regexp"
	"strings"
)

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}|\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Expand replaces {{VAR}} and ${VAR} with lookup(VAR). Unknown variables
// expand to nothing, as the shell did.
func Expand(template string, lookup func(string) string) string {
	return placeholder.ReplaceAllStringFunc(template, func(m string) string {
		sub := placeholder.FindStringSubmatch(m)
		name := sub[1]
		if name == "" {
			name = sub[2]
		}
		return lookup(name)
	})
}

// Debugger returns the GAME_DEBUGGER value for cs2.sh when a GDB port is set.
// gdbserver launches cs2 as its child, so no ptrace capability is needed.
func Debugger(port string) string {
	port = strings.TrimSpace(port)
	if port == "" || port == "0" {
		return ""
	}
	return "gdbserver --no-disable-randomization :" + port
}
