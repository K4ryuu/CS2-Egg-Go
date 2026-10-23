// SPDX-License-Identifier: GPL-3.0-or-later

package maintenance

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

const aptDir = "/etc/apt/apt.conf.d"

// UpgradesFile sorts after Debian's own 20auto-upgrades and
// 50unattended-upgrades, so its values win; deleting it undoes the setup.
var UpgradesFile = filepath.Join(aptDir, "52cs2node-unattended-upgrades")

// UpgradesConf turns security upgrades on and automatic reboot off: a reboot
// drops every player, so the node only alerts that one is pending.
const UpgradesConf = `// written by cs2node maintenance setup, delete this file to undo it
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
Unattended-Upgrade::Automatic-Reboot "false";
`

// Setup offers to install unattended-upgrades and write UpgradesFile. It
// prints the plan and asks first; yes skips the question.
func Setup(ctx context.Context, w io.Writer, in io.Reader, ex Exec, file string, yes bool) error {
	ui.Section(w, "cs2node maintenance setup")
	out, err := ex(ctx, "dpkg-query", "-W", "-f=${Status}", "unattended-upgrades")
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("needs a Debian or Ubuntu host (dpkg-query not found)")
	}
	installed := strings.Contains(string(out), "install ok installed")

	old, readErr := os.ReadFile(file)
	if errors.Is(readErr, os.ErrNotExist) && installed {
		if active, reboot := activeUpgrades(ctx, ex); active {
			ui.Ok(w, "Unattended upgrades are already on (Automatic-Reboot %q). Left alone, nothing written.", reboot)
			return nil
		}
	}
	switch {
	case readErr == nil && string(old) == UpgradesConf && installed:
		ui.Ok(w, "Already set up: %s", file)
		return nil
	case readErr != nil && !errors.Is(readErr, os.ErrNotExist):
		return readErr
	}

	ui.Line(w, "This will:")
	if !installed {
		ui.Line(w, "  - install the unattended-upgrades package (apt-get install -y)")
	}
	ui.Line(w, "  - write %s:", file)
	for _, l := range strings.Split(strings.TrimSpace(UpgradesConf), "\n") {
		ui.Line(w, "      %s", ui.Gray(l))
	}
	if len(old) > 0 {
		ui.Line(w, "  - replace what is there now (%d bytes)", len(old))
	}
	ui.Line(w, "Security upgrades install on their own; the server never reboots by itself.")
	if !yes && !confirm(w, in) {
		return errors.New("cancelled, nothing changed")
	}

	if !installed {
		ui.Step(w, "apt-get install unattended-upgrades ...")
		if out, err := ex(ctx, "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "unattended-upgrades"); err != nil {
			return fmt.Errorf("apt-get install unattended-upgrades: %w: %s", err, tail(out))
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(UpgradesConf), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		return err
	}
	ui.Ok(w, "Unattended upgrades are on. A pending reboot shows in: cs2node doctor")
	return nil
}

// activeUpgrades reads the effective apt settings, so an operator's own
// 20auto-upgrades or 50unattended-upgrades counts as already set up.
func activeUpgrades(ctx context.Context, ex Exec) (on bool, reboot string) {
	out, err := ex(ctx, "apt-config", "dump")
	if err != nil {
		return false, ""
	}
	reboot = "false"
	for _, l := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSuffix(strings.TrimSpace(l), ";"), " ")
		v = strings.Trim(v, `"`)
		switch {
		case ok && k == "APT::Periodic::Unattended-Upgrade":
			on = v != "0" && v != ""
		case ok && k == "Unattended-Upgrade::Automatic-Reboot":
			reboot = v
		}
	}
	return on, reboot
}

func confirm(w io.Writer, in io.Reader) bool {
	fmt.Fprint(w, "Go ahead? [y/N] ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// tail keeps an apt failure readable: the last lines hold the reason.
func tail(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return strings.Join(lines, " | ")
}
