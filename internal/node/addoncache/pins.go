// SPDX-License-Identifier: GPL-3.0-or-later

package addoncache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// Addons in display order.
var Addons = []string{"metamod", "css", "swiftly", "modsharp"}

// Latest is what a caller types to free one server from a global pin.
const Latest = "latest"

// RepoOf is the repo behind a pinnable addon name.
func RepoOf(addon string) string { return addons.Pinnable[addon] }

// load reads the node config and the addoncache section together.
func load(path string) (nodeconfig.Config, Config, error) {
	node, err := nodeconfig.Load(path)
	if err != nil {
		return node, Config{}, fmt.Errorf("%w (run: cs2node install)", err)
	}
	cfg := Defaults()
	ok, err := node.Section("addoncache", &cfg)
	switch {
	case err != nil:
		return node, cfg, err
	case !ok || !cfg.Enabled:
		return node, cfg, errors.New("addoncache module not enabled on this node (cs2node install)")
	}
	return node, cfg, nil
}

func (c Config) global(addon string) string {
	switch addon {
	case "metamod":
		return c.PinMetamod
	case "css":
		return c.PinCSS
	case "swiftly":
		return c.PinSwiftly
	case "modsharp":
		return c.PinModSharp
	}
	return ""
}

func (c *Config) setGlobal(addon, version string) {
	switch addon {
	case "metamod":
		c.PinMetamod = version
	case "css":
		c.PinCSS = version
	case "swiftly":
		c.PinSwiftly = version
	case "modsharp":
		c.PinModSharp = version
	}
}

// PrintPins is `cs2node pins`.
func PrintPins(w io.Writer, path string) error {
	_, cfg, err := load(path)
	if err != nil {
		return err
	}
	ui.Headline(w, "cs2node Addon Pins")
	if !cfg.PinVersions {
		ui.Warn(w, "Version locking is %s: every server follows the newest release.", ui.Bold("off"))
		ui.Line(w, "%s", ui.Gray("Turn it on with: cs2node pin <addon> <version>"))
		fmt.Fprintln(w)
	}
	rows := make([][]string, 0, len(Addons))
	for _, a := range Addons {
		v := cfg.global(a)
		if v == "" {
			v = ui.Gray("newest")
		} else {
			v = ui.Green(v)
		}
		rows = append(rows, []string{a, v})
	}
	ui.Table(w, []string{"addon", "every server"}, rows)
	servers := make([]string, 0, len(cfg.PinPerServer))
	for s := range cfg.PinPerServer {
		servers = append(servers, s)
	}
	sort.Strings(servers)
	if len(servers) > 0 {
		ui.Section(w, "Per server")
		rows = rows[:0]
		for _, s := range servers {
			for _, a := range Addons {
				v, set := cfg.PinPerServer[s][PinKey(a)]
				if !set {
					continue
				}
				if strings.TrimSpace(v) == "" {
					v = ui.Yellow("newest (frees this server)")
				} else {
					v = ui.Green(v)
				}
				rows = append(rows, []string{s, a, v})
			}
		}
		ui.Table(w, []string{"server", "addon", "version"}, rows)
	}
	fmt.Fprintln(w)
	ui.Line(w, "%s", ui.Gray("Servers move to their version on their next restart."))
	ui.Line(w, "%s", ui.Gray("cs2node pin <addon> <version> [server]  |  cs2node unpin <addon>|all [server]"))
	ui.Line(w, "%s", ui.Gray(ui.ServerIDs))
	fmt.Fprintln(w)
	return nil
}

