// SPDX-License-Identifier: GPL-3.0-or-later

package backup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintList is `cs2node backups [server]`.
func PrintList(w io.Writer, cfg Config, container string) error {
	ui.Headline(w, "cs2node Backups")
	all := List(cfg.Dir, container)
	if len(all) == 0 {
		ui.Line(w, "%s", ui.Gray("(no backup under "+cfg.Dir+" yet; next run "+nextRun(time.Now(), cfg.Hour).Format("2006-01-02 15:04")+", or: cs2node backup now)"))
		fmt.Fprintln(w)
		return nil
	}
	rows := make([][]string, 0, len(all))
	for i, m := range all {
		changed := ""
		if len(m.Changed) > 0 {
			changed = ui.Yellow(fmt.Sprintf("%d changed while read", len(m.Changed)))
		}
		rows = append(rows, []string{
			strconv.Itoa(i + 1), ui.ServerID(m.Container), m.Time.Local().Format("2006-01-02 15:04:05"), ui.Gray(ui.Ago(m.Time)),
			strconv.Itoa(m.Files), ui.Size(m.Bytes), fmt.Sprintf("%.0fs", m.Seconds), changed,
		})
	}
	ui.Table(w, []string{"#", "server", "when", "", "files", "size", "took", ""}, rows)
	fmt.Fprintln(w)
	ui.Line(w, "%s", ui.Gray(fmt.Sprintf("Daily at %02d:00, keep %s / %s.  Restore: cs2node restore <server> [#]", cfg.Hour, plural(cfg.KeepDays, "day"), plural(cfg.KeepCount, "per server"))))
	ui.Line(w, "%s", ui.Gray(ui.ServerIDs))
	fmt.Fprintln(w)
	return nil
}

// RunNow is `cs2node backup now [server]`: archives in-process and prints
// each result.
func RunNow(ctx context.Context, w io.Writer, m *Module, c *core.Core, only string) error {
	ui.Section(w, "cs2node backup")
	conts := c.Containers()
	only, err := core.PickName(core.Names(conts), only)
	if err != nil {
		return err
	}
	if len(conts) == 0 {
		ui.Warn(w, "No running CS2 container matches the configured images, nothing to back up")
		return nil
	}
	for _, cont := range conts {
		if only != "" && cont.Name != only {
			continue
		}
		ui.Info(w, "Archiving %s ...", ui.Bold(ui.ServerID(cont.Name)))
		done := m.RunAll(ctx, c, cont.Name)
		if len(done) == 0 {
			ui.Error(w, "%s failed (see: journalctl -u cs2node)", ui.ServerID(cont.Name))
			continue
		}
		meta := done[0]
		ui.Ok(w, "%s: %d files, %s in %.0fs -> %s", ui.ServerID(cont.Name), meta.Files, ui.Size(meta.Bytes), meta.Seconds, ui.Bold(meta.Path))
	}
	return nil
}

// RestoreCmd is `cs2node restore <server> [#] [--yes]`: unpacks a backup
// over the volume of a stopped server. volumeOf finds the volume of a
// server that has no container right now.
func RestoreCmd(w io.Writer, in io.Reader, cfg Config, c *core.Core, volumeOf func(string) (string, core.Owner, bool), container string, n int, yes bool) error {
	all := List(cfg.Dir, container)
	if len(all) == 0 {
		return errors.New("no backup for " + container + " under " + cfg.Dir)
	}
	if n < 1 || n > len(all) {
		return fmt.Errorf("%s has %d backup(s), pick 1..%d", container, len(all), len(all))
	}
	for _, cont := range c.Containers() {
		if cont.Name == container {
			return errors.New(container + " is running: stop it in the panel first, a restore under a live server corrupts files")
		}
	}
	volume, owner, ok := volumeOf(container)
	if !ok {
		return errors.New("volume of " + container + " not found under the Wings volume dirs (use --volume /path)")
	}
	meta := all[n-1]
	ui.Section(w, "cs2node restore")
	ui.KV(w, "server", container)
	ui.KV(w, "backup", meta.Time.Local().Format("2006-01-02 15:04:05")+"  "+ui.Gray(meta.Path))
	ui.KV(w, "files", strconv.Itoa(meta.Files))
	ui.KV(w, "volume", volume)
	ui.Warn(w, "Files in the backup overwrite the volume's copies; nothing else is touched.")
	if !yes {
		fmt.Fprintf(w, "  %s [y/N]: ", ui.Cyan(">"))
		s := bufio.NewScanner(in)
		if !s.Scan() || !strings.EqualFold(strings.TrimSpace(s.Text()), "y") {
			ui.Line(w, "Aborted.")
			return nil
		}
	}
	count, err := Restore(meta.Path, volume, owner)
	if err != nil {
		return err
	}
	ui.Ok(w, "%d files restored into %s", count, volume)
	return nil
}

func plural(n int, what string) string {
	if n == 0 {
		return "unlimited " + what
	}
	return strconv.Itoa(n) + " " + what
}
