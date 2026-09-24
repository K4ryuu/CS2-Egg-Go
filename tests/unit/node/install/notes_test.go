// SPDX-License-Identifier: GPL-3.0-or-later

package install_test

import (
	"strings"
	"testing"

	engine "github.com/K4ryuu/CS2-Egg-Go/internal/cleanup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/addoncache"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/cleanup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/guard"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/workshop"
)

// Every module the installer can switch on has to explain itself in a few
// plain lines: what changes on the servers, and what fights it.
func TestModuleNotes(t *testing.T) {
	cases := []struct {
		name string
		mod  any
		want []string
	}{
		{"workshop", workshop.New(nil, nil), []string{"next boot", "RedownloadAddonOnMount", "mm_addon_mount_download"}},
		{"guard", guard.New(nil), []string{"ENABLE_GUARD=1"}},
		{"addoncache", addoncache.New(nil, nil), []string{"cs2node pin"}},
		{"cleanup", cleanup.New(nil), []string{"cleanup.json", "modules.cleanup.rules"}},
	}
	for _, tc := range cases {
		noter, ok := tc.mod.(module.Noter)
		if !ok {
			t.Fatalf("%s has no installer notes", tc.name)
		}
		notes := noter.Notes()
		if len(notes) == 0 {
			t.Fatalf("%s: empty notes", tc.name)
		}
		joined := strings.Join(notes, "\n")
		for _, want := range tc.want {
			if !strings.Contains(joined, want) {
				t.Errorf("%s notes should mention %q:\n%s", tc.name, want, joined)
			}
		}
		for _, n := range notes {
			if len(n) > 220 {
				t.Errorf("%s: a note is too long to read at a glance (%d chars)", tc.name, len(n))
			}
		}
	}
}

// A module that seeds config keys has to produce something the operator can
// actually edit, not an empty map that leaves the file looking incomplete.
func TestCleanupSeedsItsRules(t *testing.T) {
	sd, ok := any(cleanup.New(nil)).(module.Seeder)
	if !ok {
		t.Fatal("the cleanup module must seed its rules into the config")
	}
	seed := sd.Seed()
	rules, ok := seed["rules"].([]engine.Rule)
	if !ok || len(rules) == 0 {
		t.Fatalf("seed carries no rules: %#v", seed)
	}
	for _, r := range rules {
		if r.Name == "" || len(r.Patterns) == 0 {
			t.Fatalf("seeded rule is incomplete: %+v", r)
		}
	}
}
