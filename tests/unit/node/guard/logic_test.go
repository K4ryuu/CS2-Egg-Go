// SPDX-License-Identifier: GPL-3.0-or-later

package guard_test

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/guard"
)

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestEscalationLadderAndWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offenders.json")
	o := guard.LoadOffenders(path, 24*time.Hour, 1440)
	now := time.Unix(1_700_000_000, 0)
	want := [][2]int{{30, 1}, {120, 2}, {1440, 3}, {1440, 4}}
	for i, w := range want {
		m, c := o.Escalate("1.2.3.4", 30, now)
		if m != w[0] || c != w[1] {
			t.Fatalf("hit %d: got %d/%d want %d/%d", i+1, m, c, w[0], w[1])
		}
	}
	if m, c := o.Escalate("5.6.7.8", 1000, now); m != 1000 || c != 1 {
		t.Fatalf("first hit keeps the request: %d/%d", m, c)
	}
	if m, _ := o.Escalate("5.6.7.8", 1000, now); m != 1440 {
		t.Fatalf("4x is capped: %d", m)
	}
	// persisted and reloaded. The write is no longer synchronous: a block
	// used to rewrite the whole file twice under the lock that serialises
	// every block on the node, so the daemon flushes on its sampler tick
	// and on the way out instead.
	o.Flush(now)
	o2 := guard.LoadOffenders(path, 24*time.Hour, 1440)
	if c, _ := o2.Count("1.2.3.4"); c != 4 {
		t.Fatalf("offenders must persist: %d", c)
	}
	// a hit after the window starts over
	if m, c := o2.Escalate("1.2.3.4", 30, now.Add(25*time.Hour)); m != 30 || c != 1 {
		t.Fatalf("window expiry did not reset: %d/%d", m, c)
	}
}

func TestClampAndBanForRule(t *testing.T) {
	cases := map[int]int{0: 15, -3: 15, 1: 1, 30: 30, 99999: 1440}
	for in, want := range cases {
		if got := guard.Clamp(in, 1440); got != want {
			t.Errorf("Clamp(%d) = %d want %d", in, got, want)
		}
	}
	if guard.BanForRule("tiny") != 1440 || guard.BanForRule("pps") != 60 || guard.BanForRule("rcon") != 1440 || guard.BanForRule("x") != 15 {
		t.Fatal("ban per rule")
	}
}

func sample(pairs ...any) guard.RateSample {
	s := guard.RateSample{}
	for i := 0; i+2 < len(pairs); i += 3 {
		s[ip(pairs[i].(string))] = guard.Counter{Packets: uint64(pairs[i+1].(int)), Bytes: uint64(pairs[i+2].(int))}
	}
	return s
}

func TestIdleVerdicts(t *testing.T) {
	prev := sample("1.1.1.1", 100, 10000, "2.2.2.2", 100, 5000, "3.3.3.3", 100, 7900, "5.5.5.5", 900, 9000, "6.6.6.6", 100, 1000)
	cur := sample("1.1.1.1", 740, 110000, "2.2.2.2", 130, 6500, "3.3.3.3", 420, 33180, "5.5.5.5", 20, 1000, "6.6.6.6", 100, 1000)
	type want struct {
		v        guard.Verdict
		pps, bpp int
	}
	cases := map[string]want{
		"1.1.1.1": {guard.Active, 128, 156},
		"2.2.2.2": {guard.Idle, 6, 50},  // too few packets
		"3.3.3.3": {guard.Idle, 64, 79}, // the measured ack-only bot: enough packets, too small
		"4.4.4.4": {guard.Gone, 0, 0},   // absent element
		"5.5.5.5": {guard.Idle, 4, 50},  // expired and back: counts from zero
		"6.6.6.6": {guard.Gone, 0, 0},   // unchanged counters = silent
	}
	for s, w := range cases {
		v, pps, bpp := guard.IdleVerdict(ip(s), prev, cur, 5, 15, 100)
		if v != w.v || pps != w.pps || bpp != w.bpp {
			t.Errorf("%s: got %v %d %d want %v %d %d", s, v, pps, bpp, w.v, w.pps, w.bpp)
		}
	}
}

func TestReportSortsAndMarksPlayers(t *testing.T) {
	prev := sample("1.1.1.1", 0, 0, "2.2.2.2", 0, 0)
	cur := sample("1.1.1.1", 100, 26000, "2.2.2.2", 500, 40000, "2001:db8::1", 50, 5000)
	who := func(a netip.Addr) string {
		if a == ip("1.1.1.1") {
			return "joined(59e9667c,uid12)"
		}
		return ""
	}
	r := guard.Report(prev, cur, 5, who)
	if len(r) != 3 || r[0].IP != ip("2.2.2.2") || r[0].PPS != 100 || r[0].BPP != 80 || r[0].Who != "-" {
		t.Fatalf("top line: %+v", r)
	}
	if r[1].IP != ip("1.1.1.1") || r[1].PPS != 20 || r[1].BPP != 260 || r[1].Who != "joined(59e9667c,uid12)" {
		t.Fatalf("player line: %+v", r[1])
	}
	if !r[2].IP.Is6() || r[2].PPS != 10 {
		t.Fatalf("v6 line: %+v", r[2])
	}
}

