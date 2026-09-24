// SPDX-License-Identifier: GPL-3.0-or-later

package metrics

import (
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
)

// RenderForTest draws one frame with two made-up servers, so a test can
// measure every line against the terminal width.
func RenderForTest(cols, lines int) []string {
	rows := []row{
		{cont: core.Container{Name: "59e9667c-9616-4ba9-be7e-3deca6c8806c"}, st: core.Stats{CPUPercent: 84.2, MemBytes: 1900 << 20, MemLimit: 4 << 30, RxBytes: 1}, rx: 210 << 10, tx: 1.4 * (1 << 20)},
		{cont: core.Container{Name: "7c1d0a2e-3f44-4b7a-9d21-0e5c8f6a1b2c"}, st: core.Stats{CPUPercent: 3.1, MemBytes: 1100 << 20, MemLimit: 4 << 30}},
	}
	known := map[string]core.ContainerStatus{
		rows[0].cont.Name: {Name: rows[0].cont.Name, Connected: true, Reported: true, Map: "de_dust2", Server: "KitsuneLab | Retake #1", Players: 12, Up: true},
		rows[1].cont.Name: {Name: rows[1].cont.Name, Connected: true}, // connected, never reported
	}
	offset := 0
	return render(cols, lines, rows, known, "cpu", &offset, 42, time.Second, true)
}
