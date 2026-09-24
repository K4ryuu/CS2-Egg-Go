// SPDX-License-Identifier: GPL-3.0-or-later

package workshop

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintStatus is the Workshop cache section of `cs2node status`.
func (m *Module) PrintStatus(w io.Writer, _ *core.Core, st core.Status) {
	var ws Status
	if json.Unmarshal(st.Module[m.Name()], &ws) != nil {
		return
	}
	ui.Section(w, "Workshop cache")
	ui.KV(w, "store", fmt.Sprintf("%d item(s), %s under %s", ws.Items, ui.Size(ws.Bytes), ws.Dir))
	if ws.Shared > 0 {
		ui.KV(w, "freed in volumes", ui.Green(ui.Size(ws.Shared))+ui.Gray("  since the daemon started"))
	}
	if ws.Stale > 0 {
		ui.KV(w, "updated upstream", ui.Yellow(fmt.Sprintf("%d item(s)", ws.Stale))+ui.Gray("  servers refetch them on their next boot"))
	}
	if !ws.Seed {
		ui.KV(w, "seeding", ui.Gray("off"))
	}
}
