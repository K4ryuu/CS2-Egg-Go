// SPDX-License-Identifier: GPL-3.0-or-later

package consolelog

import (
	"encoding/json"
	"io"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintStatus is the Console log section of `cs2node status`.
func (m *Module) PrintStatus(w io.Writer, _ *core.Core, st core.Status) {
	var last map[string]string
	if json.Unmarshal(st.Module[m.Name()], &last) != nil {
		return
	}
	ui.Section(w, "Console log")
	if len(last) == 0 {
		ui.Line(w, "%s", ui.Gray("(nothing saved yet)"))
	}
	for _, cont := range st.Containers {
		ts, ok := last[cont.Name]
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			continue
		}
		ui.KV(w, ui.ServerID(cont.Name), "last line "+ui.Yellow(ui.Ago(t)))
	}
}
