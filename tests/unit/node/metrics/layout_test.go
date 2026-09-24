// SPDX-License-Identifier: GPL-3.0-or-later

package metrics_test

import (
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/metrics"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

func TestLayoutAdaptsToTheTerminalWidth(t *testing.T) {
	wide := metrics.LayoutFor(160)
	if !wide.Bars || wide.Tx == 0 || wide.Server < 36 {
		t.Fatalf("wide: %+v", wide)
	}
	mid := metrics.LayoutFor(100)
	if mid.Bars || mid.Tx == 0 || mid.Server < 20 {
		t.Fatalf("100 cols keeps every column, drops the bars: %+v", mid)
	}
	narrow := metrics.LayoutFor(80)
	if narrow.Tx != 0 || narrow.Mem == 0 || narrow.Server < 20 {
		t.Fatalf("80 cols drops tx first, keeps memory: %+v", narrow)
	}
	tiny := metrics.LayoutFor(50)
	if tiny.Mem != 0 || tiny.Server < 9 {
		t.Fatalf("50 cols keeps server, map, players, cpu, egg: %+v", tiny)
	}
	for _, cols := range []int{50, 80, 100, 160} {
		l := metrics.LayoutFor(cols)
		total, n := l.Server, 1
		for _, w := range []int{l.Map, l.Players, l.CPU, l.Mem, l.Rx, l.Tx, l.Egg} {
			if w > 0 {
				total += w
				n++
			}
		}
		if cols >= 60 && total+2*(n-1)+1 > cols {
			t.Errorf("%d cols: layout needs %d", cols, total+2*(n-1)+1)
		}
	}
}

func TestClipKeepsStylesAndCounts(t *testing.T) {
	styled := ui.Bold("abc") + "def"
	if got := ui.Clip(styled, 4); ui.Visible(got) != 4 || !strings.Contains(got, "abc") {
		t.Fatalf("clip: %q", got)
	}
	if got := ui.Clip("héllo", 3); got != "hél" {
		t.Fatalf("utf8 clip: %q", got)
	}
}

func TestFitAndBar(t *testing.T) {
	if got := ui.Fit("abcdefgh", 5, false); got != "abcd…" {
		t.Fatalf("cut: %q", got)
	}
	if got := ui.Fit("12", 5, true); got != "   12" {
		t.Fatalf("right: %q", got)
	}
	if ui.Visible(ui.Bar(10, 0.5)) != 12 {
		t.Fatalf("bar width: %q", ui.Bar(10, 0.5))
	}
	if ui.Short(1<<30+1<<29) != "1.5G" || ui.Short(700<<20) != "700M" {
		t.Fatal("short sizes")
	}
}

func TestRowsNeverExceedTheHeaderWidth(t *testing.T) {
	for _, cols := range []int{40, 50, 80, 100, 120, 160, 200} {
		for _, line := range metrics.RenderForTest(cols, 30) {
			if w := ui.Visible(line); w > cols-1 { // one cell of margin stays free
				t.Errorf("%d cols: a line is %d wide: %q", cols, w, line)
			}
		}
	}
}
