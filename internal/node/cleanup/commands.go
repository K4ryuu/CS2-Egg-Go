// SPDX-License-Identifier: GPL-3.0-or-later

package cleanup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	engine "github.com/K4ryuu/CS2-Egg-Go/internal/cleanup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintStats is `cs2node cleanup [server]`: what the passes have removed so
// far, per server and per rule.
func PrintStats(w io.Writer, cfg Config, st Status, only string) error {
	ui.Headline(w, "cs2node Cleanup")
	enabled := 0
	for _, r := range cfg.Rules {
		if r.IsEnabled() {
			enabled++
		}
	}
	when := "on server start only"
	if cfg.EveryHours > 0 {
		when = fmt.Sprintf("every %dh", cfg.EveryHours)
		if !st.NextRun.IsZero() {
			when += ui.Gray(", next " + st.NextRun.Local().Format("15:04"))
		}
	}
	ui.KV(w, "rules", fmt.Sprintf("%d of %d enabled", enabled, len(cfg.Rules))+ui.Gray("  /etc/cs2node/config.json"))
	ui.KV(w, "runs", when)
	if cfg.KeepOpen {
		ui.KV(w, "open files", ui.Gray("left alone, they free no disk until the server restarts"))
	}
	total := st.Stats.Total
	if total.Files == 0 {
		fmt.Fprintln(w)
		ui.Line(w, "%s", ui.Gray("(nothing removed yet; force a pass with: cs2node cleanup now)"))
		fmt.Fprintln(w)
		return nil
	}
	ui.KV(w, "removed", ui.Green(fmt.Sprintf("%d file(s), %s", total.Files, ui.Size(total.Bytes)))+
		ui.Gray(fmt.Sprintf("  in %d pass(es) since %s", total.Runs, st.Stats.Since.Local().Format("2006-01-02"))))

	names := make([]string, 0, len(st.Stats.Server))
	for name := range st.Stats.Server {
		if only == "" || name == only {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return errors.New("no cleanup stats for " + only)
	}
	ui.Section(w, "Per server")
	rows := make([][]string, 0, len(names))
	for _, name := range names {
		v := st.Stats.Server[name]
		skipped := ""
		if v.Last.Skipped > 0 {
			skipped = ui.Gray(fmt.Sprintf("%d open", v.Last.Skipped))
		}
		rows = append(rows, []string{name, strconv.Itoa(v.Files), ui.Size(v.Bytes), strconv.Itoa(v.Runs),
			ui.Gray(ui.Ago(v.LastRun)), skipped})
	}
	ui.Table(w, []string{"server", "files", "freed", "passes", "last pass", ""}, rows)

	if only != "" {
		printRules(w, st.Stats.Server[only].PerRule)
	} else {
		printRules(w, st.Stats.Total.PerRule)
	}
	fmt.Fprintln(w)
	ui.Line(w, "%s", ui.Gray("Force a pass: cs2node cleanup now [server]"))
	ui.Line(w, "%s", ui.Gray(ui.ServerIDs))
	fmt.Fprintln(w)
	return nil
}

// printRules lists what each rule accounts for, biggest first.
func printRules(w io.Writer, per map[string]engine.RuleStat) {
	if len(per) == 0 {
		return
	}
	type row struct {
		name string
		s    engine.RuleStat
	}
	list := make([]row, 0, len(per))
	for name, s := range per {
		list = append(list, row{name, s})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].s.Bytes > list[j].s.Bytes })
	ui.Section(w, "Per rule")
	rows := make([][]string, 0, len(list))
	for _, r := range list {
		rows = append(rows, []string{r.name, strconv.Itoa(r.s.Files), ui.Size(r.s.Bytes)})
	}
	ui.Table(w, []string{"rule", "files", "freed"}, rows)
}

// RunNow is `cs2node cleanup now [server]`: cleans in-process and prints
// each server's result as it lands.
func RunNow(ctx context.Context, w io.Writer, m *Module, c *core.Core, only string) error {
	ui.Section(w, "cs2node cleanup")
	conts := c.Containers()
	if len(conts) == 0 {
		ui.Warn(w, "No running CS2 container matches the configured images, nothing to clean")
		return nil
	}
	only, err := core.PickName(core.Names(conts), only)
	if err != nil {
		return err
	}
	var files int
	var bytes int64
	for _, cont := range conts {
		if only != "" && cont.Name != only {
			continue
		}
		if cont.Volume == "" {
			continue
		}
		ui.Step(w, "%s ...", ui.Bold(ui.ServerID(cont.Name)))
		run, ok := m.RunOne(c, cont)
		switch {
		case !ok:
			ui.StepErr(w, "%s: volume unreadable (see: journalctl -u cs2node)", ui.ServerID(cont.Name))
		case run.Files == 0:
			ui.StepOk(w, "%s: nothing to remove%s", ui.ServerID(cont.Name), skipNote(run))
		default:
			ui.StepOk(w, "%s: %d file(s), %s freed in %.1fs%s", ui.ServerID(cont.Name), run.Files, ui.Size(run.Bytes), run.Seconds, skipNote(run))
			files += run.Files
			bytes += run.Bytes
		}
	}
	fmt.Fprintln(w)
	if files == 0 {
		ui.Line(w, "%s", ui.Gray("Nothing was old enough to remove."))
	} else {
		ui.Ok(w, "%d file(s), %s freed", files, ui.Bold(ui.Size(bytes)))
	}
	fmt.Fprintln(w)
	return nil
}

func skipNote(r Run) string {
	if r.Skipped == 0 {
		return ""
	}
	return ui.Gray(fmt.Sprintf("  (%d still open, kept)", r.Skipped))
}
