// SPDX-License-Identifier: GPL-3.0-or-later

package guard_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/nodeclient"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/guard"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

type fakeFW struct {
	mu      sync.Mutex
	applied int
	ports   []uint16
	blocks  map[netip.Addr]time.Duration
	players map[netip.Addr]time.Duration
	sample  guard.RateSample
}

func newFakeFW() *fakeFW {
	return &fakeFW{blocks: map[netip.Addr]time.Duration{}, players: map[netip.Addr]time.Duration{}, sample: guard.RateSample{}}
}

func (f *fakeFW) Apply(guard.Config, []netip.Prefix) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied++
	return f.applied == 1, nil
}
func (f *fakeFW) Loaded() bool { return true }
func (f *fakeFW) RefreshPorts(p []uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ports = p
	return nil
}
func (f *fakeFW) Block(a netip.Addr, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocks[a] = d
	return nil
}
func (f *fakeFW) Unblock(a netip.Addr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.blocks, a)
	return nil
}
func (f *fakeFW) UnblockAll() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocks = map[netip.Addr]time.Duration{}
	return nil
}
func (f *fakeFW) AddPlayer(a netip.Addr, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.players[a] = d
	return nil
}
func (f *fakeFW) Blocks() ([]guard.BlockEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []guard.BlockEntry
	for a, d := range f.blocks {
		out = append(out, guard.BlockEntry{IP: a, Timeout: d, Expires: d})
	}
	return out, nil
}
func (f *fakeFW) RateSample() (guard.RateSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := guard.RateSample{}
	for k, v := range f.sample {
		out[k] = v
	}
	return out, nil
}
func (f *fakeFW) Ports() ([]uint16, error) { return f.ports, nil }
func (f *fakeFW) RuleCount() (int, error)  { return guard.RuleTotal, nil }

func (f *fakeFW) players_() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.players)
}

func (f *fakeFW) blocked(a netip.Addr) (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.blocks[a]
	return d, ok
}

type fakeDocker struct {
	running []core.Container
	events  chan core.Event
}

