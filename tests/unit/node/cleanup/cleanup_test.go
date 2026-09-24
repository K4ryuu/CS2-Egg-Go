// SPDX-License-Identifier: GPL-3.0-or-later

package cleanup_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	engine "github.com/K4ryuu/CS2-Egg-Go/internal/cleanup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/cleanup"
)

func TestDefaultRulesAreUsableAndRootRelative(t *testing.T) {
	rules := cleanup.Defaults().Rules
	if len(rules) < 5 {
		t.Fatalf("the egg template should carry the whole default set, got %d", len(rules))
	}
	for _, r := range rules {
		if r.Name == "" || len(r.Directories) == 0 || len(r.Patterns) == 0 {
			t.Fatalf("incomplete rule: %+v", r)
		}
		for _, d := range r.Directories {
			if _, ok := engine.Clean(d, nil); !ok {
				t.Fatalf("rule %s points outside the volume: %s", r.Name, d)
			}
		}
	}
	if err := cleanup.Defaults().Check(); err != nil {
		t.Fatalf("the shipped defaults must pass their own check: %v", err)
	}
}

func TestCheckRefusesEscapingRules(t *testing.T) {
	cfg := cleanup.Defaults()
	cfg.Rules = []engine.Rule{{Name: "bad", Directories: []string{"../../etc"}, Patterns: []string{"*"}}}
	if err := cfg.Check(); err == nil {
		t.Fatal("a rule climbing out of the volume must be refused")
	}
	// an older cleanup.json names the container root the long way round
	cfg.Rules = []engine.Rule{{Name: "legacy", Directories: []string{"/home/container"}, Patterns: []string{"core"}}}
	if err := cfg.Check(); err != nil {
		t.Fatalf("the container root itself is fine: %v", err)
	}
	cfg.EveryHours = -1
	if err := cfg.Check(); err == nil {
		t.Fatal("a negative interval must be refused")
	}
}

func TestStatsRoundTripAndFold(t *testing.T) {
	dir := t.TempDir()
	if got := cleanup.LoadStats(dir); got.Total.Files != 0 || got.Server == nil {
		t.Fatalf("a missing stats file must start empty and usable: %+v", got)
	}
	st := cleanup.LoadStats(dir)
	now := time.Now().Truncate(time.Second)
	for _, r := range []cleanup.Run{
		{Container: "srv-a", Time: now, Files: 3, Bytes: 300, PerRule: map[string]engine.RuleStat{"demos": {Files: 3, Bytes: 300}}},
		{Container: "srv-a", Time: now, Files: 1, Bytes: 100, Skipped: 2, PerRule: map[string]engine.RuleStat{"demos": {Files: 1, Bytes: 100}}},
		{Container: "srv-b", Time: now, Files: 2, Bytes: 50, PerRule: map[string]engine.RuleStat{"core_dumps": {Files: 2, Bytes: 50}}},
	} {
		cleanup.Fold(&st, r)
	}
	if st.Total.Files != 6 || st.Total.Bytes != 450 || st.Total.Runs != 3 {
		t.Fatalf("totals: %+v", st.Total)
	}
	if a := st.Server["srv-a"]; a.Files != 4 || a.Runs != 2 || a.PerRule["demos"].Files != 4 || a.Last.Skipped != 2 {
		t.Fatalf("per server: %+v", a)
	}
	if err := cleanup.SaveStats(dir, st); err != nil {
		t.Fatal(err)
	}
	back := cleanup.LoadStats(dir)
	if back.Total.Files != st.Total.Files || back.Total.Bytes != st.Total.Bytes || back.Server["srv-b"].Bytes != 50 {
		t.Fatalf("round trip lost data: %+v", back)
	}
	// a corrupt file must not take the module down with it
	os.WriteFile(filepath.Join(dir, "stats.json"), []byte("{not json"), 0o644)
	if got := cleanup.LoadStats(dir); got.Total.Files != 0 || got.Server == nil {
		t.Fatalf("a corrupt stats file must start empty: %+v", got)
	}
}

func TestStartPassIsDebounced(t *testing.T) {
	now := time.Now()
	if !cleanup.Due(time.Time{}, now) {
		t.Fatal("a server with no pass yet is always due")
	}
	// docker reconnects and the core repeats Start for everything running
	if cleanup.Due(now.Add(-time.Minute), now) {
		t.Fatal("a pass that just ran must not be repeated on a Start event")
	}
	if !cleanup.Due(now.Add(-cleanup.StartDebounce-time.Second), now) {
		t.Fatal("the debounce must expire")
	}
}
