// SPDX-License-Identifier: GPL-3.0-or-later

package manual_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/metrics"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// TestShowFrame prints a frame with its measured widths, for eyeballing a
// layout change: SHOW_FRAME=1 go test -run ShowFrame -v ./tests/manual/
func TestShowFrame(t *testing.T) {
	if os.Getenv("SHOW_FRAME") == "" {
		t.Skip("set SHOW_FRAME=1 to print a sample frame")
	}
	for _, cols := range []int{103, 120} {
		fmt.Printf("=== cols=%d\n", cols)
		for i, l := range metrics.RenderForTest(cols, 12) {
			fmt.Printf("%2d w=%3d |%s|\n", i, ui.Visible(l), l)
		}
	}
}
