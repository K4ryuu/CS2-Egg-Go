// SPDX-License-Identifier: GPL-3.0-or-later

package cleanup

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintStatus is the Cleanup section of `cs2node status`.
func (m *Module) PrintStatus(w io.Writer, _ *core.Core, st core.Status) {
	var cs Status
	if json.Unmarshal(st.Module[m.Name()], &cs) != nil {
		return
	}
	ui.Section(w, "Cleanup")
	when := "on server start only"
	if cs.EveryHours > 0 {
		when = fmt.Sprintf("every %dh", cs.EveryHours)
		if !cs.NextRun.IsZero() {
			when += ui.Gray("  next " + cs.NextRun.Local().Format("15:04"))
		}
	}
	ui.KV(w, "passes", when+ui.Gray(fmt.Sprintf(", %d rule(s)", cs.Rules)))
	if t := cs.Stats.Total; t.Files > 0 {
		ui.KV(w, "removed", ui.Green(fmt.Sprintf("%d file(s), %s", t.Files, ui.Size(t.Bytes)))+
			ui.Gray(fmt.Sprintf("  in %d pass(es)", t.Runs)))
	}
	for _, cont := range st.Containers {
		v, ok := cs.Stats.Server[cont.Name]
		if !ok {
			ui.KV(w, ui.ServerID(cont.Name), ui.Gray("no pass yet"))
			continue
		}
		line := fmt.Sprintf("%s freed, last pass %s", ui.Size(v.Bytes), ui.Green(ui.Ago(v.LastRun)))
		if v.Last.Skipped > 0 {
			line += ui.Gray(fmt.Sprintf("  %d file(s) left open", v.Last.Skipped))
		}
		ui.KV(w, ui.ServerID(cont.Name), line)
	}
}
