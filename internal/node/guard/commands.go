// SPDX-License-Identifier: GPL-3.0-or-later

package guard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/netx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// Tools is what the operator commands need outside the daemon.
type Tools struct {
	FW        Firewall
	Cfg       Config
	Offenders *Offenders
	Rates     *RatesLog
	Status    *core.Status // nil when the daemon is down
	Out       io.Writer
	Now       func() time.Time // tests pin the clock
}

// NewTools opens the firewall and the state files for a CLI run.
func NewTools(cfg Config, st *core.Status, out io.Writer) (*Tools, error) {
	fw, err := NewFirewall()
	if err != nil {
		return nil, err
	}
	t := &Tools{FW: fw, Cfg: cfg, Status: st, Out: out,
		Offenders: LoadOffenders(filepath.Join(StateDir, "offenders.json"), time.Duration(cfg.RepeatWindowHours)*time.Hour, cfg.MaxBanMinutes)}
	if cfg.RatesLog != "" {
		t.Rates = &RatesLog{Path: cfg.RatesLog, MaxSources: cfg.RatesLogMaxSources}
	}
	return t, nil
}

func (t *Tools) clients() map[string]Client {
	if t.Status == nil {
		return nil
	}
	var st status
	if raw, ok := t.Status.Module["guard"]; ok {
		json.Unmarshal(raw, &st)
	}
	return st.Clients
}

// mins renders a ban the way an operator reads it: 24h, 11h43m, 45m.
func mins(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m < 0:
		return "0m"
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}

func (t *Tools) needTable() error {
	if !t.FW.Loaded() {
		return errors.New("nftables table cs2guard is not loaded - daemon not running? systemctl status cs2node")
	}
	return nil
}

// Blocks prints every active block with its reason.
func (t *Tools) Blocks() error {
	if err := t.needTable(); err != nil {
		return err
	}
	blocks, err := t.FW.Blocks()
	if err != nil {
		return err
	}
	ui.Section(t.Out, "Active blocks")
	if len(blocks) == 0 {
		ui.Line(t.Out, "%s", ui.Gray("(none)"))
		return nil
	}
	logSince, haveLog := t.Rates.Since()
	now := time.Now()
	if t.Now != nil {
		now = t.Now()
	}
	rows := make([][]string, 0, len(blocks))
	notes := map[string]bool{}
	for _, b := range blocks {
		count, _ := t.Offenders.Count(b.IP.String())
		if count == 0 {
			count = 1
		}
		// the store knows for as long as the block lasts; the log is only
		// the fallback for blocks recorded before this daemon learned to
		// keep them
		why, ok := t.Offenders.Reason(b.IP.String())
		if !ok {
			why = t.Rates.LastBlockReason(b.IP)
		}
		if why == "" {
			reason := missingReason(now.Add(b.Expires-b.Timeout), logSince, haveLog)
			notes[reason] = true
			why = ui.Gray(reason)
		}
		hits := fmt.Sprint(count)
		if count > 1 {
			hits = ui.Yellow(hits)
		}
		rows = append(rows, []string{ui.Bold(b.IP.String()), mins(b.Timeout), mins(b.Expires), hits, why})
	}
	ui.Table(t.Out, []string{"ip", "ban", "left", "hits", "why"}, rows)
	if len(notes) > 0 {
		fmt.Fprintln(t.Out)
		for _, n := range missingNotes {
			if notes[n.reason] {
				ui.Line(t.Out, "%s", ui.Gray(n.text))
			}
		}
		ui.Line(t.Out, "%s", ui.Gray("The drop is real either way.  cs2node why <ip>  has the rest."))
	}
	return nil
}

// missingNotes explains each reason a block can have no entry in the rates
// log, in the order they are printed.
var missingNotes = []struct{ reason, text string }{
	{"no rates log yet", "No rates log: rates_log is empty in the config, or the daemon has not written to it yet."},
	{"older than this log", "Older than this log: the rates log rotates at 20 MB, and those lines have aged out."},
	{"not logged", "Not logged: a static nftables rule dropped it, and the kernel rate limits its own"},
	{"not logged", "log lines to 30 a minute, which a flood reaches immediately."},
}

// missingReason says why the rates log has nothing for a block, instead of
// the bare "?" that used to sit there and explain nothing.
func missingReason(started, logSince time.Time, haveLog bool) string {
	switch {
	case !haveLog:
		return "no rates log yet"
	case started.Before(logSince):
		return "older than this log"
	}
	return "not logged"
}

