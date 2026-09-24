// SPDX-License-Identifier: GPL-3.0-or-later

package boot_test

import (
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/boot"
)

func TestMigratedPrefix(t *testing.T) {
	cases := []struct {
		name       string
		fromEnv    string
		filePrefix string
		want       string
	}{
		{"nothing to migrate, variable unset", "", "KitsuneLab", ""},
		{"variable already at the default", "KitsuneLab", "KitsuneLab", ""},
		{"file already customised, do not overwrite it", "MyServer", "AlreadyCustom", ""},
		{"migrates a real custom value", "MyServer", "KitsuneLab", "MyServer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := boot.MigratedPrefix(c.fromEnv, c.filePrefix); got != c.want {
				t.Fatalf("MigratedPrefix(%q, %q) = %q, want %q", c.fromEnv, c.filePrefix, got, c.want)
			}
		})
	}
}
