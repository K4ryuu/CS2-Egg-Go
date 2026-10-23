// SPDX-License-Identifier: GPL-3.0-or-later

package maintenance

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// StateDir holds the running totals across daemon restarts.
var StateDir = "/var/lib/cs2node/maintenance"

// TaskStat is what one task has freed over all real passes.
type TaskStat struct {
	Runs  int   `json:"runs"`
	Items int   `json:"items"`
	Bytes int64 `json:"bytes"`
}

// Stats are the totals plus the last pass, dry or not.
type Stats struct {
	Since time.Time           `json:"since"`
	Task  map[string]TaskStat `json:"tasks"`
	Last  time.Time           `json:"last"`
	Dry   bool                `json:"last_dry"`
	Done  []Result            `json:"last_results,omitempty"`
}

// Fold records one pass. Dry runs only replace the last-pass view, they never
// touch the totals.
func Fold(s *Stats, at time.Time, res []Result, dry bool) {
	if s.Task == nil {
		s.Task = map[string]TaskStat{}
	}
	s.Last, s.Dry, s.Done = at, dry, res
	if dry {
		return
	}
	if s.Since.IsZero() {
		s.Since = at
	}
	for _, r := range res {
		if r.Err != "" {
			continue
		}
		t := s.Task[r.Task]
		t.Runs++
		t.Items += r.Items
		t.Bytes += r.Bytes
		s.Task[r.Task] = t
	}
}

// Total sums every task's freed bytes.
func (s Stats) Total() (b int64) {
	for _, t := range s.Task {
		b += t.Bytes
	}
	return b
}

// LoadStats reads the totals; a missing or unreadable file is a fresh start.
func LoadStats(dir string) Stats {
	var s Stats
	if data, err := os.ReadFile(filepath.Join(dir, "stats.json")); err == nil {
		_ = json.Unmarshal(data, &s) // a corrupt file only costs the history
	}
	if s.Task == nil {
		s.Task = map[string]TaskStat{}
	}
	return s
}

// SaveStats writes the totals atomically.
func SaveStats(dir string, s Stats) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "stats.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "stats.json"))
}

// Status is the maintenance section of the control snapshot.
type Status struct {
	Stats      Stats     `json:"stats"`
	NextRun    time.Time `json:"next_run"`
	EveryHours int       `json:"every_hours"`
	DryRun     bool      `json:"dry_run"`
}

// PrintStatus is the Maintenance section of `cs2node status`.
func (m *Module) PrintStatus(w io.Writer, _ *core.Core, st core.Status) {
	var ms Status
	if json.Unmarshal(st.Module[m.Name()], &ms) != nil {
		return
	}
	ui.Section(w, "Maintenance")
	mode := ui.Green("live")
	if ms.DryRun {
		mode = ui.Yellow("dry run, nothing is deleted")
	}
	when := "only on demand"
	if ms.EveryHours > 0 {
		when = fmt.Sprintf("every %dh", ms.EveryHours)
		if !ms.NextRun.IsZero() {
			when += ui.Gray("  next " + ms.NextRun.Local().Format("Jan 2 15:04"))
		}
	}
	ui.KV(w, "passes", when+"  "+mode)
	if t := ms.Stats.Total(); t > 0 {
		ui.KV(w, "freed", ui.Green(ui.Size(t)))
	}
	if !ms.Stats.Last.IsZero() {
		ui.KV(w, "last pass", ui.Ago(ms.Stats.Last))
	}
}
