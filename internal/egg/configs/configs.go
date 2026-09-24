// SPDX-License-Identifier: GPL-3.0-or-later

// Package configs owns the egg's four JSON config files: their templates,
// their typed shape, and loading them with migration.
package configs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/K4ryuu/CS2-Egg-Go/internal/cleanup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/config"
)

// Filter is console-filter.json.
type Filter struct {
	PreviewMode bool     `json:"preview_mode"`
	Patterns    []string `json:"patterns"`
}

// Cleanup is cleanup.json. The rule shape lives in the shared engine so the
// egg and the node module configure the same thing.
type Cleanup struct {
	Rules []cleanup.Rule `json:"rules"`
}

// Logging is logging.json.
type Logging struct {
	Logging struct {
		ConsoleLevel string `json:"console_level"`
		Prefix       string `json:"prefix"`
		FileEnabled  bool   `json:"file_enabled"`
		MaxSizeMB    int    `json:"max_size_mb"`
		MaxFiles     int    `json:"max_files"`
		MaxDays      int    `json:"max_days"`
	} `json:"logging"`
}

// GuardRule is one entry of guard.json "behavior_rules".
type GuardRule struct {
	Name       string `json:"name"`
	Threshold  int    `json:"threshold"`
	WindowSecs int    `json:"window_secs"`
	BanMinutes int    `json:"ban_minutes"`
	Enabled    *bool  `json:"enabled"`
	Log        *bool  `json:"log"`
}

// IsEnabled defaults to true when the key is absent.
func (r GuardRule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// Logs defaults to true when the key is absent.
func (r GuardRule) Logs() bool { return r.Log == nil || *r.Log }

// Guard is guard.json.
type Guard struct {
	Action            string      `json:"action"`
	BanMinutes        int         `json:"ban_minutes"`
	CooldownSecs      int         `json:"cooldown_secs"`
	StuckGraceSecs    int         `json:"stuck_grace_secs"`
	WhitelistSteamIDs []string    `json:"whitelist_steamids"`
	WhitelistIPs      []string    `json:"whitelist_ips"`
	Rules             []GuardRule `json:"behavior_rules"`
}

// All is every config the egg reads, loaded together.
type All struct {
	Filter  Filter
	Cleanup Cleanup
	Logging Logging
	Guard   Guard
}

// Event describes what happened to one file during Load.
type Event struct {
	File   string
	Report config.Report
}

// Load reads (and creates or migrates) the four files under dir.
func Load(dir string) (All, []Event, error) {
	var all All
	var events []Event
	files := []struct {
		name     string
		template string
		into     any
	}{
		{"console-filter.json", filterTemplate, &all.Filter},
		{"cleanup.json", cleanupTemplate, &all.Cleanup},
		{"logging.json", loggingTemplate, &all.Logging},
		{"guard.json", guardTemplate, &all.Guard},
	}
	for _, f := range files {
		raw, rep, err := config.Load(filepath.Join(dir, f.name), []byte(f.template), SchemaVersion)
		if err != nil {
			return all, events, fmt.Errorf("%s: %w", f.name, err)
		}
		if err := json.Unmarshal(raw, f.into); err != nil {
			return all, events, fmt.Errorf("%s: %w", f.name, err)
		}
		events = append(events, Event{File: f.name, Report: rep})
	}
	return all, events, nil
}

// DefaultCleanupRules are the rules the template ships with. The node's
// cleanup module offers the same set, so the two sides never drift.
func DefaultCleanupRules() []cleanup.Rule {
	var c Cleanup
	if json.Unmarshal([]byte(cleanupTemplate), &c) != nil {
		return nil
	}
	return c.Rules
}

// MigratePrefix folds a pre-1.2.4 install's PREFIX_TEXT panel variable into
// logging.json once, since that variable is no longer read. Callers only
// call this when the file's own prefix is still untouched.
func MigratePrefix(dir, prefix string) error {
	path := filepath.Join(dir, "logging.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	logging, ok := m["logging"].(map[string]any)
	if !ok {
		return fmt.Errorf("logging.json: no logging section")
	}
	logging["prefix"] = prefix
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteAtomic(path, append(out, '\n'))
}
