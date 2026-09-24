// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/addoncache"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/backup"
	nodecleanup "github.com/K4ryuu/CS2-Egg-Go/internal/node/cleanup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/consolelog"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/control"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/crashes"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/guard"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/metrics"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/update"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/workshop"
	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

// guardTools loads the guard config and the daemon snapshot for a CLI run.
func guardTools() (*guard.Tools, error) {
	cfg, err := nodeconfig.Load(nodeconfig.DefaultPath)
	if err != nil {
		return nil, fmt.Errorf("%w (run: cs2node install)", err)
	}
	gcfg := guard.Defaults()
	if ok, err := cfg.Section("guard", &gcfg); err != nil || !ok || !gcfg.Enabled {
		return nil, errors.New("guard module not enabled on this node (cs2node install)")
	}
	var st core.Status
	stPtr := &st
	if err := control.Query(control.DefaultPath, &st); err != nil {
		stPtr = nil
	}
	return guard.NewTools(gcfg, stPtr, os.Stdout)
}

func runGuardCommand(cmd string, args []string) error {
	t, err := guardTools()
	if err != nil {
		return err
	}
	switch cmd {
	case "blocks":
		return t.Blocks()
	case "why":
		if len(args) < 1 {
			return errors.New("usage: cs2node why <ip>")
		}
		return t.Why(args[0])
	case "block":
		if len(args) < 1 {
			return errors.New("usage: cs2node block <ip> [minutes]")
		}
		minutes := guard.DefaultBanMinutes
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &minutes)
		}
		return t.Block(args[0], minutes)
	case "unblock":
		if len(args) < 1 {
			return errors.New("usage: cs2node unblock <ip>|all")
		}
		return t.Unblock(args[0])
	case "rates":
		secs := 10
		if len(args) > 0 {
			fmt.Sscanf(args[0], "%d", &secs)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return t.RatesReport(ctx, secs)
	}
	return nil
}

// runCrashCommand reads bundles straight from disk, the daemon is not needed.
func runCrashCommand(cmd string, args []string) error {
	cfg, err := nodeconfig.Load(nodeconfig.DefaultPath)
	if err != nil {
		return fmt.Errorf("%w (run: cs2node install)", err)
	}
	ccfg := crashes.Defaults()
	if ok, err := cfg.Section("crashes", &ccfg); err != nil || !ok || !ccfg.Enabled {
		return errors.New("crashes module not enabled on this node (cs2node install)")
	}
	switch cmd {
	case "crashes":
		server := ""
		if len(args) > 0 {
			server = args[0]
		}
		return crashes.PrintList(os.Stdout, ccfg, server)
	case "crash":
		if len(args) < 1 {
			return errors.New("usage: cs2node crash <server> [#]")
		}
		n := 1
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &n)
		}
		return crashes.PrintConsole(os.Stdout, ccfg, args[0], n)
	}
	return nil
}

// runLogsCommand reads log files straight from disk, the daemon is not needed.
func runLogsCommand(args []string) error {
	cfg, err := nodeconfig.Load(nodeconfig.DefaultPath)
	if err != nil {
		return fmt.Errorf("%w (run: cs2node install)", err)
	}
	lcfg := consolelog.Defaults()
	if ok, err := cfg.Section("consolelog", &lcfg); err != nil || !ok || !lcfg.Enabled {
		return errors.New("consolelog module not enabled on this node (cs2node install)")
	}
	server := ""
	if len(args) > 0 {
		server = args[0]
	}
	return consolelog.PrintList(os.Stdout, lcfg, server)
}

// runTop is `cs2node top`: a passive core for the container list, docker
// stats for the numbers, the daemon snapshot for map and players.
func runTop(log *slog.Logger) error {
	_, c, _, err := load(log, true)
	if err != nil {
		return err
	}
	reader, err := core.NewStatsReader()
	if err != nil {
		return err
	}
	defer reader.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	c.Passive = true
	go c.Run(ctx)
	time.Sleep(1500 * time.Millisecond)
	states := func() map[string]core.ContainerStatus {
		var st core.Status
		if control.Query(control.DefaultPath, &st) != nil {
			return nil
		}
		out := make(map[string]core.ContainerStatus, len(st.Containers))
		for _, cs := range st.Containers {
			out[cs.Name] = cs
		}
		return out
	}
	err = metrics.Top(ctx, os.Stdout, c, reader, states, time.Second)
	fmt.Fprintln(os.Stdout)
	return err
}

