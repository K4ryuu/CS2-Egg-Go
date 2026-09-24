// SPDX-License-Identifier: GPL-3.0-or-later

// Package install is `cs2node install`: the wizard, the config, the
// systemd unit, and the binary's move into place.
package install

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// Options for Run.
type Options struct {
	ConfigPath string
	BinDest    string
	UnitPath   string
	Yes        bool   // take every default, no prompts
	Channel    string // when set, skips the channel question
	In         io.Reader
	Out        io.Writer
}

// Defaults for a node.
func Defaults() Options {
	return Options{ConfigPath: nodeconfig.DefaultPath, BinDest: "/usr/local/bin/cs2node", UnitPath: "/etc/systemd/system/cs2node.service", In: os.Stdin, Out: os.Stdout}
}

type wizard struct {
	in  *bufio.Scanner
	out io.Writer
	yes bool
}

// ask prints one question block: a blank line, the title, its help lines
// under it (what can be typed, what it means), then the input line. The
// help sits with its own question, never right after the previous answer.
func (w *wizard) ask(prompt, def string, help ...string) string {
	fmt.Fprintln(w.out)
	fmt.Fprintf(w.out, "  %s\n", ui.Bold(prompt))
	for _, h := range help {
		if h != "" {
			fmt.Fprintf(w.out, "  %s %s\n", ui.Gray("↳"), h)
		}
	}
	fmt.Fprintf(w.out, "  %s [%s]: ", ui.Cyan(">"), ui.Cyan(def))
	if w.yes {
		fmt.Fprintln(w.out, def)
		return def
	}
	if !w.in.Scan() {
		fmt.Fprintln(w.out)
		return def
	}
	if v := strings.TrimSpace(w.in.Text()); v != "" {
		return v
	}
	return def
}

func (w *wizard) yesNo(prompt string, def bool) bool {
	d := "n"
	if def {
		d = "y"
	}
	for {
		v := strings.ToLower(w.ask(prompt, d, ui.Bold("y")+" / "+ui.Bold("n")))
		switch v {
		case "y", "yes", "true":
			return true
		case "n", "no", "false":
			return false
		}
		if w.yes {
			return def
		}
		ui.StepWarn(w.out, "Please answer y or n.")
	}
}

// helpLines spells out what can be typed for a question.
func helpLines(q module.Question) []string {
	var lines []string
	switch q.Kind {
	case module.Choice, module.List:
		width := 0
		for _, c := range q.Choices {
			if len(c.Value) > width {
				width = len(c.Value)
			}
		}
		for _, c := range q.Choices {
			lines = append(lines, ui.Bold(fmt.Sprintf("%-*s", width, c.Value))+"   "+ui.Gray(c.Desc))
		}
		if q.Kind == module.List {
			lines = append(lines, ui.Gray("comma-separated, or ")+ui.Bold("all"))
		}
	case module.Bool:
		lines = append(lines, ui.Bold("y")+" / "+ui.Bold("n"))
	case module.Int:
		lines = append(lines, ui.Gray("a whole number"))
	}
	if q.Help != "" {
		lines = append(lines, ui.Gray(q.Help))
	}
	return lines
}

// answer asks one module question until the answer parses.
func (w *wizard) answer(q module.Question) any {
	for {
		v := w.ask(q.Prompt, q.Default, helpLines(q)...)
		if q.Required && v == "" {
			ui.StepWarn(w.out, "This one is needed.")
			continue
		}
		switch q.Kind {
		case module.Int:
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
			ui.StepWarn(w.out, "A whole number please.")
		case module.Bool:
			switch strings.ToLower(v) {
			case "y", "yes", "true":
				return true
			case "n", "no", "false":
				return false
			}
			ui.StepWarn(w.out, "y or n please.")
		case module.Choice:
			var names []string
			for _, c := range q.Choices {
				if v == c.Value {
					return v
				}
				names = append(names, c.Value)
			}
			ui.StepWarn(w.out, "One of: %s", strings.Join(names, ", "))
		case module.List:
			if list, bad := splitList(v, q.Choices); bad == "" {
				return list
			} else {
				ui.StepWarn(w.out, "Unknown entry %q", bad)
			}
		default:
			return v
		}
		if w.yes {
			return q.Default
		}
	}
}

// splitList parses a comma-separated answer; "all" stays a single entry.
// bad names the first entry outside choices (when choices is set).
func splitList(v string, choices []module.Option) (list []string, bad string) {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part == "all" {
			return []string{"all"}, ""
		}
		known := len(choices) == 0
		for _, c := range choices {
			known = known || c.Value == part
		}
		if !known {
			return nil, part
		}
		list = append(list, part)
	}
	return list, ""
}

