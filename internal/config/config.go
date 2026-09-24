// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads the egg's JSON config files and migrates them across
// schema versions with the same rules the bash egg used: user values win,
// named rule arrays merge by name, template-only keys appear, dropped keys go.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Report says what Load did to the file.
type Report struct {
	Created  bool
	Migrated bool
	Warning  string // set when the old file could not be merged
}

// Load returns the effective config as JSON. template is the full default
// document (with "_description"); version is the current schema version.
// Missing file: template written. Same version: file used as is. Different
// version: user values merged into the template and written back atomically.
func Load(path string, template []byte, version string) (json.RawMessage, Report, error) {
	var rep Report
	fresh, err := withVersion(template, version)
	if err != nil {
		return nil, rep, fmt.Errorf("template: %w", err)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		rep.Created = true
		return fresh, rep, WriteAtomic(path, fresh)
	}
	if err != nil {
		return nil, rep, err
	}
	var old map[string]any
	if err := json.Unmarshal(data, &old); err != nil {
		rep.Warning = fmt.Sprintf("Config merge failed for %s - keeping new defaults", filepath.Base(path))
		return fresh, rep, WriteAtomic(path, fresh)
	}
	if v, _ := old["version"].(string); v == version {
		return json.RawMessage(data), rep, nil
	}
	rep.Migrated = true
	var tpl map[string]any
	if err := json.Unmarshal(fresh, &tpl); err != nil {
		return nil, rep, err
	}
	delete(old, "_description")
	delete(old, "version")
	merged := merge(tpl, old).(map[string]any)
	merged["version"] = version
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, rep, err
	}
	out = append(out, '\n')
	return out, rep, WriteAtomic(path, out)
}

// merge walks the template; old supplies values where it has any.
func merge(tpl, old any) any {
	t, ok := tpl.(map[string]any)
	if !ok {
		return or(old, tpl)
	}
	o, _ := old.(map[string]any)
	out := make(map[string]any, len(t))
	for k, tv := range t {
		ov := o[k]
		switch tvt := tv.(type) {
		case map[string]any:
			out[k] = merge(tvt, ov)
		case []any:
			if oa, isArr := ov.([]any); isArr && len(tvt) > 0 && allNamed(tvt) {
				out[k] = mergeNamed(oa, tvt)
			} else {
				out[k] = or(ov, tv)
			}
		default:
			out[k] = or(ov, tv)
		}
	}
	return out
}

// or is jq's `old // new`: old unless it is null or false.
func or(old, def any) any {
	if old == nil || old == false {
		return def
	}
	return old
}

func allNamed(arr []any) bool {
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			return false
		}
		if _, has := m["name"]; !has {
			return false
		}
	}
	return true
}

// mergeNamed keeps the user's entries whole and appends template entries the
// user does not have yet, so new default rules reach existing installs.
func mergeNamed(old, tpl []any) []any {
	have := map[any]bool{}
	for _, e := range old {
		if m, ok := e.(map[string]any); ok {
			have[m["name"]] = true
		}
	}
	out := append([]any{}, old...)
	for _, e := range tpl {
		if !have[e.(map[string]any)["name"]] {
			out = append(out, e)
		}
	}
	return out
}

func withVersion(template []byte, version string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(template, &m); err != nil {
		return nil, err
	}
	m["version"] = version
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// WriteAtomic never leaves a partial or empty file behind.
func WriteAtomic(path string, data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("refusing to write an empty config")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	os.Chmod(tmp.Name(), 0o644)
	return os.Rename(tmp.Name(), path)
}
