// SPDX-License-Identifier: GPL-3.0-or-later

package eggvariants_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/eggvariants"
)

const fixture = `{
  "_comment": "DO NOT EDIT",
  "meta": {"version": "PTDL_v2", "update_url": "https://example.com/with-node.json"},
  "exported_at": "2026-01-01T00:00:00+00:00",
  "name": "Test Egg",
  "author": "a@b.c",
  "description": "desc",
  "features": [],
  "docker_images": {"x": "y"},
  "file_denylist": [],
  "startup": "./run.sh",
  "config": {"files": "{}", "startup": "{}", "logs": "{}", "stop": "quit"},
  "scripts": {"installation": {"script": "true", "container": "debian", "entrypoint": "bash"}},
  "variables": [
    {"name": "A", "description": "", "env_variable": "STANDALONE_VAR", "default_value": "0", "user_viewable": true, "user_editable": true, "rules": "required|boolean", "field_type": "text"},
    {"name": "B", "description": "", "env_variable": "NODE_ONLY_VAR", "default_value": "0", "user_viewable": true, "user_editable": true, "rules": "required|boolean", "field_type": "text"}
  ],
  "uuid": "test-uuid"
}`

func TestStandaloneDropsOnlyNodeOnlyVariables(t *testing.T) {
	orig := eggvariants.NodeOnly
	eggvariants.NodeOnly = map[string]bool{"NODE_ONLY_VAR": true}
	defer func() { eggvariants.NodeOnly = orig }()

	out, err := eggvariants.Standalone([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	vars, _ := got["variables"].([]any)
	if len(vars) != 1 {
		t.Fatalf("want 1 variable left, got %d: %v", len(vars), vars)
	}
	env := vars[0].(map[string]any)["env_variable"]
	if env != "STANDALONE_VAR" {
		t.Fatalf("wrong variable survived: %v", env)
	}
	if got["name"] != "Test Egg"+eggvariants.NameSuffix {
		t.Fatalf("name suffix missing: %v", got["name"])
	}
	if got["uuid"] != eggvariants.StandaloneUUID {
		t.Fatalf("uuid must be the fixed standalone one, so re-imports update in place: %v", got["uuid"])
	}
	gotMeta := got["meta"].(map[string]any)
	if gotMeta["update_url"] != eggvariants.StandaloneUpdateURL {
		t.Fatalf("update_url must point at the standalone export, not the with-node one: %v", gotMeta["update_url"])
	}
	if gotMeta["version"] != "PTDL_v2" {
		t.Fatalf("meta.version must survive untouched: %v", gotMeta["version"])
	}
}

func TestStandaloneLeavesEverythingElseUntouched(t *testing.T) {
	out, err := eggvariants.Standalone([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	var src, got map[string]any
	json.Unmarshal([]byte(fixture), &src)
	json.Unmarshal(out, &got)
	// meta and uuid deliberately change, checked in TestStandaloneDropsOnlyNodeOnlyVariables
	for _, k := range []string{"_comment", "exported_at", "author", "description", "features", "docker_images", "file_denylist", "startup", "config", "scripts"} {
		gv, err1 := json.Marshal(got[k])
		sv, err2 := json.Marshal(src[k])
		if err1 != nil || err2 != nil || string(gv) != string(sv) {
			t.Fatalf("%s changed: got %s want %s", k, gv, sv)
		}
	}
}

// The real production file must actually parse and round-trip: this is
// what `go generate ./cmd/eggvariants` runs against.
func TestStandaloneAgainstTheRealEggFile(t *testing.T) {
	root := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "pterodactyl", "kitsunelab-cs2-go-egg.json"))
	if err != nil {
		t.Skip("egg JSON not found from this working directory")
	}
	out, err := eggvariants.Standalone(data)
	if err != nil {
		t.Fatal(err)
	}
	var got, src map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	json.Unmarshal(data, &src)
	srcVars, _ := src["variables"].([]any)
	gotVars, _ := got["variables"].([]any)
	if len(gotVars) >= len(srcVars) {
		t.Fatalf("expected at least one variable dropped: %d -> %d", len(srcVars), len(gotVars))
	}
	for _, v := range gotVars {
		env, _ := v.(map[string]any)["env_variable"].(string)
		if eggvariants.NodeOnly[env] {
			t.Fatalf("%s is NodeOnly and must not be in the standalone export", env)
		}
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found in any parent directory")
		}
		dir = parent
	}
}
