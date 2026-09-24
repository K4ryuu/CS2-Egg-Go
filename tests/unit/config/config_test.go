// SPDX-License-Identifier: GPL-3.0-or-later

package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/config"
)

const version = "1.2.3"

var template = []byte(`{
  "_description": ["template text"],
  "version": "0.0.0",
  "action": "log",
  "ban_minutes": 30,
  "nested": {"a": 1, "b": "x"},
  "plain_list": ["one", "two"],
  "rules": [
    {"name": "r1", "threshold": 3, "log": true},
    {"name": "r2", "threshold": 5, "log": true}
  ]
}`)

func load(t *testing.T, dir string, existing string) (map[string]any, config.Report) {
	t.Helper()
	path := filepath.Join(dir, "guard.json")
	if existing != "" {
		if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, rep, err := config.Load(path, template, version)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil || len(onDisk) == 0 {
		t.Fatalf("config file must exist and never be empty: %v", err)
	}
	var disk map[string]any
	if err := json.Unmarshal(onDisk, &disk); err != nil {
		t.Fatalf("file on disk is not json: %v", err)
	}
	if disk["version"] != version {
		t.Fatalf("version on disk = %v, want %s", disk["version"], version)
	}
	return m, rep
}

func TestMissingFileGetsTheTemplate(t *testing.T) {
	m, rep := load(t, t.TempDir(), "")
	if !rep.Created || m["action"] != "log" || m["ban_minutes"].(float64) != 30 {
		t.Fatalf("defaults not written: %+v %v", rep, m)
	}
	if _, ok := m["_description"]; !ok {
		t.Fatal("_description must be present")
	}
}

func TestSameVersionIsReadAsIs(t *testing.T) {
	dir := t.TempDir()
	m, rep := load(t, dir, `{"version":"1.2.3","action":"kick","ban_minutes":5,"rules":[]}`)
	if rep.Created || rep.Migrated || m["action"] != "kick" {
		t.Fatalf("same version must be used untouched: %+v %v", rep, m)
	}
}

func TestMigrationKeepsUserScalarsAndNestedValues(t *testing.T) {
	m, rep := load(t, t.TempDir(), `{"version":"1.0.0","action":"block","ban_minutes":0,"nested":{"a":9},"gone_key":true,"_description":["old"]}`)
	if !rep.Migrated {
		t.Fatal("must migrate")
	}
	if m["action"] != "block" || m["ban_minutes"].(float64) != 0 {
		t.Errorf("user scalars lost: %v", m)
	}
	n := m["nested"].(map[string]any)
	if n["a"].(float64) != 9 || n["b"] != "x" {
		t.Errorf("nested merge wrong: %v", n)
	}
	if _, ok := m["gone_key"]; ok {
		t.Error("keys removed from the template must drop")
	}
	if d := m["_description"].([]any); d[0] != "template text" {
		t.Error("_description must come from the template")
	}
}

func TestMigrationFalseAndNullRevertToDefault(t *testing.T) {
	m, _ := load(t, t.TempDir(), `{"version":"1.0.0","action":null,"ban_minutes":false}`)
	if m["action"] != "log" || m["ban_minutes"].(float64) != 30 {
		t.Fatalf("null/false must revert to the template: %v", m)
	}
}

func TestMigrationNamedArraysUserWinsAndNewEntriesAppend(t *testing.T) {
	m, _ := load(t, t.TempDir(), `{"version":"1.0.0","rules":[{"name":"r2","threshold":99},{"name":"custom","threshold":1}],"plain_list":["mine"]}`)
	rules := m["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("want user r2 + custom + appended r1, got %v", rules)
	}
	r2 := rules[0].(map[string]any)
	if r2["name"] != "r2" || r2["threshold"].(float64) != 99 {
		t.Errorf("user entry must win wholesale: %v", r2)
	}
	if _, hasLog := r2["log"]; hasLog {
		t.Error("new fields never reach an existing named entry (documented)")
	}
	if rules[2].(map[string]any)["name"] != "r1" {
		t.Errorf("template entry r1 must be appended last: %v", rules)
	}
	if pl := m["plain_list"].([]any); len(pl) != 1 || pl[0] != "mine" {
		t.Errorf("plain arrays: old wins wholesale: %v", pl)
	}
}

func TestCorruptFileGetsDefaultsAndAWarning(t *testing.T) {
	m, rep := load(t, t.TempDir(), `{not json`)
	if rep.Warning == "" || m["action"] != "log" {
		t.Fatalf("corrupt file must yield defaults + warning: %+v", rep)
	}
}
