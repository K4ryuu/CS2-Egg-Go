// SPDX-License-Identifier: GPL-3.0-or-later

// Package guard is the node's packet-level bot and DoS defence: an
// nftables table on the CS2 game ports fed by static rules, by the eggs'
// detections, and by the daemon's own traffic heuristics.
package guard

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
)

// Config is the "guard" section of the node config.
type Config struct {
	Enabled            bool   `json:"enabled"`
	BlockMode          string `json:"block_mode"` // enforce | log (dry-run for egg/idle/chat blocks; static rules always drop)
	UDPPPSLimit        int    `json:"udp_pps_limit"`
	RconSynPerMinute   int    `json:"rcon_syn_per_minute"`
	A2SPPSPerSource    int    `json:"a2s_pps_per_source"`
	TCPPPSPerSource    int    `json:"tcp_pps_per_source"`
	StrangerPPSPerPort int    `json:"stranger_pps_per_port"`
	IdlePPSMax         int    `json:"idle_pps_max"`
	IdleBytesMin       int    `json:"idle_bytes_min"`
	IdleGraceSecs      int    `json:"idle_grace_secs"`
	IdleSampleSecs     int    `json:"idle_sample_secs"`
	IdleStrikes        int    `json:"idle_strikes"`
	IdleBanMinutes     int    `json:"idle_ban_minutes"`
	ChatEarlySecs      int    `json:"chat_early_secs"`
	RepeatWindowHours  int    `json:"repeat_window_hours"`
	MaxBanMinutes      int    `json:"max_ban_minutes"`
	WhitelistIPs       string `json:"whitelist_ips"`
	QuietRules         string `json:"quiet_rules"`
	RatesLog           string `json:"rates_log"`
	RatesLogMaxSources int    `json:"rates_log_max_sources"`
	DockerBridgeSubnet string `json:"docker_bridge_subnet"`
}

// Fixed bans of the static rules and the daemon's own paths.
const (
	TinyBanMinutes    = 1440
	FloodBanMinutes   = 60
	RconBanMinutes    = 1440
	DefaultBanMinutes = 15
	MinBanMinutes     = 1
	PlayerExempt      = time.Hour
	// RulesetTag marks the chain layout; a change recreates the table.
	RulesetTag = "cs2node-guard-1"
	// StateDir holds the offenders file.
	StateDir = "/var/lib/cs2node/guard"
)

// Defaults is what the wizard offers.
func Defaults() Config {
	return Config{
		Enabled: true, BlockMode: "enforce",
		UDPPPSLimit: 1500, RconSynPerMinute: 20, A2SPPSPerSource: 20, TCPPPSPerSource: 200, StrangerPPSPerPort: 3000,
		IdlePPSMax: 15, IdleBytesMin: 100, IdleGraceSecs: 5, IdleSampleSecs: 5, IdleStrikes: 2, IdleBanMinutes: 60, ChatEarlySecs: 10,
		RepeatWindowHours: 24, MaxBanMinutes: 10080,
		RatesLog: "/var/log/cs2node/guard-rates.log", RatesLogMaxSources: 500,
		DockerBridgeSubnet: "172.18.0.0/16",
	}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	switch {
	case c.BlockMode != "enforce" && c.BlockMode != "log":
		return errors.New("block_mode must be enforce or log")
	case c.UDPPPSLimit < 1 || c.RconSynPerMinute < 1 || c.A2SPPSPerSource < 1 || c.TCPPPSPerSource < 1 || c.StrangerPPSPerPort < 1:
		return errors.New("rate limits must be at least 1")
	case c.IdleSampleSecs < 1 || c.IdleStrikes < 1 || c.IdleBanMinutes < 1:
		return errors.New("idle settings must be at least 1")
	case c.MaxBanMinutes < MinBanMinutes:
		return errors.New("max_ban_minutes must be at least 1")
	case c.DockerBridgeSubnet == "":
		return errors.New("docker_bridge_subnet is required")
	}
	return nil
}

// Questions for the install wizard (the tunables an operator actually touches).
// The bridge subnet is read from docker's pterodactyl_nw when it exists.
func Questions() []module.Question {
	d := Defaults()
	bridgeHelp := "its gateway fronts every docker-proxied client, blocking it would cut them all off"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if sub := core.BridgeSubnet(ctx, "pterodactyl_nw"); sub != "" {
		d.DockerBridgeSubnet = sub
		bridgeHelp = "detected from docker network pterodactyl_nw; " + bridgeHelp
	} else {
		bridgeHelp = "pterodactyl_nw not found, this is Wings' default; " + bridgeHelp
	}
	return []module.Question{
		{Key: "block_mode", Prompt: "Block mode", Default: d.BlockMode, Kind: module.Choice, Choices: []module.Option{
			{Value: "enforce", Desc: "drop what the eggs report and what the heuristics catch"},
			{Value: "log", Desc: "dry run: only announce those; the static rules (tiny/pps/rcon) still drop"},
		}},
		{Key: "udp_pps_limit", Prompt: "UDP packets/s per source before a 60m block", Default: strconv.Itoa(d.UDPPPSLimit), Kind: module.Int, Help: "a player sends 64-128; LAN parties and CGNAT share one ip, keep it high"},
		{Key: "rcon_syn_per_minute", Prompt: "TCP SYN per minute per source before a 24h block", Default: strconv.Itoa(d.RconSynPerMinute), Kind: module.Int},
		{Key: "max_ban_minutes", Prompt: "Hard cap for every block, minutes", Default: strconv.Itoa(d.MaxBanMinutes), Kind: module.Int,
			Help: "where the escalation ladder stops: a third offence inside the window gets this. 10080 is a week"},
		{Key: "whitelist_ips", Prompt: "Whitelisted ips or CIDRs (space separated)", Default: d.WhitelistIPs, Help: "never blocked or rate limited; the docker bridge is always included"},
		{Key: "docker_bridge_subnet", Prompt: "Docker bridge subnet (pterodactyl_nw)", Default: d.DockerBridgeSubnet, Help: bridgeHelp},
	}
}
