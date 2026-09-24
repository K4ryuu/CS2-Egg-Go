// SPDX-License-Identifier: GPL-3.0-or-later

// Package vpksync keeps one CS2 install on the node and hands its files to
// every server volume, so SteamCMD runs once per node instead of once per
// server.
package vpksync

import "errors"

// Method is how VPK files reach a volume.
type Method string

const (
	Symlink  Method = "symlink"  // links into a read-only bind mount of the central dir
	Hardlink Method = "hardlink" // same filesystem only, copy otherwise
	Copy     Method = "copy"
)

// Policy is when a server restarts after a CS2 update landed in its volume.
type Policy string

const (
	Immediate Policy = "immediate" // right after the push, players or not
	Empty     Policy = "empty"     // once nobody is on, or after max_delay_hours
	InWindow  Policy = "window"    // once empty, or inside restart_window, or after max_delay_hours
)

// Config is the "vpksync" section of the node config.
type Config struct {
	Enabled       bool   `json:"enabled"`
	CS2Dir        string `json:"cs2_dir"`
	SteamcmdDir   string `json:"steamcmd_dir"`
	PushMethod    Method `json:"push_method"`
	MaxWorkers    int    `json:"max_workers"`
	AutoRestart   bool   `json:"auto_restart"`
	RestartPolicy Policy `json:"restart_policy"`
	RestartWindow string `json:"restart_window"`  // "HH:MM-HH:MM" local time, for window
	MaxDelayHours int    `json:"max_delay_hours"` // restart anyway after this, 0 = never force
	WarnMinutes   int    `json:"warn_minutes"`    // say warnings before a forced restart, 0 = none
	Validate      bool   `json:"validate"`
	CheckMinutes  int    `json:"check_minutes"`
	WingsConfig   string `json:"wings_config"` // empty = auto-detect
}

// Defaults is what the wizard offers.
func Defaults() Config {
	return Config{Enabled: true, CS2Dir: "/srv/cs2-shared", SteamcmdDir: "/root/steamcmd", PushMethod: Symlink, MaxWorkers: 8, AutoRestart: true,
		RestartPolicy: Immediate, RestartWindow: "04:00-06:00", MaxDelayHours: 12, WarnMinutes: 5, CheckMinutes: 1}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	switch {
	case c.CS2Dir == "" || c.CS2Dir[0] != '/':
		return errors.New("cs2_dir must be an absolute path")
	case c.SteamcmdDir == "" || c.SteamcmdDir[0] != '/':
		return errors.New("steamcmd_dir must be an absolute path")
	case c.PushMethod != Symlink && c.PushMethod != Hardlink && c.PushMethod != Copy:
		return errors.New("push_method must be symlink, hardlink or copy")
	case c.MaxWorkers < 1:
		return errors.New("max_workers must be at least 1")
	case c.CheckMinutes < 1:
		return errors.New("check_minutes must be at least 1")
	case c.RestartPolicy != "" && c.RestartPolicy != Immediate && c.RestartPolicy != Empty && c.RestartPolicy != InWindow:
		return errors.New("restart_policy must be immediate, empty or window")
	case c.MaxDelayHours < 0 || c.WarnMinutes < 0:
		return errors.New("max_delay_hours and warn_minutes cannot be negative")
	}
	if c.RestartPolicy == InWindow {
		if _, err := ParseWindow(c.RestartWindow); err != nil {
			return err
		}
	}
	return nil
}

// MountDst is where the central dir appears inside every container in
// symlink mode; the links point here, so they resolve only in-container.
const MountDst = "/tmp/cs2-shared"
