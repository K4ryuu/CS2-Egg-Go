// SPDX-License-Identifier: GPL-3.0-or-later

package cleanup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	engine "github.com/K4ryuu/CS2-Egg-Go/internal/cleanup"
)

// Run is one pass over one server.
type Run struct {
	Container string                     `json:"container"`
	Time      time.Time                  `json:"time"`
	Seconds   float64                    `json:"seconds"`
	Files     int                        `json:"files"`
	Bytes     int64                      `json:"bytes"`
	Skipped   int                        `json:"skipped,omitempty"`
	PerRule   map[string]engine.RuleStat `json:"per_rule,omitempty"`
}

// Stat is what one server has had removed, all passes together.
type Stat struct {
	Runs    int                        `json:"runs"`
	Files   int                        `json:"files"`
	Bytes   int64                      `json:"bytes"`
	LastRun time.Time                  `json:"last_run"`
	Last    Run                        `json:"last"`
	PerRule map[string]engine.RuleStat `json:"per_rule,omitempty"`
}

// Stats are the running totals, kept across daemon restarts.
type Stats struct {
	Since  time.Time       `json:"since"`
	Total  Stat            `json:"total"`
	Server map[string]Stat `json:"servers"`
}

// Fold adds one pass to the totals.
func Fold(s *Stats, r Run) {
	if s.Server == nil {
		s.Server = map[string]Stat{}
	}
	if s.Since.IsZero() {
		s.Since = r.Time
	}
	s.Total = add(s.Total, r)
	s.Server[r.Container] = add(s.Server[r.Container], r)
}

func add(st Stat, r Run) Stat {
	st.Runs++
	st.Files += r.Files
	st.Bytes += r.Bytes
	st.LastRun = r.Time
	st.Last = r
	if len(r.PerRule) > 0 && st.PerRule == nil {
		st.PerRule = map[string]engine.RuleStat{}
	}
	for name, v := range r.PerRule {
		c := st.PerRule[name]
		c.Files += v.Files
		c.Bytes += v.Bytes
		st.PerRule[name] = c
	}
	return st
}

func (s Stats) clone() Stats {
	out := Stats{Since: s.Since, Total: s.Total, Server: make(map[string]Stat, len(s.Server))}
	for k, v := range s.Server {
		out.Server[k] = v
	}
	return out
}

func statsPath(dir string) string { return filepath.Join(dir, "stats.json") }

// LoadStats reads the totals; a missing or unreadable file starts fresh,
// because losing a counter is not worth refusing to clean.
func LoadStats(dir string) Stats {
	s := Stats{Server: map[string]Stat{}}
	b, err := os.ReadFile(statsPath(dir))
	if err != nil {
		return s
	}
	if json.Unmarshal(b, &s) != nil || s.Server == nil {
		return Stats{Server: map[string]Stat{}}
	}
	return s
}

// SaveStats writes the totals atomically.
func SaveStats(dir string, s Stats) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := statsPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, statsPath(dir))
}