func TestParseKmsg(t *testing.T) {
	rule, a, port, ok := guard.ParseKmsg("6,1234,567890,-;cs2guard pps IN=eth0 OUT= MAC=00 SRC=1.2.3.4 DST=9.9.9.9 LEN=1228 PROTO=UDP SPT=5 DPT=27015 LEN=1208")
	if !ok || rule != "pps" || a != ip("1.2.3.4") || port != 27015 {
		t.Fatalf("%q %v %d %v", rule, a, port, ok)
	}
	if _, _, _, ok := guard.ParseKmsg("cs2guard tiny IN=eth0 SRC=5.6.7.8"); ok {
		t.Fatal("no DPT must not parse")
	}
	if _, _, _, ok := guard.ParseKmsg("6,1,2,-;something else SRC=1.1.1.1 DPT=1"); ok {
		t.Fatal("other kernel lines must not parse")
	}
	rule, a, _, ok = guard.ParseKmsg("cs2guard rcon SRC=2001:db8::5 DPT=27015")
	if !ok || rule != "rcon" || !a.Is6() {
		t.Fatal("v6 source")
	}
}

func TestRatesLogLinesRotationAndLookups(t *testing.T) {
	dir := t.TempDir()
	r := &guard.RatesLog{Path: filepath.Join(dir, "rates.log"), MaxSources: 2, MaxBytes: 200, Keep: 3}
	now := time.Unix(1_788_806_404, 0)
	r.Sample(now, []guard.RateLine{{IP: ip("1.1.1.1"), PPS: 64, BPP: 264, Who: "joined(abc,uid1)"}, {IP: ip("2.2.2.2"), PPS: 64, BPP: 79, Who: "-"}})
	r.Block(now, ip("2.2.2.2"), 60, "idle client 64 pps 79 B/pkt, uid 2, abc")
	r.Sample(now, []guard.RateLine{{IP: ip("1.1.1.1"), PPS: 10}, {IP: ip("2.2.2.2"), PPS: 20}, {IP: ip("3.3.3.3"), PPS: 70}})
	data, _ := os.ReadFile(r.Path)
	got := string(data)
	for _, want := range []string{
		"1788806404 rate 1.1.1.1 64 264 joined(abc,uid1)\n",
		"1788806404 rate 2.2.2.2 64 79 -\n",
		"1788806404 block 2.2.2.2 60 idle client 64 pps 79 B/pkt, uid 2, abc\n",
		"1788806404 flood 3 100\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "rate 3.3.3.3") {
		t.Fatal("above the cap only the flood summary is written")
	}
	if r.LastBlockReason(ip("2.2.2.2")) != "idle client 64 pps 79 B/pkt, uid 2, abc" || r.LastBlockReason(ip("9.9.9.9")) != "" {
		t.Fatalf("reason lookup: %q", r.LastBlockReason(ip("2.2.2.2")))
	}
	if len(r.LastRates(ip("1.1.1.1"), 3)) != 1 || len(r.BlockHistory(ip("2.2.2.2"), 10)) != 1 {
		t.Fatal("history lookups")
	}
	// push past 200 bytes: rotation
	for i := 0; i < 5; i++ {
		r.Block(now, ip("2.2.2.2"), 60, "manual")
	}
	if _, err := os.Stat(r.Path + ".1"); err != nil {
		t.Fatal("rotation must create .1")
	}
	var empty *guard.RatesLog
	empty.Block(now, ip("1.1.1.1"), 1, "x") // nil log is a no-op
}

// The reason has to outlive the rates log: that log rotates at 20 MB and
// the kernel rate limits its own lines, so neither can be asked later.
func TestReasonLastsAsLongAsTheBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offenders.json")
	now := time.Now()
	o := guard.LoadOffenders(path, 24*time.Hour, 1440)
	o.Escalate("5.255.117.135", 1440, now)
	o.Record("5.255.117.135", "stuck_no_full, srv-1, repeat #3", now.Add(24*time.Hour), now)
	o.Flush(now)

	// a fresh daemon reads it back
	again := guard.LoadOffenders(path, 24*time.Hour, 1440)
	why, ok := again.Reason("5.255.117.135")
	if !ok || why != "stuck_no_full, srv-1, repeat #3" {
		t.Fatalf("reason lost across a restart: %q %v", why, ok)
	}

	// the offender window would drop the record, but the block is still on.
	// Flush is where the prune happens now, so that is what is driven.
	later := now.Add(23 * time.Hour)
	again.Escalate("9.9.9.9", 10, later)
	again.Flush(later)
	if why, ok := again.Reason("5.255.117.135"); !ok || why == "" {
		t.Fatal("a live block must keep its reason past the offender window")
	}

	// once the block has lifted and the window passed, it goes
	after := now.Add(49 * time.Hour)
	again.Escalate("9.9.9.9", 10, after)
	again.Flush(after)
	if _, ok := again.Reason("5.255.117.135"); ok {
		t.Fatal("an expired block must not keep its record forever")
	}
}

// The addresses come from containers reporting hits, so the store cannot
// be allowed to grow with however many a server chooses to report.
func TestOffendersAreBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offenders.json")
	o := guard.LoadOffenders(path, 24*time.Hour, 1440)
	now := time.Now()
	for i := 0; i < guard.MaxOffenders+5000; i++ {
		// blocks that have already lifted, which is the evictable case
		o.Escalate(fmt.Sprintf("203.0.%d.%d", (i/256)%256, i%256), 1, now.Add(-48*time.Hour))
	}
	o.Flush(now)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var seen map[string]any
	if err := json.Unmarshal(data, &seen); err != nil {
		t.Fatal(err)
	}
	if len(seen) > guard.MaxOffenders {
		t.Fatalf("offender store grew to %d entries, cap is %d", len(seen), guard.MaxOffenders)
	}
}
