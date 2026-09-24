// SPDX-License-Identifier: GPL-3.0-or-later

// cs2node is the KitsuneLab CS2 node daemon: one binary, modules chosen at
// install time.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	osexec "os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/addoncache"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/alerts"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/backup"
	nodecleanup "github.com/K4ryuu/CS2-Egg-Go/internal/node/cleanup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/consolelog"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/control"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/crashes"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/guard"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/install"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/metrics"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/update"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/vpksync"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/workshop"
	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

func usage() {
	w := os.Stderr
	fmt.Fprintf(w, "%s  %s\n\n", ui.Bold("KitsuneLab CS2 node"), ui.Gray(version.Version+", "+version.Channel))
	fmt.Fprintf(w, "%s\n", ui.Bold("Usage:"))
	fmt.Fprintf(w, "    cs2node <command> [args]\n")
	group := func(title string) { fmt.Fprintf(w, "\n%s\n", ui.Bold(title)) }
	cmd := func(name, what string) {
		fmt.Fprintln(w, strings.TrimRight("    "+ui.Cyan(fmt.Sprintf("%-24s", name))+" "+what, " "))
	}
	group("Node:")
	cmd("install [--yes]", "install or reconfigure (wizard)")
	cmd("doctor", "health checks, exit 1 on failure")
	cmd("status", "servers and their ids, egg connections, push state, firewall, modules")
	cmd("update", "check the release channel now, install what is newer")
	cmd("uninstall [--purge]", "remove the service (and the config with --purge)")
	cmd("daemon", "run the modules (systemd runs this)")
	cmd("version", "")
	group("Host guard  (module guard):")
	cmd("blocks", "active blocks: ip, ban, time left, hits, why")
	cmd("why <ip>", "everything the node knows about one ip")
	cmd("block <ip> [min]", "manual block (default 15m, never escalated)")
	cmd("unblock <ip>|all", "lift a block")
	cmd("rates [secs]", "packets/s and bytes/packet per source (10s sample)")
	group("Addon cache  (module addoncache):")
	cmd("pins", "the version every server runs, and the per-server exceptions")
	cmd("pin <addon> <ver> [srv]", "hold every server (or one) at a version: metamod, css, swiftly, modsharp")
	cmd("unpin <addon>|all [srv]", "follow the newest release again")
	group("Workshop cache  (module workshop):")
	cmd("workshop", "cached addons: size, version, how many servers share each")
	cmd("workshop sync [srv]", "move a stopped server's addons into the cache and back as links")
	cmd("workshop prune", "drop cached versions nothing links to")
	group("Live view  (module metrics):")
	cmd("top", "map, players, cpu, memory, network per server, like htop")
	group("Crashes  (module crashes):")
	cmd("crashes [server]", "crash bundles: when, exit code, map, players, files")
	cmd("crash <server> [#]", "console tail of one crash bundle (newest by default)")
	group("Console log  (module consolelog):")
	cmd("logs [server]", "console log files on disk: days kept, size, last line")
	group("Backups  (module backup):")
	cmd("backups [server]", "backups on disk: when, files, size")
	cmd("backup now [server]", "back up every running server (or one) right now")
	cmd("restore <server> [#]", "unpack a backup over a stopped server's volume (--yes, --volume)")
	group("Cleanup  (module cleanup):")
	cmd("cleanup [server]", "what the cleanup passes removed, per server and per rule")
	cmd("cleanup now [server]", "run a cleanup pass over every running server (or one) right now")
	fmt.Fprintf(w, "\n  %s\n\n", ui.Gray(ui.ServerIDs))
}

func allModules(log *slog.Logger) []module.Module {
	client := &http.Client{Timeout: 5 * time.Minute}
	// this order is the installer's question order and the order the
	// sections come out of `cs2node status`, so there is one list to keep
	// in a sensible shape rather than two that drift
	return []module.Module{
		vpksync.New(log, client), guard.New(log), alerts.New(log, client), addoncache.New(log, client),
		workshop.New(log, client), backup.New(log), nodecleanup.New(log), crashes.New(log), consolelog.New(log), metrics.New(log),
	}
}

func enabledModules(cfg nodeconfig.Config, all []module.Module) []module.Module {
	var out []module.Module
	for _, m := range all {
		if cfg.Enabled(m.Name()) {
			out = append(out, m)
		}
	}
	return out
}