// SetPin is `cs2node pin <addon> <version> [server]`. The version is looked
// up on GitHub first: a typo that silently held the fleet on nothing would
// be worse than a slow command.
func SetPin(ctx context.Context, w io.Writer, path, addon, version, server string, client *http.Client) error {
	repo, known := addons.Pinnable[addon]
	if !known {
		return fmt.Errorf("unknown addon %q, pick one of: %s", addon, strings.Join(Addons, ", "))
	}
	node, cfg, err := load(path)
	if err != nil {
		return err
	}
	ui.Section(w, "cs2node pin")
	free := strings.EqualFold(version, Latest)
	if free && server == "" {
		return errors.New("`latest` only makes sense for one server; to unpin everyone use: cs2node unpin " + addon)
	}
	if !free {
		ui.Info(w, "Looking up %s %s ...", ui.Bold(addon), ui.Bold(version))
		cache := Open(cfg.Dir, client, time.Duration(cfg.RefreshMinutes)*time.Minute)
		rel, err := cache.ReleaseByVersion(ctx, repo, version)
		if err != nil {
			return fmt.Errorf("%w\nNothing was changed: check the version on https://github.com/%s/releases", err, repo)
		}
		ui.Ok(w, "Found release %s with %d asset(s)", ui.Bold(rel.Tag), len(rel.Assets))
	}
	switch {
	case server == "":
		cfg.setGlobal(addon, version)
		cfg.PinVersions = true
	default:
		if cfg.PinPerServer == nil {
			cfg.PinPerServer = map[string]map[string]string{}
		}
		if cfg.PinPerServer[server] == nil {
			cfg.PinPerServer[server] = map[string]string{}
		}
		if free {
			cfg.PinPerServer[server][PinKey(addon)] = ""
		} else {
			cfg.PinPerServer[server][PinKey(addon)] = version
			cfg.PinVersions = true
		}
	}
	if err := save(path, node, cfg); err != nil {
		return err
	}
	switch {
	case free:
		ui.Ok(w, "%s on %s follows the newest release again", ui.Bold(addon), ui.Bold(server))
	case server == "":
		ui.Ok(w, "Every server is held at %s %s", ui.Bold(addon), ui.Bold(version))
	default:
		ui.Ok(w, "%s is held at %s %s", ui.Bold(server), ui.Bold(addon), ui.Bold(version))
	}
	ui.Info(w, "Servers pick it up on their next restart; %s shows the whole picture", ui.Cyan("cs2node pins"))
	fmt.Fprintln(w)
	return nil
}

// ClearPin is `cs2node unpin <addon>|all [server]`.
func ClearPin(w io.Writer, path, addon, server string) error {
	all := addon == "all"
	if _, known := addons.Pinnable[addon]; !known && !all {
		return fmt.Errorf("unknown addon %q, pick one of: %s, all", addon, strings.Join(Addons, ", "))
	}
	node, cfg, err := load(path)
	if err != nil {
		return err
	}
	targets := []string{addon}
	if all {
		targets = Addons
	}
	ui.Section(w, "cs2node unpin")
	switch {
	case server == "":
		for _, a := range targets {
			cfg.setGlobal(a, "")
		}
		if all {
			cfg.PinPerServer = nil
			cfg.PinVersions = false
		}
	default:
		if _, ok := cfg.PinPerServer[server]; !ok {
			return errors.New(server + " has no pin of its own (cs2node pins)")
		}
		for _, a := range targets {
			delete(cfg.PinPerServer[server], PinKey(a))
		}
		if len(cfg.PinPerServer[server]) == 0 {
			delete(cfg.PinPerServer, server)
		}
	}
	if err := save(path, node, cfg); err != nil {
		return err
	}
	switch {
	case server != "" && all:
		ui.Ok(w, "%s follows the node's pins again", ui.Bold(server))
	case server != "":
		ui.Ok(w, "%s follows the node's %s pin again", ui.Bold(server), ui.Bold(addon))
	case all:
		ui.Ok(w, "Every pin is gone, all servers follow the newest releases")
	default:
		ui.Ok(w, "%s follows the newest release again", ui.Bold(addon))
	}
	ui.Info(w, "Servers pick it up on their next restart")
	fmt.Fprintln(w)
	return nil
}

func save(path string, node nodeconfig.Config, cfg Config) error {
	if err := node.SetSection("addoncache", cfg); err != nil {
		return err
	}
	return nodeconfig.Save(path, node)
}
