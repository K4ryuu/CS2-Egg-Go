// SPDX-License-Identifier: GPL-3.0-or-later

package consolelog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// Entry is one log file on disk for one server.
type Entry struct {
	Container string
	Name      string // YYYY-MM-DD.log or YYYY-MM-DD.log.gz
	Bytes     int64
	ModTime   time.Time
}

// List returns every log file under dir, newest first. An empty container
// lists every server; a set one narrows to it.
func List(dir, container string) []Entry {
	servers, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, s := range servers {
		if !s.IsDir() || (container != "" && s.Name() != container) {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(dir, s.Name()))
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			out = append(out, Entry{Container: s.Name(), Name: f.Name(), Bytes: info.Size(), ModTime: info.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out
}

// PrintList is `cs2node logs [server]`.
func PrintList(w io.Writer, cfg Config, container string) error {
	ui.Headline(w, "cs2node Console log")
	all := List(cfg.Dir, container)
	if len(all) == 0 {
		if container != "" {
			ui.Line(w, "%s", ui.Gray("(no log files for "+container+" under "+cfg.Dir+")"))
		} else {
			ui.Line(w, "%s", ui.Gray("(no log files under "+cfg.Dir+")"))
		}
		fmt.Fprintln(w)
		return nil
	}
	rows := make([][]string, 0, len(all))
	for _, e := range all {
		rows = append(rows, []string{ui.ServerID(e.Container), e.Name, ui.Size(e.Bytes), ui.Gray(ui.Ago(e.ModTime))})
	}
	ui.Table(w, []string{"server", "file", "size", ""}, rows)
	fmt.Fprintln(w)
	ui.Line(w, "%s", ui.Gray("files live under "+cfg.Dir))
	ui.Line(w, "%s", ui.Gray(ui.ServerIDs))
	fmt.Fprintln(w)
	return nil
}
