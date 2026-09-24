// SPDX-License-Identifier: GPL-3.0-or-later

package crashes

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintList is `cs2node crashes [server]`.
func PrintList(w io.Writer, cfg Config, container string) error {
	ui.Headline(w, "cs2node Crashes")
	all := List(cfg.Dir, container)
	if len(all) == 0 {
		if container != "" {
			ui.Line(w, "%s", ui.Gray("(no crash bundle for "+container+" under "+cfg.Dir+")"))
		} else {
			ui.Line(w, "%s", ui.Gray("(no crash bundle under "+cfg.Dir+")"))
		}
		fmt.Fprintln(w)
		return nil
	}
	rows := make([][]string, 0, len(all))
	for i, m := range all {
		files := strconv.Itoa(len(m.Files))
		if len(m.Skipped) > 0 {
			files += ui.Yellow(fmt.Sprintf(" (+%d too big)", len(m.Skipped)))
		}
		rows = append(rows, []string{
			strconv.Itoa(i + 1),
			ui.ServerID(m.Container),
			m.Time.Local().Format("2006-01-02 15:04:05"),
			ui.Gray(ui.Ago(m.Time)),
			ui.Red(strconv.Itoa(m.ExitCode)),
			orDash(m.Map),
			strconv.Itoa(m.Players),
			files,
			ui.Size(m.Bytes),
		})
	}
	ui.Table(w, []string{"#", "server", "when", "", "exit", "map", "players", "files", "size"}, rows)
	fmt.Fprintln(w)
	ui.Line(w, "%s", ui.Gray("Console tail of one bundle: cs2node crash <server> [#]     bundles live under "+cfg.Dir))
	ui.Line(w, "%s", ui.Gray(ui.ServerIDs))
	fmt.Fprintln(w)
	return nil
}

// PrintConsole is `cs2node crash <server> [n]`: the console tail of the
// nth newest bundle of that server (default the newest).
func PrintConsole(w io.Writer, cfg Config, container string, n int) error {
	all := List(cfg.Dir, container)
	if len(all) == 0 {
		return errors.New("no crash bundle for " + container + " under " + cfg.Dir)
	}
	// a short id that fits two servers would silently mix their bundles
	seen := map[string]bool{}
	for _, m := range all {
		seen[m.Container] = true
	}
	if len(seen) > 1 {
		names := make([]string, 0, len(seen))
		for n := range seen {
			names = append(names, n)
		}
		sort.Strings(names)
		return fmt.Errorf("%s matches %d servers (%s): type more of it", container, len(names), strings.Join(names, ", "))
	}
	if n < 1 || n > len(all) {
		return fmt.Errorf("%s has %d bundle(s), pick 1..%d", container, len(all), len(all))
	}
	m := all[n-1]
	text, err := Console(m.Path)
	if err != nil {
		return err
	}
	ui.Headline(w, "Crash "+m.Time.Local().Format("2006-01-02 15:04:05")+"  "+container)
	ui.KV(w, "exit code", ui.Red(strconv.Itoa(m.ExitCode)))
	ui.KV(w, "map", orDash(m.Map))
	ui.KV(w, "players", strconv.Itoa(m.Players))
	ui.KV(w, "bundle", m.Path)
	if len(m.Files) > 0 {
		ui.KV(w, "files", strings.Join(m.Files, ", "))
	}
	if len(m.Skipped) > 0 {
		ui.KV(w, "skipped", ui.Yellow(strings.Join(m.Skipped, ", ")))
	}
	ui.Section(w, "Console tail")
	fmt.Fprint(w, ui.Plain(text))
	fmt.Fprintln(w)
	return nil
}

func orDash(s string) string { return ui.Or(s, ui.Gray("-")) }
