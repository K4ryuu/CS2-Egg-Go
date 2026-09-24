// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/control"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/vpksync"
	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

func runStatus(log *slog.Logger) error {
	cfg, c, mods, err := load(log, true)
	if err != nil {
		return err
	}
	w := os.Stdout
	ui.Headline(w, "cs2node Status")
	var st core.Status
	live := control.Query(control.DefaultPath, &st) == nil
	ui.Section(w, "Node")
	ui.KV(w, "binary", fmt.Sprintf("cs2node %s, channel %s", version.Version, cfg.Channel))
	switch {
	case !live:
		ui.KV(w, "daemon", ui.Red("not running")+ui.Gray("  systemctl status cs2node"))
	case st.Version != version.Version:
		ui.KV(w, "daemon", ui.Yellow("running "+st.Version)+ui.Gray("  restart to pick up "+version.Version))
	case st.Built != version.Built:
		ui.KV(w, "daemon", ui.Yellow("running the build from "+orUnknown(st.Built))+ui.Gray("  this binary is from "+orUnknown(version.Built)+", systemctl restart cs2node"))
	default:
		ui.KV(w, "daemon", ui.Green("running"))
	}
	ui.KV(w, "modules", strings.Join(names(mods), ", "))
	ui.KV(w, "images", strings.Join(cfg.Images, ", "))
	if !live {
		fmt.Fprintln(w)
		return nil
	}
	ui.Section(w, "Servers")
	if len(st.Containers) == 0 {
		ui.Line(w, "%s", ui.Gray("(no running CS2 container matches the configured images)"))
	} else {
		rows := make([][]string, 0, len(st.Containers))
		for _, cont := range st.Containers {
			egg := ui.Yellow("not connected")
			if cont.Connected {
				switch {
				case cont.EggBuilt == "":
					egg = ui.Green("connected") + ui.Yellow(" (old image)")
				case !cont.Reported:
					egg = ui.Green("connected") + ui.Gray(" "+shortStamp(cont.EggBuilt)) + ui.Yellow(" (no report)")
				default:
					egg = ui.Green("connected") + ui.Gray(" "+shortStamp(cont.EggBuilt))
				}
			}
			vpk := ui.Gray("-")
			if last, held := vpksync.ServerState(st, cont.Name); last != "" {
				switch last {
				case "done":
					vpk = ui.Green(last)
				case "failed":
					vpk = ui.Red(last)
				default:
					vpk = ui.Yellow(last)
				}
				if held {
					vpk += ui.Yellow(", restart pending")
				}
			}
			ports := strings.Trim(strings.ReplaceAll(fmt.Sprint(cont.Ports), " ", ", "), "[]")
			game, players := ui.Gray("-"), ui.Gray("-")
			if cont.Reported {
				if cont.Map != "" {
					game = cont.Map
				}
				players = fmt.Sprint(cont.Players)
				if !cont.Up {
					game = ui.Yellow("stopped")
				}
			}
			rows = append(rows, []string{ui.Gray(ui.ServerID(cont.Name)), ui.Or(ui.Clip(cont.Server, 32), ui.Gray("-")), ports, egg, vpk, game, players})
		}
		ui.Table(w, []string{"id", "server", "ports", "egg", "vpk", "map", "players"}, rows)
	}
	// every module draws its own section, in the order allModules lists them
	for _, m := range mods {
		if sp, ok := m.(module.Statuser); ok {
			sp.PrintStatus(w, c, st)
		}
	}
	fmt.Fprintln(w)
	return nil
}
