// SPDX-License-Identifier: GPL-3.0-or-later

package steamcmd_test

import (
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/steamcmd"
)

func TestArgsMatchTheEggCommandLine(t *testing.T) {
	args, shown := steamcmd.Options{AppID: "730"}.Args("/home/container")
	if strings.Join(args, " ") != "+login anonymous +force_install_dir /home/container +app_update 730 +quit" {
		t.Fatalf("anonymous: %v", args)
	}
	if shown != strings.Join(args, " ") {
		t.Fatalf("display must equal args without a password: %q", shown)
	}
	args, shown = steamcmd.Options{AppID: "730", Login: "me", Password: "hunter2", BetaID: "beta", BetaPass: "bp", Validate: true}.Args("/x")
	want := "+login me hunter2 +force_install_dir /x +app_update 730 -beta beta -betapassword bp validate +quit"
	if strings.Join(args, " ") != want {
		t.Fatalf("full: %v", args)
	}
	if strings.Contains(shown, "hunter2") || !strings.Contains(shown, "+login me ****") {
		t.Fatalf("password must be masked in the display string: %q", shown)
	}
}
