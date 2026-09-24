// SPDX-License-Identifier: GPL-3.0-or-later

package version_test

import (
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

func TestCompareOrdersLikeSortV(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.9", "1.0.10", -1},
		{"1.0.10", "1.0.9", 1},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3", "1.2.3", 0},
		{"1.0.335", "1.0.336", -1},
		{"git1391", "git1400", -1},
		{"git1400", "git1391", 1},
		{"1.0.0-beta.2", "1.0.0", -1},
		{"1.0.0", "1.0.0-beta.2", 1},
		{"1.0.0-beta.2", "1.0.0-beta.10", -1},
		{"1.0", "1.0.0", -1},
		{"2.0.0", "10.0.0", -1},
	}
	for _, c := range cases {
		if got := version.Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestDefaultsWhenNotBuiltWithLdflags(t *testing.T) {
	if version.Version == "" || version.Channel == "" {
		t.Fatalf("Version=%q Channel=%q must have defaults", version.Version, version.Channel)
	}
}