// Run performs the install.
func Run(opts Options, modules []module.Module) error {
	if os.Geteuid() != 0 {
		return errors.New("run as root: sudo cs2node install")
	}
	w := &wizard{in: bufio.NewScanner(opts.In), out: opts.Out, yes: opts.Yes}
	out := opts.Out
	cfg, err := nodeconfig.Load(opts.ConfigPath)
	fresh := err != nil
	if fresh {
		cfg = nodeconfig.Default()
	}

	// welcome
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s  -  Installer\n", ui.Bold("  KitsuneLab CS2 node"))
	fmt.Fprintf(out, "  %s\n\n", ui.Gray("github.com/K4ryuu/CS2-Egg-Go"))
	for _, m := range modules {
		fmt.Fprintf(out, "  %s  %s\n", ui.Bold(m.Name()), m.Describe())
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s\n", ui.Gray("This installer will:"))
	plan := func(n, what, where string, dim bool) {
		if dim {
			where = ui.Gray(where)
		} else {
			where = ui.Bold(where)
		}
		fmt.Fprintf(out, "    %s %-30s %s  %s\n", ui.Cyan(n), what, ui.Cyan("→"), where)
	}
	plan("1.", "Put the binary in place", opts.BinDest, false)
	plan("2.", "Walk you through configuration", "(wizard)", true)
	plan("3.", "Write the config", opts.ConfigPath, false)
	plan("4.", "Install and start the service", "cs2node", false)
	fmt.Fprintln(out)
	if fresh {
		ui.Step(out, "Fresh install.")
	} else {
		ui.Step(out, "Existing config at %s, its values are the defaults below.", opts.ConfigPath)
	}
	if !w.yesNo("Proceed?", true) {
		ui.Line(out, "Aborted.")
		return nil
	}

	ui.Rule(out, "Node")
	if opts.Channel != "" {
		fmt.Fprintln(out)
		cfg.Channel = opts.Channel
		ui.Step(out, "Update channel: %s", ui.Cyan(cfg.Channel))
	} else {
		cfg.Channel = w.answer(module.Question{Key: "channel", Prompt: "Update channel", Default: cfg.Channel, Kind: module.Choice, Choices: []module.Option{
			{Value: "stable", Desc: "stable releases"},
			{Value: "beta", Desc: "prereleases too (the beta branch)"},
			{Value: "dev", Desc: "never self-update, you copy the binary yourself"},
		}}).(string)
	}
	cfg.Update.Auto = w.yesNo("Self-update automatically", cfg.Update.Auto)

	// modules
	type picked struct {
		name    string
		on      bool
		section map[string]any
		keys    []string
	}
	var picks []picked
	for _, m := range modules {
		ui.Rule(out, m.Name())
		fmt.Fprintf(out, "  %s\n", m.Describe())
		existing := map[string]any{}
		if raw, ok := cfg.Modules[m.Name()]; ok {
			json.Unmarshal(raw, &existing)
		}
		on := w.yesNo("Enable "+m.Name()+"?", fresh || cfg.Enabled(m.Name()))
		section := map[string]any{"enabled": on}
		var keys []string
		if on {
			for _, q := range m.Questions() {
				if q.When != nil && !q.When(section) {
					continue // not relevant with the answers so far
				}
				if q.Required && w.yes && q.Default == "" {
					if v, ok := existing[q.Key]; !ok || fmt.Sprint(v) == "" {
						ui.StepWarn(out, "%s needs %s, left off (run cs2node install without --yes to set it)", m.Name(), q.Key)
						on, section = false, map[string]any{"enabled": false}
						break
					}
				}
				if v, ok := existing[q.Key]; ok {
					if arr, isList := v.([]any); isList {
						parts := make([]string, 0, len(arr))
						for _, a := range arr {
							parts = append(parts, fmt.Sprint(a))
						}
						q.Default = strings.Join(parts, ", ")
					} else {
						q.Default = fmt.Sprint(v)
					}
				}
				section[q.Key] = w.answer(q)
				keys = append(keys, q.Key)
			}
			// what the operator has to know now that it is on
			if noter, ok := m.(module.Noter); ok {
				if notes := noter.Notes(); len(notes) > 0 {
					fmt.Fprintln(out)
					for _, n := range notes {
						fmt.Fprintf(out, "  %s %s\n", ui.Yellow("!"), ui.Gray(n))
					}
				}
			}
		}
		for k, v := range existing {
			if _, set := section[k]; !set {
				section[k] = v
			}
		}
		// keys the wizard never asks about, written out so they can be edited
		if sd, ok := m.(module.Seeder); ok && on {
			for k, v := range sd.Seed() {
				if _, set := section[k]; !set {
					section[k] = v
				}
			}
		}
		cfg.SetSection(m.Name(), section)
		picks = append(picks, picked{m.Name(), on, section, keys})
	}

	// summary
	ui.Rule(out, "Summary")
	fmt.Fprintln(out)
	for _, p := range picks {
		if !p.on {
			fmt.Fprintf(out, "  %s  %s\n\n", ui.Bold(p.name), ui.Gray("off"))
			continue
		}
		fmt.Fprintf(out, "  %s\n", ui.Bold(p.name))
		for _, k := range p.keys {
			ui.KVC(out, k, fmt.Sprint(p.section[k]))
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "  %s\n", ui.Bold("Node"))
	ui.KVC(out, "binary", opts.BinDest)
	ui.KVC(out, "config", opts.ConfigPath)
	ui.KVC(out, "service", "cs2node")
	ui.KVC(out, "channel", cfg.Channel)
	ui.KVC(out, "self-update", strconv.FormatBool(cfg.Update.Auto))
	ui.KVC(out, "images", strings.Join(cfg.Images, ", "))
	if !w.yesNo("Install with these settings?", true) {
		ui.Line(out, "Aborted.")
		return nil
	}

	// install
	ui.Rule(out, "Installing")
	fmt.Fprintln(out)
	if err := nodeconfig.Save(opts.ConfigPath, cfg); err != nil {
		return err
	}
	ui.StepOk(out, "Config written to %s", opts.ConfigPath)
	if err := placeBinary(opts.BinDest); err != nil {
		return err
	}
	ui.StepOk(out, "Binary in place at %s", opts.BinDest)
	if err := os.WriteFile(opts.UnitPath, []byte(unit(opts.BinDest)), 0o644); err != nil {
		return err
	}
	ui.StepOk(out, "Service file written to %s", opts.UnitPath)
	if _, err := exec.LookPath("systemctl"); err != nil {
		ui.StepWarn(out, "systemd not found: start the daemon yourself with %s daemon", opts.BinDest)
	} else {
		for _, args := range [][]string{{"daemon-reload"}, {"enable", "cs2node"}, {"restart", "cs2node"}} {
			if o, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(o)))
			}
		}
		ui.StepOk(out, "Service cs2node enabled and started")
	}

	// done
	ui.Rule(out, "Done")
	fmt.Fprintln(out)
	ui.StepOk(out, "Installation complete.")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s\n", ui.Bold("Quick reference:"))
	ref := func(label, cmd string) { fmt.Fprintf(out, "    %-26s %s\n", label, ui.Cyan(cmd)) }
	ref("Health check:", "cs2node doctor")
	ref("Status:", "cs2node status")
	ref("Daemon logs:", "journalctl -u cs2node -f")
	enabled := map[string]bool{}
	for _, p := range picks {
		enabled[p.name] = p.on
	}
	if enabled["guard"] {
		ref("Blocks / why:", "cs2node blocks | cs2node why <ip>")
		ref("Block / unblock:", "cs2node block <ip> [min] | cs2node unblock <ip>")
		ref("Traffic:", "cs2node rates 10")
	}
	if enabled["metrics"] {
		ref("Live view:", "cs2node top")
	}
	if enabled["crashes"] {
		ref("Crash bundles:", "cs2node crashes | cs2node crash <server>")
	}
	if enabled["backup"] {
		ref("Backups:", "cs2node backups | cs2node backup now | cs2node restore <server>")
	}
	if enabled["workshop"] {
		ref("Workshop cache:", "cs2node workshop | cs2node workshop prune")
	}
	if enabled["addoncache"] {
		ref("Version locks:", "cs2node pins | cs2node pin <addon> <version>")
	}
	if cfg.Channel != "dev" {
		ref("Update now:", "cs2node update")
	}
	ref("Reconfigure:", "cs2node install")

	fmt.Fprintln(out)
	return nil
}