func names(ms []module.Module) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name())
	}
	return out
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	log := slog.New(ui.NewLogHandler(os.Stderr))
	var err error
	switch os.Args[1] {
	case "version", "-h", "--help", "help":
	default:
		rerunAsRoot() // the config, the control socket and nftables are root-only
	}
	switch os.Args[1] {
	case "version":
		fmt.Printf("cs2node %s (%s, %s, built %s)\n", version.Version, version.Channel, version.Commit, version.Built)
		fmt.Printf("%s\n%s\n", version.Copyright, version.License)
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		yes := fs.Bool("yes", false, "take every default")
		channel := fs.String("channel", "", "update channel: stable, beta or dev (overrides the wizard)")
		fs.Parse(os.Args[2:])
		opts := install.Defaults()
		opts.Yes = *yes
		opts.Channel = *channel
		err = install.Run(opts, allModules(log))
	case "uninstall":
		fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
		purge := fs.Bool("purge", false, "remove the config too")
		fs.Parse(os.Args[2:])
		err = install.Uninstall(install.Defaults(), *purge)
	case "daemon":
		err = runDaemon(log)
	case "doctor":
		os.Exit(runDoctor(log))
	case "status":
		err = runStatus(log)
	case "blocks", "why", "block", "unblock", "rates":
		err = runGuardCommand(os.Args[1], os.Args[2:])
	case "crashes", "crash":
		err = runCrashCommand(os.Args[1], os.Args[2:])
	case "logs":
		err = runLogsCommand(os.Args[2:])
	case "pins", "pin", "unpin":
		err = runPinCommand(os.Args[1], os.Args[2:])
	case "workshop":
		err = runWorkshopCommand(log, os.Args[2:])
	case "cleanup":
		err = runCleanupCommand(log, os.Args[2:])
	case "backups", "backup", "restore":
		err = runBackupCommand(log, os.Args[1], os.Args[2:])
	case "top":
		err = runTop(log)
	case "update":
		err = runUpdate()
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		ui.Error(os.Stderr, "%v", err)
		os.Exit(1)
	}
}

// shortStamp turns a build stamp (2026-09-09T12:20Z) into 09-09 12:20.
func shortStamp(built string) string {
	if t, err := time.Parse("2006-01-02T15:04Z", built); err == nil {
		return t.Format("01-02 15:04")
	}
	return built
}

func orUnknown(s string) string { return ui.Or(s, "unknown") }

// rerunAsRoot re-executes the same command under sudo when not root, the
// way the bash tools did. Returns only when already root or sudo is missing.
func rerunAsRoot() {
	if os.Geteuid() == 0 {
		return
	}
	sudo, err := osexec.LookPath("sudo")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cs2node needs root: run it with sudo")
		os.Exit(1)
	}
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	args := append([]string{"sudo", "--", self}, os.Args[1:]...)
	if err := syscall.Exec(sudo, args, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "cs2node needs root:", err)
		os.Exit(1)
	}
}

// load builds the config, a core and the enabled modules. quiet gives the
// core a silent logger: CLI commands only watch, their core has nothing to say.
func load(log *slog.Logger, quiet bool) (nodeconfig.Config, *core.Core, []module.Module, error) {
	cfg, err := nodeconfig.Load(nodeconfig.DefaultPath)
	if err != nil {
		return cfg, nil, nil, fmt.Errorf("%w (run: cs2node install)", err)
	}
	dock, err := core.NewDocker()
	if err != nil {
		return cfg, nil, nil, err
	}
	mods := enabledModules(cfg, allModules(log))
	coreLog := log
	if quiet {
		coreLog = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	c := core.New(dock, cfg, names(mods), coreLog)
	c.ConfigPath = nodeconfig.DefaultPath
	return cfg, c, mods, nil
}

func runDaemon(log *slog.Logger) error {
	cfg, c, mods, err := load(log, false)
	if err != nil {
		return err
	}
	if len(mods) == 0 {
		return errors.New("no module enabled, nothing to run (cs2node install)")
	}
	log.Info("cs2node starting", "version", version.Version, "channel", cfg.Channel, "modules", strings.Join(names(mods), ","), "images", strings.Join(cfg.Images, ","))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 1)
	for _, m := range mods {
		go func(m module.Module) {
			// one broken module never takes the others down: it is logged,
			// dropped from the hello, and doctor points at it
			if err := m.Run(ctx, c); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("module stopped", "module", m.Name(), "err", err)
				c.Disable(m.Name())
			}
		}(m)
	}
	go func() { errc <- c.Run(ctx) }()
	go func() {
		if err := control.Serve(ctx, control.DefaultPath, func() any { return c.Status(version.Version, version.Built) }); err != nil {
			log.Warn("control socket failed", "err", err)
		}
	}()
	if cfg.Update.Auto && cfg.Channel != "dev" {
		if bin, err := os.Executable(); err == nil {
			go update.Loop(ctx, &http.Client{Timeout: 5 * time.Minute}, cfg.Channel, bin, 24*time.Hour, func(s string) { log.Info(s) }, func(to string) {
				c.Notify(core.NoticeNodeUpdate, "", map[string]any{"from": version.Version, "to": to})
			})
		}
	}
	select {
	case <-ctx.Done():
		log.Info("cs2node stopping")
		return nil
	case err := <-errc:
		return err
	}
}

