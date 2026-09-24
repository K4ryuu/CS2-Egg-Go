// SPDX-License-Identifier: GPL-3.0-or-later

package alerts

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintStatus is the Alerts section of `cs2node status`.
func (m *Module) PrintStatus(w io.Writer, _ *core.Core, st core.Status) {
	var as status
	if json.Unmarshal(st.Module[m.Name()], &as) != nil {
		return
	}
	ui.Section(w, "Alerts")
	ui.KV(w, "events", strings.Join(as.Events, ", "))
	posted := fmt.Sprintf("%d posted", as.Sent)
	if as.Failed > 0 {
		posted += ui.Red(fmt.Sprintf(", %d failed", as.Failed))
	}
	ui.KV(w, "since start", posted)
}
