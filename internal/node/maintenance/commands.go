// SPDX-License-Identifier: GPL-3.0-or-later

package maintenance

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintStats is `cs2node maintenance`: the settings, the totals per task and
// what the last pass did or would have done.
func PrintStats(w io.Writer, cfg Config, st Stats) {
	ui.Headline(w, "cs2node Maintenance")
	mode := ui.Green("live")
	if cfg.DryRun {
		mode = ui.Yellow("dry run, nothing is deleted (dry_run in /etc/cs2node/config.json)")
	}
	ui.KV(w, "mode", mode)
	if cfg.EveryHours > 0 {
		ui.KV(w, "runs", fmt.Sprintf("every %dh", cfg.EveryHours))
	} else {
		ui.KV(w, "runs", "only on demand")
	}
	if st.Last.IsZero() {
		fmt.Fprintln(w)
		ui.Line(w, "%s", ui.Gray("(no pass yet; run one with: cs2node maintenance now)"))
		fmt.Fprintln(w)
		return
	}
	last := ui.Ago(st.Last)
	if st.Dry {
		last += ui.Gray("  dry run")
	}
	ui.KV(w, "last pass", last)
	if t := st.Total(); t > 0 {
		ui.KV(w, "freed", ui.Green(ui.Size(t))+ui.Gray("  since "+st.Since.Local().Format("2006-01-02")))
	}
	ui.Section(w, "Last pass")
	for _, l := range Summary(st.Done) {
		ui.Line(w, "%s", l)
	}
	if len(st.Task) > 0 {
		names := make([]string, 0, len(st.Task))
		for n := range st.Task {
			names = append(names, n)
		}
		sort.Strings(names)
		rows := make([][]string, 0, len(names))
		for _, n := range names {
			t := st.Task[n]
			rows = append(rows, []string{n, fmt.Sprint(t.Runs), fmt.Sprint(t.Items), ui.Size(t.Bytes)})
		}
		ui.Section(w, "Totals")
		ui.Table(w, []string{"task", "passes", "items", "freed"}, rows)
	}
	fmt.Fprintln(w)
}

// RunNow is `cs2node maintenance now [--apply]`: one pass, in-process. apply
// overrides dry_run for this pass only.
func RunNow(ctx context.Context, w io.Writer, m *Module, c *core.Core, apply bool) {
	dry := m.cfg.DryRun && !apply
	ui.Section(w, "cs2node maintenance")
	if dry {
		ui.Warn(w, "Dry run: reporting only (dry_run is on; --apply forces this pass)")
	}
	res := m.Pass(ctx, c, dry)
	for _, r := range res {
		line := Summary([]Result{r})[0]
		if r.Err != "" {
			ui.StepErr(w, "%s", line)
		} else {
			ui.StepOk(w, "%s", line)
		}
	}
	fmt.Fprintln(w)
}
