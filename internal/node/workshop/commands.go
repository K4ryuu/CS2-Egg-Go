// SPDX-License-Identifier: GPL-3.0-or-later

package workshop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// Tools is what the operator commands need outside the daemon.
type Tools struct {
	Cfg   Config
	Store Store
	Out   io.Writer
	HTTP  *http.Client
}

// NewTools builds the command surface from a loaded config.
func NewTools(cfg Config, out io.Writer, client *http.Client) *Tools {
	return &Tools{Cfg: cfg, Store: Store{Dir: cfg.Dir, Mount: cfg.Mount}, Out: out, HTTP: client}
}

// List is `cs2node workshop`: every cached item, its size, and which
// servers link to it.
func (t *Tools) List(ctx context.Context, conts []core.Container) error {
	w := t.Out
	ui.Headline(w, "cs2node Workshop Cache")
	items := t.Store.Items()
	if len(items) == 0 {
		ui.Line(w, "%s", ui.Gray("(nothing cached yet; a server's workshop addons move in here on its next boot)"))
		fmt.Fprintln(w)
		return nil
	}
	// who links to what
	users := map[string][]string{}
	for _, cont := range conts {
		for key := range Used(t.Store, cont.Volume) {
			users[key] = append(users[key], cont.Name)
		}
	}
	stale := map[string]bool{}
	if t.HTTP != nil {
		var ids []string
		seen := map[string]bool{}
		for _, it := range items {
			if !seen[it.ID] {
				seen[it.ID] = true
				ids = append(ids, it.ID)
			}
		}
		if details, err := Details(ctx, t.HTTP, ids); err == nil {
			all := t.Store.NewestAll() // one walk, not one per listed item
			for _, it := range items {
				if d, ok := details[it.ID]; ok && !d.Missing && d.Manifest != "" {
					if newest, have := all[it.ID]; have && ValidManifest(newest.Manifest) && newest.Manifest != d.Manifest {
						stale[it.ID] = true
					}
				}
			}
		}
	}
	var total, saved int64
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		total += it.Bytes
		key := it.ID + "/" + it.Manifest
		who := users[key]
		sort.Strings(who)
		state := ui.Green("shared")
		switch {
		case stale[it.ID]:
			state = ui.Yellow("update out")
		case len(who) == 0:
			state = ui.Gray("unused")
		}
		if n := len(who); n > 0 {
			saved += it.Bytes * int64(n-1)
		}
		rows = append(rows, []string{
			it.ID,
			it.Manifest,
			time.Unix(it.Updated, 0).Format("2006-01-02"),
			ui.Size(it.Bytes),
			strconv.Itoa(len(who)),
			state,
		})
	}
	ui.Table(w, []string{"item", "version", "published", "size", "servers", ""}, rows)
	fmt.Fprintln(w)
	ui.KV(w, "store", t.Cfg.Dir)
	ui.KV(w, "on disk", ui.Size(total))
	ui.KV(w, "saved", ui.Green(ui.Size(saved))+ui.Gray("  what the servers would hold without the cache"))
	seed := ui.Gray("off, each server keeps only what it downloaded")
	if t.Cfg.Seed {
		seed = ui.Green("on") + ui.Gray("  every server is offered every cached addon")
	}
	ui.KV(w, "seeding", seed)
	fmt.Fprintln(w)
	ui.Line(w, "%s", ui.Gray("Maps, models, skins: anything from the workshop. Servers move in and out of the cache on their next boot."))
	ui.Line(w, "%s", ui.Gray("cs2node workshop sync [server]  |  cs2node workshop prune"))
	ui.Line(w, "%s", ui.Gray(ui.ServerIDs))
	fmt.Fprintln(w)
	return nil
}

// Sync is `cs2node workshop sync [server]`: the same pass the egg asks for
// at boot, for servers that are not running.
func (t *Tools) Sync(conts []core.Container, running map[string]bool, only string) error {
	w := t.Out
	ui.Section(w, "cs2node workshop sync")
	only, err := core.PickName(core.Names(conts), only)
	if err != nil {
		return err
	}
	done := false
	for _, cont := range conts {
		if (only != "" && cont.Name != only) || cont.Volume == "" {
			continue
		}
		done = true
		if running[cont.Name] {
			ui.Warn(w, "%s is running, skipped: stop it first, or let its next boot do this", ui.Bold(ui.ServerID(cont.Name)))
			continue
		}
		rep, err := t.Store.Sync(cont.Volume, cont.Owner, t.Cfg.Seed, nil)
		if err != nil {
			ui.Error(w, "%s: %v", cont.Name, err)
			continue
		}
		ui.Ok(w, "%s: %d absorbed, %d linked, %d seeded, %s freed", ui.Bold(ui.ServerID(cont.Name)),
			len(rep.Absorbed), len(rep.Linked), len(rep.Seeded), ui.Size(rep.Freed))
	}
	if !done {
		return errors.New("no matching server volume (cs2node status)")
	}
	fmt.Fprintln(w)
	return nil
}

// Prune is `cs2node workshop prune`: drop versions nothing links to.
func (t *Tools) Prune(conts []core.Container) error {
	w := t.Out
	ui.Section(w, "cs2node workshop prune")
	used := map[string]bool{}
	for _, cont := range conts {
		for key := range Used(t.Store, cont.Volume) {
			used[key] = true
		}
	}
	n, freed := t.Store.Prune(used, t.Cfg.KeepDays, time.Now())
	switch {
	case n == 0:
		ui.Info(w, "Nothing to remove: every stored version is either in use or younger than %d day(s)", t.Cfg.KeepDays)
	default:
		ui.Ok(w, "Removed %d unused version(s), %s freed on the node", n, ui.Size(freed))
	}
	fmt.Fprintln(w)
	return nil
}
