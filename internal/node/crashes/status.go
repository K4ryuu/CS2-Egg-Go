// SPDX-License-Identifier: GPL-3.0-or-later

package crashes

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintStatus is the Crashes section of `cs2node status`.
func (m *Module) PrintStatus(w io.Writer, _ *core.Core, st core.Status) {
	var last map[string]Meta
	if json.Unmarshal(st.Module[m.Name()], &last) != nil {
		return
	}
	ui.Section(w, "Crashes")
	if len(last) == 0 {
		ui.Line(w, "%s", ui.Gray("(no crash bundle yet)"))
	}
	for _, cont := range st.Containers {
		if c, ok := last[cont.Name]; ok {
			ui.KV(w, ui.ServerID(cont.Name), fmt.Sprintf("last %s, exit %s on %s",
				ui.Yellow(c.Time.Local().Format("2006-01-02 15:04")), ui.Red(fmt.Sprint(c.ExitCode)), c.Map))
		}
	}
}