func (f *fakeDocker) Running(context.Context, []string) ([]core.Container, error) {
	return f.running, nil
}
func (f *fakeDocker) Inspect(context.Context, string) (core.Container, error) {
	return f.running[0], nil
}
func (f *fakeDocker) Events(context.Context, []string) (<-chan core.Event, <-chan error) {
	return f.events, make(chan error)
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cs2g")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

type rig struct {
	fw     *fakeFW
	client *nodeclient.Client
	cancel context.CancelFunc
}

func start(t *testing.T, patch func(*guard.Config)) *rig {
	t.Helper()
	vol := shortTempDir(t)
	cfgG := guard.Defaults()
	cfgG.RatesLog = ""
	if patch != nil {
		patch(&cfgG)
	}
	cfg := nodeconfig.Default()
	cfg.SetSection("guard", cfgG)
	abc := core.Container{ID: "1", Name: "abcdef123456", Image: "sples1/k4ryuu-cs2:dev", Volume: vol, Ports: []uint16{27015, 27020}}
	fd := &fakeDocker{running: []core.Container{abc}, events: make(chan core.Event)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := core.New(fd, cfg, []string{"guard"}, log)
	m := guard.New(log)
	fw := newFakeFW()
	m.FW = fw
	m.StateDir = shortTempDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	go m.Run(ctx, c)
	deadline := time.Now().Add(3 * time.Second)
	for {
		fw.mu.Lock()
		ok := len(fw.ports) == 2
		fw.mu.Unlock()
		if ok || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cl, err := nodeclient.Connect(ctx, core.SocketPath(vol), 3*time.Second, proto.Hello{BootID: "b"})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the server side register the connection
	return &rig{fw: fw, client: cl, cancel: cancel}
}

func (r *rig) notice(t *testing.T) *proto.GuardBlock {
	t.Helper()
	done := make(chan *proto.GuardBlock, 1)
	go func() {
		for {
			m, err := r.client.Next()
			if err != nil {
				done <- nil
				return
			}
			if b, ok := m.(*proto.GuardBlock); ok {
				done <- b
				return
			}
		}
	}()
	select {
	case b := <-done:
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("no notice")
		return nil
	}
}

func TestPortsAndEggHitBecomesABlockWithNotice(t *testing.T) {
	r := start(t, nil)
	defer r.cancel()
	if len(r.fw.ports) != 2 || r.fw.ports[0] != 27015 {
		t.Fatalf("ports: %v", r.fw.ports)
	}
	r.client.Send(&proto.GuardHit{IP: "77.238.141.49", Minutes: 30, Rule: "stray_no_connection", UID: 0})
	b := r.notice(t)
	if b == nil || b.IP != "77.238.141.49" || b.Minutes != 30 || b.Rule != "stray_no_connection" || b.Src != "egg" || b.Dry {
		t.Fatalf("notice: %+v", b)
	}
	if d, ok := r.fw.blocked(ip("77.238.141.49")); !ok || d != 30*time.Minute {
		t.Fatalf("block: %v %v", d, ok)
	}
	// second hit inside the window escalates to 4x
	r.client.Send(&proto.GuardHit{IP: "77.238.141.49", Minutes: 30, Rule: "connect_flood"})
	b = r.notice(t)
	if b == nil || b.Minutes != 120 {
		t.Fatalf("escalated notice: %+v", b)
	}
	// private and invalid ips never reach the firewall
	r.client.Send(&proto.GuardHit{IP: "172.18.0.1", Minutes: 30, Rule: "x"})
	r.client.Send(&proto.GuardHit{IP: "1.2.3.4;flush", Minutes: 30, Rule: "x"})
	r.client.Send(&proto.GuardHit{IP: "9.9.9.9", Minutes: 99999, Rule: "rcon_bruteforce"})
	b = r.notice(t)
	if cap := guard.Defaults().MaxBanMinutes; b == nil || b.IP != "9.9.9.9" || b.Minutes != cap {
		t.Fatalf("a hit over the cap must land exactly on it (%d): %+v", cap, b)
	}
	if _, ok := r.fw.blocked(ip("172.18.0.1")); ok {
		t.Fatal("bridge ip blocked")
	}
}

func TestLogModeSendsDryNoticesOnly(t *testing.T) {
	r := start(t, func(c *guard.Config) { c.BlockMode = "log" })
	defer r.cancel()
	r.client.Send(&proto.GuardHit{IP: "7.7.7.7", Minutes: 30, Rule: "connect_flood"})
	b := r.notice(t)
	if b == nil || !b.Dry || b.IP != "7.7.7.7" {
		t.Fatalf("dry notice: %+v", b)
	}
	if _, ok := r.fw.blocked(ip("7.7.7.7")); ok {
		t.Fatal("log mode must not block")
	}
}

func TestWhitelistAndQuietRules(t *testing.T) {
	r := start(t, func(c *guard.Config) { c.WhitelistIPs = "5.0.0.0/8"; c.QuietRules = "connect_flood" })
	defer r.cancel()
	r.client.Send(&proto.GuardHit{IP: "5.1.1.1", Minutes: 30, Rule: "stray_no_connection"})
	r.client.Send(&proto.GuardHit{IP: "6.6.6.6", Minutes: 30, Rule: "connect_flood"})
	r.client.Send(&proto.GuardHit{IP: "6.6.6.7", Minutes: 30, Rule: "stray_no_connection"})
	b := r.notice(t)
	if b == nil || b.IP != "6.6.6.7" {
		t.Fatalf("quiet rule must block silently, the loud one notifies: %+v", b)
	}
	if _, ok := r.fw.blocked(ip("5.1.1.1")); ok {
		t.Fatal("whitelisted ip blocked")
	}
	if _, ok := r.fw.blocked(ip("6.6.6.6")); !ok {
		t.Fatal("quiet rule must still block")
	}
}

func TestJoinChatAndIdle(t *testing.T) {
	r := start(t, func(c *guard.Config) {
		c.IdleSampleSecs = 1
		c.IdleGraceSecs = 0
		c.IdleStrikes = 2
		c.ChatEarlySecs = 10
	})
	defer r.cancel()
	r.fw.mu.Lock()
	r.fw.sample[ip("1.2.3.4")] = guard.Counter{Packets: 200, Bytes: 15800} // 79 B/pkt
	r.fw.sample[ip("5.6.7.8")] = guard.Counter{Packets: 200, Bytes: 52000} // 260 B/pkt
	r.fw.mu.Unlock()
	r.client.Send(&proto.GuardEvent{Kind: proto.EventJoin, IP: "1.2.3.4", UID: 12})
	r.client.Send(&proto.GuardEvent{Kind: proto.EventJoin, IP: "5.6.7.8", UID: 13})
	r.client.Send(&proto.GuardEvent{Kind: proto.EventJoin, IP: "172.18.0.1", UID: 14})
	time.Sleep(150 * time.Millisecond)
	r.fw.mu.Lock()
	_, p1 := r.fw.players[ip("1.2.3.4")]
	_, pb := r.fw.players[ip("172.18.0.1")]
	r.fw.mu.Unlock()
	if !p1 || pb {
		t.Fatal("join must add public players only")
	}
	r.client.Send(&proto.GuardEvent{Kind: proto.EventChat, IP: "5.6.7.8", UID: 13}) // real player: nothing
	r.client.Send(&proto.GuardEvent{Kind: proto.EventChat, IP: "1.2.3.4", UID: 12}) // ack-only bot
	b := r.notice(t)
	if b == nil || b.IP != "1.2.3.4" || b.Rule != "chatbot" || b.UID != 12 || b.Minutes != 60 {
		t.Fatalf("chatbot notice: %+v", b)
	}
	if _, ok := r.fw.blocked(ip("5.6.7.8")); ok {
		t.Fatal("player blocked for chatting")
	}

	// idle watcher: 5.6.7.8 keeps sending 6 pps of small packets -> two strikes -> block
	go func() {
		for i := 1; i <= 6; i++ {
			time.Sleep(time.Second)
			r.fw.mu.Lock()
			r.fw.sample[ip("5.6.7.8")] = guard.Counter{Packets: uint64(200 + 6*i), Bytes: uint64(52000 + 300*i)}
			r.fw.mu.Unlock()
		}
	}()
	b = r.notice(t)
	if b == nil || b.IP != "5.6.7.8" || b.Rule != "idle" || b.UID != 13 {
		t.Fatalf("idle notice: %+v", b)
	}
}

// A block the rates log has nothing for used to print "?", which told the
// operator nothing about whether the daemon was broken or the kernel had
// simply rate limited its own log line.
func TestBlocksExplainAMissingReason(t *testing.T) {
	dir := t.TempDir()
	fw := newFakeFW()
	now := time.Now()
	ip := netip.MustParseAddr("5.255.117.135")
	old := netip.MustParseAddr("80.240.24.223")
	fw.blocks[ip] = 24 * time.Hour
	fw.blocks[old] = 24 * time.Hour

	var out bytes.Buffer
	rates := &guard.RatesLog{Path: filepath.Join(dir, "rates.log")}
	tools := &guard.Tools{FW: fw, Cfg: guard.Defaults(), Offenders: guard.LoadOffenders(filepath.Join(dir, "off.json"), 24*time.Hour, 1440),
		Rates: rates, Out: &out, Now: func() time.Time { return now }}

	// no log file at all
	if err := tools.Blocks(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no rates log yet") {
		t.Fatalf("want the empty-log case named:\n%s", out.String())
	}

	// a log that starts after the blocks did: their reason can never be in it
	rates.Block(now, netip.MustParseAddr("1.2.3.4"), 10, "something else")
	out.Reset()
	fw.blocks[ip] = 24 * time.Hour
	if err := tools.Blocks(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "?") {
		t.Fatalf("the bare question mark must be gone:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "not logged") && !strings.Contains(out.String(), "older than this log") {
		t.Fatalf("want a stated reason:\n%s", out.String())
	}

	// and a real reason still wins over any of that
	rates.Block(now, ip, 1440, "stuck_no_full, srv-1, repeat #3")
	out.Reset()
	if err := tools.Blocks(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "stuck_no_full, srv-1, repeat #3") {
		t.Fatalf("want the logged reason:\n%s", out.String())
	}
}

// Membership of the players set skips the per-port limiter for every server
// on the node, and the node cannot verify that anyone joined: the claim
// comes from the container. One server must not be able to exempt a botnet
// on everyone else's behalf.
func TestOneServerCannotExemptTheWorld(t *testing.T) {
	r := start(t, nil)
	defer r.cancel()
	for i := 0; i < guard.MaxExemptPerServer+200; i++ {
		r.client.Send(&proto.GuardEvent{Kind: proto.EventJoin, IP: fmt.Sprintf("203.0.%d.%d", i/256, i%256), UID: i})
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r.fw.players_() >= guard.MaxExemptPerServer {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // let any extras through if they are coming
	if n := r.fw.players_(); n > guard.MaxExemptPerServer {
		t.Fatalf("one container exempted %d addresses, cap is %d", n, guard.MaxExemptPerServer)
	}
}