// placeBinary copies the running executable to dest unless it already is
// dest. Written next to the target and renamed, so a running daemon keeps
// its old inode until systemd restarts it.
func placeBinary(dest string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(dest); err == nil && real == self {
		return nil
	}
	data, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	tmp := dest + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func unit(bin string) string {
	return `[Unit]
Description=KitsuneLab CS2 node daemon
Documentation=https://github.com/K4ryuu/CS2-Egg-Go
After=docker.service network-online.target
Requires=docker.service

[Service]
Type=simple
ExecStart=` + bin + ` daemon
Restart=always
RestartSec=5
SyslogIdentifier=cs2node

[Install]
WantedBy=multi-user.target
`
}

// Uninstall stops and removes the service and the binary; the config stays
// unless purge is set.
func Uninstall(opts Options, purge bool) error {
	if os.Geteuid() != 0 {
		return errors.New("run as root")
	}
	out := opts.Out
	ui.Rule(out, "Uninstalling cs2node")
	fmt.Fprintln(out)
	if _, err := exec.LookPath("systemctl"); err == nil {
		exec.Command("systemctl", "disable", "--now", "cs2node").Run()
		ui.StepOk(out, "Service stopped and disabled")
	}
	os.Remove(opts.UnitPath)
	if _, err := exec.LookPath("systemctl"); err == nil {
		exec.Command("systemctl", "daemon-reload").Run()
	}
	os.Remove(opts.BinDest)
	os.Remove(opts.BinDest + ".prev")
	ui.StepOk(out, "Service file and binary removed")
	if purge {
		os.Remove(opts.ConfigPath)
		ui.StepOk(out, "Config removed")
	} else {
		ui.Step(out, "Config kept at %s (use --purge to remove it)", opts.ConfigPath)
	}
	ui.Step(out, "The nftables table inet cs2guard stays until its blocks expire: nft delete table inet cs2guard")
	fmt.Fprintln(out)
	return nil
}