// Why prints everything the node knows about one ip.
func (t *Tools) Why(ipText string) error {
	ip, ok := netx.ParseAddr(ipText)
	if !ok {
		return fmt.Errorf("not a valid ip: %s", ipText)
	}
	w := t.Out
	ui.Section(w, "Why "+ip.String())
	blocked := ui.Gray("no")
	if blocks, err := t.FW.Blocks(); err == nil {
		for _, b := range blocks {
			if b.IP == ip {
				blocked = ui.Red("yes") + fmt.Sprintf(", %s left of %s", mins(b.Expires), mins(b.Timeout))
			}
		}
	}
	ui.KV(w, "blocked now", blocked)
	if why, ok := t.Offenders.Reason(ip.String()); ok {
		ui.KV(w, "reason", why)
	}
	if count, last := t.Offenders.Count(ip.String()); count > 0 {
		ui.KV(w, "offender", fmt.Sprintf("%d block(s), last %s", count, last.Format("2006-01-02 15:04:05")))
	} else {
		ui.KV(w, "offender", ui.Gray("never blocked by this daemon"))
	}
	if cl, ok := t.clients()[ip.String()]; ok {
		ui.KV(w, "joined", fmt.Sprintf("%s  uid %d  since %s", cl.Container, cl.UID, cl.Since.Format("15:04:05")))
	}
	if wl, err := netx.ParsePrefixList(t.Cfg.WhitelistIPs); err == nil && netx.InAny(ip, wl) {
		ui.KV(w, "whitelisted", ui.Green("yes"))
	}
	if t.Rates != nil {
		history := t.Rates.BlockHistory(ip, 10)
		ui.KV(w, "block history", fmt.Sprintf("%s, last 10", ui.Gray(t.Rates.Path)))
		if len(history) == 0 {
			ui.Line(w, "  %s", ui.Gray("(none)"))
		}
		for _, l := range history {
			ui.Line(w, "  %s", humanTS(l))
		}
		samples := t.Rates.LastRates(ip, 3)
		ui.KV(w, "last rate samples", "")
		if len(samples) == 0 {
			ui.Line(w, "  %s", ui.Gray("(none)"))
		}
		for _, l := range samples {
			ui.Line(w, "  %s", humanTS(l))
		}
	}
	if _, err := exec.LookPath("journalctl"); err == nil {
		ui.KV(w, "daemon log (24h)", "")
		out, _ := exec.Command("journalctl", "-u", "cs2node", "--since", "-24h", "-o", "short-iso", "--no-pager").Output()
		n := 0
		for _, l := range strings.Split(string(out), "\n") {
			if strings.Contains(l, "ip="+ip.String()+" ") || strings.HasSuffix(l, "ip="+ip.String()) {
				ui.Line(w, "  %s", l)
				if n++; n >= 10 {
					break
				}
			}
		}
		if n == 0 {
			ui.Line(w, "  %s", ui.Gray("(none)"))
		}
		ui.KV(w, "kernel log (24h)", "")
		out, _ = exec.Command("journalctl", "-k", "--since", "-24h", "-o", "short-iso", "--no-pager").Output()
		n = 0
		for _, l := range strings.Split(string(out), "\n") {
			if strings.Contains(l, "cs2guard") && strings.Contains(l, "SRC="+ip.String()+" ") {
				ui.Line(w, "  %s", l)
				if n++; n >= 5 {
					break
				}
			}
		}
		if n == 0 {
			ui.Line(w, "  %s", ui.Gray("(none)"))
		}
	}
	fmt.Fprintln(w)
	return nil
}

// humanTS swaps the leading epoch of a rates-log line for a readable time.
func humanTS(line string) string {
	f := strings.SplitN(line, " ", 2)
	if len(f) != 2 {
		return line
	}
	var ts int64
	if _, err := fmt.Sscanf(f[0], "%d", &ts); err != nil {
		return line
	}
	return ui.Gray(time.Unix(ts, 0).Format("2006-01-02 15:04:05")) + " " + f[1]
}

// Block adds a manual block: exact minutes, no escalation.
func (t *Tools) Block(ipText string, minutes int) error {
	ip, ok := netx.ParseAddr(ipText)
	if !ok {
		return fmt.Errorf("not a valid ip: %s", ipText)
	}
	bridge, _ := netip.ParsePrefix(t.Cfg.DockerBridgeSubnet)
	if !netx.IsPublic(ip, bridge) {
		return errors.New("refusing to block a private or bridge address: it would cut docker-proxy or local traffic")
	}
	if err := t.needTable(); err != nil {
		return err
	}
	minutes = Clamp(minutes, t.Cfg.MaxBanMinutes)
	if err := t.FW.Block(ip, time.Duration(minutes)*time.Minute); err != nil {
		return err
	}
	now := time.Now()
	t.Rates.Block(now, ip, minutes, "manual")
	// a short-lived command, so the reason is written out here and then
	// `cs2node blocks` can say why this address is blocked for as long as
	// it is, log rotation or not
	t.Offenders.Record(ip.String(), "manual", now.Add(time.Duration(minutes)*time.Minute), now)
	t.Offenders.Flush(now)
	ui.Ok(t.Out, "Blocked %s for %s", ui.Bold(ip.String()), mins(time.Duration(minutes)*time.Minute))
	return nil
}

