// SPDX-License-Identifier: GPL-3.0-or-later

// Package nodeconfig is /etc/cs2node/config.json: the node-wide settings
// plus one opaque JSON section per module.
package nodeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultPath is where the installer writes the config.
const DefaultPath = "/etc/cs2node/config.json"

// Config is the whole file.
type Config struct {
	Version int    `json:"version"`
	Channel string `json:"channel"` // stable | beta | dev
	// Images this node manages. A name without a tag matches every tag of it;
	// add the tag to match one. The bash egg's stable tags are deliberately
	// absent: this daemon only manages its own servers.
	Images []string `json:"images"`
	Update struct {
		Auto bool `json:"auto"`
	} `json:"update"`
	Modules map[string]json.RawMessage `json:"modules"`
}

// Default is a fresh config with no module enabled.
func Default() Config {
	c := Config{Version: 1, Channel: "stable", Images: []string{"ghcr.io/k4ryuu/cs2-egg-go", "sples1/k4ryuu-cs2:dev", "ghcr.io/k4ryuu/cs2-egg:dev"}, Modules: map[string]json.RawMessage{}}
	c.Update.Auto = true
	return c
}

// Load reads path. A missing file is an error the caller can test with
// os.ErrNotExist.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if c.Modules == nil {
		c.Modules = map[string]json.RawMessage{}
	}
	if len(c.Images) == 0 {
		return Config{}, errors.New("config lists no images")
	}
	return migrate(c), nil
}

// CurrentVersion of the config schema.
const CurrentVersion = 1

// migrate lifts an older file to the current schema in memory; the next
// Save writes it back. Add one step per version bump, oldest first.
func migrate(c Config) Config {
	if c.Version < 1 {
		c.Version = 1 // pre-versioned files had the same shape
	}
	return c
}

// Save writes path atomically with mode 0600 (the guard section may hold
// nothing secret today, the file still belongs to root alone).
func Save(path string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Section decodes one module's settings into v; a missing section leaves v
// at its defaults and returns false.
func (c Config) Section(name string, v any) (bool, error) {
	raw, ok := c.Modules[name]
	if !ok || len(raw) == 0 {
		return false, nil
	}
	return true, json.Unmarshal(raw, v)
}

// SetSection stores v as the module's settings.
func (c *Config) SetSection(name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if c.Modules == nil {
		c.Modules = map[string]json.RawMessage{}
	}
	c.Modules[name] = raw
	return nil
}

// Enabled reports the module section's "enabled" flag.
func (c Config) Enabled(name string) bool {
	var s struct {
		Enabled bool `json:"enabled"`
	}
	ok, err := c.Section(name, &s)
	return ok && err == nil && s.Enabled
}
