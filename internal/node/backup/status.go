// SPDX-License-Identifier: GPL-3.0-or-later

package backup

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintStatus is the Backups section of `cs2node status`.
func (m *Module) PrintStatus(w io.Writer, _ *core.Core, st core.Status) {
	var bs status
	if json.Unmarshal(st.Module[m.Name()], &bs) != nil {
		return
	}
	ui.Section(w, "Backups")
	next := bs.NextRun.Local().Format("2006-01-02 15:04")
	if bs.Running != "" {
		next = ui.Yellow("running now: " + bs.Running)
	}
	ui.KV(w, "next run", next)
	for _, cont := range st.Containers {
		if meta, ok := bs.Last[cont.Name]; ok {
			ui.KV(w, ui.ServerID(cont.Name), fmt.Sprintf("last %s, %d files, %.0f MiB",
				ui.Green(meta.Time.Local().Format("2006-01-02 15:04")), meta.Files, float64(meta.Bytes)/(1<<20)))
		} else {
			ui.KV(w, ui.ServerID(cont.Name), ui.Yellow("no backup yet"))
		}
	}
}
