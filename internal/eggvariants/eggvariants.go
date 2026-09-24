// SPDX-License-Identifier: GPL-3.0-or-later

// Package eggvariants derives the standalone (no node daemon) Pterodactyl
// export from the canonical, hand-maintained egg JSON: the same egg, minus
// the Variables that do nothing without the node daemon.
package eggvariants

import "encoding/json"

// NodeOnly are the env_variable names that need a node daemon module to do
// anything at all; without one they are a silent no-op on the standalone
// egg, so Standalone drops them rather than ship a switch that does
// nothing. Add a name here when a new Variable gets that kind of hard
// dependency, not for one the node daemon merely enhances (guard and
// cleanup both work fully without a node, so neither belongs here).
var NodeOnly = map[string]bool{
	"ENABLE_LOG_FILES": true, // needs the node's consolelog module
}

// NameSuffix is appended to the egg's display name so the standalone
// export never looks identical to the with-node one in a panel's egg
// picker.
const NameSuffix = " (standalone, no node daemon)"

// StandaloneUUID replaces the source egg's own uuid. Pterodactyl matches an
// import to an existing egg by uuid to decide whether to update it in
// place or create a new one; sharing the with-node egg's uuid would make
// importing both into the same panel collide instead of coexist. Fixed, so
// re-running the generator updates the same standalone egg rather than
// minting a new one each time.
const StandaloneUUID = "97bb2e16-abbe-446e-aa4c-715be51f2d70"

// StandaloneUpdateURL replaces meta.update_url so a panel checking this egg
// for updates re-fetches the standalone export, not the with-node one.
const StandaloneUpdateURL = "https://raw.githubusercontent.com/K4ryuu/CS2-Egg-Go/main/pterodactyl/kitsunelab-cs2-go-egg-standalone.json"

// meta is meta.update_url, the one field inside "meta" this package edits.
type meta struct {
	Version   string `json:"version"`
	UpdateURL string `json:"update_url"`
}

// egg mirrors the export's top-level shape so marshaling keeps the file's
// existing key order. Blocks this package never changes stay untouched
// bytes via json.RawMessage.
type egg struct {
	Comment      string          `json:"_comment"`
	Meta         meta            `json:"meta"`
	ExportedAt   string          `json:"exported_at"`
	Name         string          `json:"name"`
	Author       string          `json:"author"`
	Description  string          `json:"description"`
	Features     json.RawMessage `json:"features"`
	DockerImages json.RawMessage `json:"docker_images"`
	FileDenylist json.RawMessage `json:"file_denylist"`
	Startup      string          `json:"startup"`
	Config       json.RawMessage `json:"config"`
	Scripts      json.RawMessage `json:"scripts"`
	Variables    []Variable      `json:"variables"`
	UUID         string          `json:"uuid"`
}

// Variable is one Pterodactyl egg Variable, field order matching the
// export's own convention.
type Variable struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	EnvVariable  string `json:"env_variable"`
	DefaultValue string `json:"default_value"`
	UserViewable bool   `json:"user_viewable"`
	UserEditable bool   `json:"user_editable"`
	Rules        string `json:"rules"`
	FieldType    string `json:"field_type"`
}

// Standalone reads src (a full, with-node egg export) and returns the
// standalone variant: NodeOnly variables dropped, everything else
// byte-identical, the name carrying NameSuffix.
func Standalone(src []byte) ([]byte, error) {
	var e egg
	if err := json.Unmarshal(src, &e); err != nil {
		return nil, err
	}
	kept := make([]Variable, 0, len(e.Variables))
	for _, v := range e.Variables {
		if !NodeOnly[v.EnvVariable] {
			kept = append(kept, v)
		}
	}
	e.Variables = kept
	e.Name += NameSuffix
	e.UUID = StandaloneUUID
	e.Meta.UpdateURL = StandaloneUpdateURL

	var buf []byte
	enc := json.NewEncoder(sliceWriter{&buf})
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "    ")
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return buf, nil
}

// sliceWriter lets json.Encoder (which wants an io.Writer) fill a []byte,
// so SetEscapeHTML and SetIndent both apply, which json.MarshalIndent's
// shortcut does not offer together with HTML escaping off.
type sliceWriter struct{ buf *[]byte }

func (w sliceWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}