// runPinCommand edits the addoncache pins in the config; the daemon reads
// them per query, so nothing needs restarting here.
func runPinCommand(cmd string, args []string) error {
	switch cmd {
	case "pins":
		return addoncache.PrintPins(os.Stdout, nodeconfig.DefaultPath)
	case "pin":
		if len(args) < 2 {
			return errors.New("usage: cs2node pin <addon> <version> [server]   (version `latest` frees one server)")
		}
		server := ""
		if len(args) > 2 {
			server = args[2]
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return addoncache.SetPin(ctx, os.Stdout, nodeconfig.DefaultPath, args[0], args[1], server, &http.Client{Timeout: time.Minute})
	case "unpin":
		if len(args) < 1 {
			return errors.New("usage: cs2node unpin <addon>|all [server]")
		}
		server := ""
		if len(args) > 1 {
			server = args[1]
		}
		return addoncache.ClearPin(os.Stdout, nodeconfig.DefaultPath, args[0], server)
	}
	return nil
}

// runWorkshopCommand needs the container list, so it runs a passive core
// for one reconcile; a sync only touches servers that are not running.
func runWorkshopCommand(log *slog.Logger, args []string) error {
	cfg, c, _, err := load(log, true)
	if err != nil {
		return err
	}
	wcfg := workshop.Defaults()
	if ok, err := cfg.Section("workshop", &wcfg); err != nil || !ok || !wcfg.Enabled {
		return errors.New("workshop module not enabled on this node (cs2node install)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	c.Passive = true
	go c.Run(ctx)
	time.Sleep(1500 * time.Millisecond) // one reconcile for the container list
	// a passive core only sees running containers, so everything it lists
	// is running; stopped ones come from the volume dirs
	running := map[string]bool{}
	conts := c.Containers()
	for _, cont := range conts {
		running[cont.Name] = true
	}
	conts = append(conts, stoppedVolumes(running)...)
	t := workshop.NewTools(wcfg, os.Stdout, &http.Client{Timeout: 30 * time.Second})
	switch {
	case len(args) == 0:
		return t.List(ctx, conts)
	case args[0] == "sync":
		only := ""
		if len(args) > 1 {
			only = args[1]
		}
		return t.Sync(conts, running, only)
	case args[0] == "prune":
		return t.Prune(conts)
	}
	return errors.New("usage: cs2node workshop [sync [server] | prune]")
}

// stoppedVolumes finds server volumes with no running container, so their
// maps can still be shared.
func stoppedVolumes(running map[string]bool) []core.Container {
	var out []core.Container
	for _, dir := range VolumeDirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() || running[e.Name()] {
				continue
			}
			path := dir + "/" + e.Name()
			if _, err := os.Stat(path + "/" + workshop.VolWorkshop); err != nil {
				continue // never had workshop content
			}
			out = append(out, core.Container{Name: e.Name(), Volume: path, Owner: core.VolumeOwner(path)})
		}
	}
	return out
}

// runBackupCommand: listing reads disk; backup now and restore run a
// passive core for the container list.
func runBackupCommand(log *slog.Logger, cmd string, args []string) error {
	cfg, c, mods, err := load(log, true)
	if err != nil {
		return err
	}
	var bm *backup.Module
	for _, m := range mods {
		if b, ok := m.(*backup.Module); ok {
			bm = b
		}
	}
	if bm == nil {
		return errors.New("backup module not enabled on this node (cs2node install)")
	}
	if err := bm.Configure(c); err != nil {
		return err
	}
	_ = cfg
	if cmd == "backups" {
		server := ""
		if len(args) > 0 {
			server = args[0]
		}
		return backup.PrintList(os.Stdout, bm.Cfg(), server)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	c.Passive = true
	go c.Run(ctx)
	time.Sleep(1500 * time.Millisecond) // one reconcile for the container list
	switch cmd {
	case "backup":
		if len(args) < 1 || args[0] != "now" {
			return errors.New("usage: cs2node backup now [server]")
		}
		server := ""
		if len(args) > 1 {
			server = args[1]
		}
		return backup.RunNow(ctx, os.Stdout, bm, c, server)
	case "restore":
		fs := flag.NewFlagSet("restore", flag.ExitOnError)
		yes := fs.Bool("yes", false, "no confirmation")
		volume := fs.String("volume", "", "volume path when it is not under the default Wings dirs")
		split := 0 // positionals first, flags after
		for split < len(args) && !strings.HasPrefix(args[split], "-") {
			split++
		}
		rest := args[:split]
		fs.Parse(args[split:])
		if len(rest) < 1 {
			return errors.New("usage: cs2node restore <server> [#] [--yes] [--volume /path]")
		}
		n := 1
		if len(rest) > 1 {
			fmt.Sscanf(rest[1], "%d", &n)
		}
		volumeOf := func(name string) (string, core.Owner, bool) {
			if *volume != "" {
				return *volume, core.VolumeOwner(*volume), true
			}
			for _, d := range VolumeDirs {
				p := d + "/" + name
				if st, err := os.Stat(p); err == nil && st.IsDir() {
					return p, core.VolumeOwner(p), true
				}
			}
			return "", core.Owner{}, false
		}
		return backup.RestoreCmd(os.Stdout, os.Stdin, bm.Cfg(), c, volumeOf, rest[0], n, *yes)
	}
	return nil
}

// runUpdate is `cs2node update`: check now, install, restart.
func runUpdate() error {
	cfg, err := nodeconfig.Load(nodeconfig.DefaultPath)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	ui.Section(os.Stdout, "cs2node update")
	if cfg.Channel == "dev" {
		ui.Info(os.Stdout, "Channel %s never self-updates; copy a new binary and run: cs2node install", ui.Bold("dev"))
		return nil
	}
	ui.Info(os.Stdout, "Checking channel %s for something newer than %s...", ui.Bold(cfg.Channel), ui.Bold(version.Version))
	c, err := update.Check(context.Background(), client, cfg.Channel, version.Version)
	if errors.Is(err, update.ErrUpToDate) {
		ui.Ok(os.Stdout, "cs2node %s is current on channel %s", ui.Bold(version.Version), cfg.Channel)
		return nil
	}
	if err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	ui.Info(os.Stdout, "Installing %s -> %s (sha256 verified from checksums.txt)", version.Version, ui.Bold(c.Version))
	if err := update.Apply(context.Background(), client, c, bin); err != nil {
		return err
	}
	ui.Ok(os.Stdout, "Installed %s, previous binary kept as %s", ui.Bold(c.Version), ui.Bold(bin+".prev"))
	if err := update.Restart(); err != nil {
		ui.Warn(os.Stdout, "Restart failed: %v", err)
		return nil
	}
	ui.Ok(os.Stdout, "Daemon restarted on the new version")
	return nil
}

// runCleanupCommand: the stats read the state file; `cleanup now` runs a
// passive core so it has the container list to work from.
func runCleanupCommand(log *slog.Logger, args []string) error {
	_, c, mods, err := load(log, true)
	if err != nil {
		return err
	}
	var cm *nodecleanup.Module
	for _, m := range mods {
		if v, ok := m.(*nodecleanup.Module); ok {
			cm = v
		}
	}
	if cm == nil {
		return errors.New("cleanup module not enabled on this node (cs2node install)")
	}
	if err := cm.Configure(c); err != nil {
		return err
	}
	if len(args) == 0 || args[0] != "now" {
		server := ""
		if len(args) > 0 {
			server = args[0]
		}
		// the daemon has the live counters; the state file is the fallback
		st := nodecleanup.Status{Stats: nodecleanup.LoadStats(nodecleanup.StateDir)}
		var snap core.Status
		if control.Query(control.DefaultPath, &snap) == nil {
			c.UseRemote(snap)
			var live nodecleanup.Status
			if c.ModuleStatus("cleanup", &live) {
				st = live
			}
		}
		return nodecleanup.PrintStats(os.Stdout, cm.Cfg(), st, server)
	}
	server := ""
	if len(args) > 1 {
		server = args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	c.Passive = true
	go c.Run(ctx)
	time.Sleep(1500 * time.Millisecond) // one reconcile for the container list
	return nodecleanup.RunNow(ctx, os.Stdout, cm, c, server)
}
