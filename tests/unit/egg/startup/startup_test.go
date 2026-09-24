// SPDX-License-Identifier: GPL-3.0-or-later

package startup_test

import (
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/startup"
)

func TestExpandReplacesPanelPlaceholders(t *testing.T) {
	env := map[string]string{"SERVER_PORT": "27015", "SRCDS_MAP": "de_dust2", "CUSTOM_PARAMS": "", "STEAM_ACC": "ABC"}
	lookup := func(k string) string { return env[k] }
	in := "./game/cs2.sh {{CUSTOM_PARAMS}} -dedicated +ip 0.0.0.0 -port {{SERVER_PORT}} +map {{ SRCDS_MAP }} +sv_setsteamaccount ${STEAM_ACC} {{UNKNOWN}}"
	want := "./game/cs2.sh  -dedicated +ip 0.0.0.0 -port 27015 +map de_dust2 +sv_setsteamaccount ABC "
	if got := startup.Expand(in, lookup); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestDebugger(t *testing.T) {
	if startup.Debugger("") != "" || startup.Debugger("0") != "" {
		t.Fatal("no port = no debugger")
	}
	if got := startup.Debugger("2345"); got != "gdbserver --no-disable-randomization :2345" {
		t.Fatalf("got %q", got)
	}
}