// runDoctor runs a short-lived core (one reconcile) so checks see the
// containers, then prints the report.
func runDoctor(log *slog.Logger) int {
	const title = "cs2node Doctor"
	var checks []doctor.Check
	cfg, c, mods, err := load(log, true)
	if err != nil {
		checks = append(checks, doctor.Failf("Node", err.Error()))
		return doctor.Print(os.Stdout, title, checks)
	}
	checks = append(checks, doctor.Ok("Node", fmt.Sprintf("cs2node %s, channel %s, config %s", version.Version, cfg.Channel, nodeconfig.DefaultPath)))
	if out, err := osexec.Command("systemctl", "is-active", "cs2node").CombinedOutput(); err != nil {
		checks = append(checks, doctor.Failf("Node", "service not active ("+strings.TrimSpace(string(out))+"), start it: systemctl start cs2node"))
	} else {
		checks = append(checks, doctor.Ok("Node", "service active"))
	}
	var st core.Status
	if err := control.Query(control.DefaultPath, &st); err != nil {
		checks = append(checks, doctor.Failf("Node", "control socket: "+err.Error()))
	} else {
		switch {
		case st.Version != version.Version:
			checks = append(checks, doctor.Warnf("Node", fmt.Sprintf("daemon runs %s, this binary is %s: systemctl restart cs2node", st.Version, version.Version)))
		case st.Built != version.Built:
			checks = append(checks, doctor.Warnf("Node", fmt.Sprintf("daemon is the build from %s, this binary is from %s: systemctl restart cs2node", orUnknown(st.Built), orUnknown(version.Built))))
		default:
			checks = append(checks, doctor.Ok("Node", "daemon answers on the control socket, build "+orUnknown(st.Built)))
		}
		c.UseRemote(st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.Passive = true
	go c.Run(ctx)
	time.Sleep(1500 * time.Millisecond) // one reconcile for the container list
	if len(mods) == 0 {
		checks = append(checks, doctor.Warnf("Node", "no module enabled"))
	}
	for _, cs := range st.Containers {
		switch {
		case !cs.Connected:
		case cs.EggBuilt == "":
			checks = append(checks, doctor.Warnf("Node", cs.Name+": the egg in this container is older than 2026-09-11 (its hello carries no build stamp), so it reports no map or players. Restart the server in the panel, Wings pulls the current image"))
		case !cs.Reported:
			checks = append(checks, doctor.Warnf("Node", cs.Name+": egg built "+cs.EggBuilt+" is connected but reported no map or players yet (it restates every 30s, give it a moment)"))
		default:
			checks = append(checks, doctor.Ok("Node", cs.Name+": egg built "+orUnknown(cs.EggBuilt)+", reports map and players"))
		}
	}
	for _, m := range mods {
		checks = append(checks, m.Doctor(ctx, c)...)
	}
	return doctor.Print(os.Stdout, title, checks)
}

// VolumeDirs is where Wings and Pelican keep server volumes by default.
var VolumeDirs = []string{"/var/lib/pterodactyl/volumes", "/var/lib/pelican/volumes"}