// Unblock removes one block, or every block with "all".
func (t *Tools) Unblock(ipText string) error {
	if err := t.needTable(); err != nil {
		return err
	}
	if ipText == "all" {
		if err := t.FW.UnblockAll(); err != nil {
			return err
		}
		ui.Ok(t.Out, "All blocks removed")
		return nil
	}
	ip, ok := netx.ParseAddr(ipText)
	if !ok {
		return fmt.Errorf("not a valid ip: %s", ipText)
	}
	if err := t.FW.Unblock(ip); err != nil {
		return err
	}
	ui.Ok(t.Out, "%s unblocked", ui.Bold(ip.String()))
	return nil
}

// RatesReport samples the rate sets twice and prints pps and bytes/packet
// per source.
func (t *Tools) RatesReport(ctx context.Context, secs int) error {
	if secs < 1 {
		secs = 10
	}
	if err := t.needTable(); err != nil {
		return err
	}
	a, err := t.FW.RateSample()
	if err != nil {
		return err
	}
	ui.Section(t.Out, fmt.Sprintf("Per-source rates on the game ports (%ds sample)", secs))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(secs) * time.Second):
	}
	b, err := t.FW.RateSample()
	if err != nil {
		return err
	}
	clients := t.clients()
	report := Report(a, b, secs, func(ip netip.Addr) string {
		if cl, ok := clients[ip.String()]; ok {
			name := cl.Container
			if len(name) > 8 {
				name = name[:8]
			}
			return fmt.Sprintf("joined(%s,uid%d)", name, cl.UID)
		}
		return ""
	})
	if len(report) == 0 {
		ui.Line(t.Out, "%s", ui.Gray("(no traffic on the game ports during the sample)"))
	} else {
		rows := make([][]string, 0, len(report))
		for _, l := range report {
			who := l.Who
			if who != "-" {
				who = ui.Green(who)
			} else {
				who = ui.Gray(who)
			}
			rows = append(rows, []string{l.IP.String(), fmt.Sprint(l.PPS), fmt.Sprint(l.BPP), who})
		}
		ui.Table(t.Out, []string{"source", "pps", "bytes/pkt", "joined player?"}, rows)
	}
	fmt.Fprintln(t.Out)
	ui.Info(t.Out, "A real client sends ~64-128 pps of 100+ byte packets; a joined source far below that is not playing")
	if t.Rates != nil {
		ui.Info(t.Out, "The daemon logs the same every %ds to %s (rate/block/flood lines)", t.Cfg.IdleSampleSecs, ui.Bold(t.Rates.Path))
	}
	return nil
}

// StatusReport prints the guard section of `cs2node status`.
func (t *Tools) StatusReport() {
	ui.Section(t.Out, "Host guard")
	if !t.FW.Loaded() {
		ui.Line(t.Out, "%s", ui.Yellow("nftables table cs2guard not loaded"))
		return
	}
	ports, _ := t.FW.Ports()
	n, _ := t.FW.RuleCount()
	blocks, _ := t.FW.Blocks()
	mode := ui.Green(t.Cfg.BlockMode)
	if t.Cfg.BlockMode == "log" {
		mode = ui.Yellow("log (dry run)")
	}
	ui.KV(t.Out, "table", fmt.Sprintf("inet cs2guard, %d rules", n))
	ui.KV(t.Out, "mode", mode)
	ui.KV(t.Out, "protected ports", fmt.Sprint(ports))
	wl := t.Cfg.WhitelistIPs
	if wl == "" {
		wl = ui.Gray("(none)")
	}
	ui.KV(t.Out, "whitelist", wl+ui.Gray("  + bridge "+t.Cfg.DockerBridgeSubnet))
	ui.KV(t.Out, "active blocks", fmt.Sprint(len(blocks)))
	if clients := t.clients(); len(clients) > 0 {
		ui.KV(t.Out, "watched players", fmt.Sprint(len(clients)))
	}
	if t.Rates != nil {
		ui.KV(t.Out, "rates log", t.Rates.Path)
	}
}
