// SPDX-License-Identifier: GPL-3.0-or-later

package boot_test

import (
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/boot"
)

func TestOverrideString(t *testing.T) {
	cases := []struct {
		name              string
		fromEnv, fallback string
		want              string
	}{
		{"variable unset, file wins", "", "INFO", "INFO"},
		{"variable set, it wins", "DEBUG", "INFO", "DEBUG"},
		{"both empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := boot.OverrideString(c.fromEnv, c.fallback); got != c.want {
				t.Fatalf("OverrideString(%q, %q) = %q, want %q", c.fromEnv, c.fallback, got, c.want)
			}
		})
	}
}

func TestOverrideInt(t *testing.T) {
	cases := []struct {
		name     string
		fromEnv  string
		fallback int
		want     int
	}{
		{"variable unset, file wins", "", 10, 10},
		{"variable set, it wins", "30", 10, 30},
		{"unparseable value, file wins", "not-a-number", 10, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := boot.OverrideInt(c.fromEnv, c.fallback); got != c.want {
				t.Fatalf("OverrideInt(%q, %d) = %d, want %d", c.fromEnv, c.fallback, got, c.want)
			}
		})
	}
}

func TestOverrideList(t *testing.T) {
	cases := []struct {
		name     string
		fromEnv  string
		fallback []string
		want     []string
	}{
		{"variable unset, file wins", "", []string{"a"}, []string{"a"}},
		{"variable blank, file wins", "   ", []string{"a"}, []string{"a"}},
		{"variable set, it wins, trimmed", "76561198000000000, 76561198000000001", []string{"a"}, []string{"76561198000000000", "76561198000000001"}},
		{"only commas and spaces, file wins", " , , ", []string{"a"}, []string{"a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := boot.OverrideList(c.fromEnv, c.fallback)
			if len(got) != len(c.want) {
				t.Fatalf("OverrideList(%q, %v) = %v, want %v", c.fromEnv, c.fallback, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("OverrideList(%q, %v) = %v, want %v", c.fromEnv, c.fallback, got, c.want)
				}
			}
		})
	}
}
