// SPDX-License-Identifier: GPL-3.0-or-later

package addoncache

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintStatus is the Addon cache section of `cs2node status`.
func (m *Module) PrintStatus(w io.Writer, c *core.Core, st core.Status) {
	var as status
	if json.Unmarshal(st.Module[m.Name()], &as) != nil {
		return
	}
	ui.Section(w, "Addon cache")
	ui.KV(w, "cached", fmt.Sprintf("%d file(s), %.0f MiB, %d release answer(s)", as.Files, float64(as.Bytes)/(1<<20), as.Releases))
	var cfg Config
	if c.Config(m.Name(), &cfg) != nil {
		return
	}
	pins := []string{}
	for _, a := range Addons {
		if v := cfg.Pin("", RepoOf(a)); v != "" {
			pins = append(pins, a+" "+ui.Green(v))
		}
	}
	switch {
	case !cfg.PinVersions:
		ui.KV(w, "version lock", ui.Gray("off, every server follows the newest"))
	case len(pins) == 0 && len(cfg.PinPerServer) == 0:
		ui.KV(w, "version lock", ui.Yellow("on, but nothing pinned"))
	default:
		ui.KV(w, "version lock", strings.Join(pins, ", "))
	}
	if n := len(cfg.PinPerServer); n > 0 {
		ui.KV(w, "per-server pins", fmt.Sprintf("%d server(s)", n)+ui.Gray("  cs2node pins"))
	}
}
