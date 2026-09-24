// SPDX-License-Identifier: GPL-3.0-or-later

package guard_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/configs"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/guard"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

const cfgJSON = `{
  "action": "block", "ban_minutes": 15, "cooldown_secs": 30, "stuck_grace_secs": 12,
  "whitelist_steamids": ["76561198000000001"], "whitelist_ips": ["10.0.0.9"],
  "behavior_rules": [
    {"name":"reconnect_stuck","threshold":3,"window_secs":15,"ban_minutes":360},
    {"name":"move_not_joined","threshold":5,"window_secs":15,"ban_minutes":360},
    {"name":"stray_no_connection","threshold":4,"window_secs":10,"ban_minutes":30},
    {"name":"connect_flood","threshold":6,"window_secs":10,"ban_minutes":30},
    {"name":"rcon_bruteforce","threshold":1,"window_secs":10,"ban_minutes":1440},
    {"name":"malformed_packet","threshold":2,"window_secs":10,"ban_minutes":1440},
    {"name":"stuck_no_full","threshold":3,"window_secs":10,"ban_minutes":60}
  ]
}`

type harness struct {
	g     *guard.Guard
	out   *bytes.Buffer
	cmds  []string
	msgs  []proto.Message
	clock time.Time
	node  bool
}

func newHarness(t *testing.T, patch func(*configs.Guard)) *harness {
	t.Helper()
	var cfg configs.Guard
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	if patch != nil {
		patch(&cfg)
	}
	h := &harness{out: &bytes.Buffer{}, clock: time.Unix(1000, 0)}
	g, err := guard.New(cfg, logx.New("T", logx.Debug, h.out))
	if err != nil {
		t.Fatal(err)
	}
	g.Now = func() time.Time { return h.clock }
	g.Send = func(c string) { h.cmds = append(h.cmds, c) }
	g.Report = func(m proto.Message) { h.msgs = append(h.msgs, m) }
	g.NodeActive = func() bool { return h.node }
	h.g = g
	return h
}

func (h *harness) feed(lines ...string) {
	for _, l := range lines {
		h.g.Inspect(l)
	}
}

func (h *harness) tripped(rule string) bool {
	return strings.Contains(h.out.String(), "tripped '"+rule+"'")
}

func (h *harness) sent(cmd string) bool {
	for _, c := range h.cmds {
		if strings.Contains(c, cmd) {
			return true
		}
	}
	return false
}

func (h *harness) punitive() bool {
	for _, c := range h.cmds {
		if strings.HasPrefix(c, "kickid") || strings.HasPrefix(c, "addip") {
			return true
		}
	}
	return false
}

func (h *harness) hit(rule string) *proto.GuardHit {
	for _, m := range h.msgs {
		if x, ok := m.(*proto.GuardHit); ok && x.Rule == rule {
			return x
		}
	}
	return nil
}

func (h *harness) events(kind string) []*proto.GuardEvent {
	var out []*proto.GuardEvent
	for _, m := range h.msgs {
		if x, ok := m.(*proto.GuardEvent); ok && x.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

func stray(ip string, port int) string {
	return fmt.Sprintf("[Data] Ignored bad %s:%d.  Stray data packet from host with no connection.  Ignoring.", ip, port)
}

func TestCleanJoinTriggersNothing(t *testing.T) {
	h := newHarness(t, nil)
	h.feed(
		"Accepting Steam Net connection #2921449639 UDP steamid:76561198345583467@80.250.101.109:53071",
		"Sending S2C_CHALLENGE [0 auth 3] to 80.250.101.109:53071",
		"Receiving C2S_CONNECT [protocol 14178 0 auth 3] from 80.250.101.109:53071",
		"CServerSideClientBase::Connect( name='K4ryuu -'', userid=10, fake=0, connectiontypeflags=4, chan->addr=80.250.101.109:53071 )",
		"Client 10 'K4ryuu -'' signon state SIGNONSTATE_NONE -> SIGNONSTATE_CONNECTED",
		"Client 10 'K4ryuu -'' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL",
		`SV:  "K4ryuu -'<10><[U:1:385317739]><>" STEAM USERID validated`,
	)
	if strings.Contains(h.out.String(), "tripped") || h.punitive() {
		t.Fatalf("clean join reacted: %s %v", h.out.String(), h.cmds)
	}
	if len(h.events(proto.EventJoin)) != 1 || h.events(proto.EventJoin)[0].UID != 10 {
		t.Fatal("FULL must report a join")
	}
}

func TestReconnectStuckKicksAndReportsUID(t *testing.T) {
	h := newHarness(t, nil)
	h.feed("CServerSideClientBase::Connect( name='cs2commends.com', userid=16, fake=0, connectiontypeflags=4, chan->addr=213.189.218.189:51445 )")
	for i := 0; i < 3; i++ {
		h.feed("SV:  Forcing client reconnect (SIGNONSTATE_CONNECTED) for client 'cs2commends.com'")
	}
	if !h.tripped("reconnect_stuck") || !h.sent("kickid 16") {
		t.Fatalf("out=%s cmds=%v", h.out.String(), h.cmds)
	}
	hit := h.hit("reconnect_stuck")
	if hit == nil || hit.IP != "213.189.218.189" || hit.Minutes != 360 || hit.UID != 16 {
		t.Fatalf("hit: %+v", hit)
	}
}

func TestMoveNotJoinedBlocksAdName(t *testing.T) {
	h := newHarness(t, nil)
	name := "★★★ STEAMLVLS.COM ★★ CHEAP STEAM LEVELS"
	h.feed(fmt.Sprintf("CServerSideClientBase::Connect( name='%s', userid=7, fake=0, connectiontypeflags=4, chan->addr=45.45.45.45:2000 )", name))
	for i := 0; i < 5; i++ {
		h.feed("[" + name + "] sent command that failed delta decode, discarding move msg")
	}
	if !h.tripped("move_not_joined") || !h.sent("addip 360 45.45.45.45") {
		t.Fatalf("cmds=%v", h.cmds)
	}
}

func TestStrayIsSwallowedAndBlocked(t *testing.T) {
	h := newHarness(t, nil)
	for i := 1; i <= 4; i++ {
		if h.g.Inspect(stray("77.238.141.49", 1400+i)) {
			t.Fatal("stray line must be swallowed")
		}
	}
	if !h.tripped("stray_no_connection") || !h.sent("addip 30 77.238.141.49") {
		t.Fatalf("cmds=%v out=%s", h.cmds, h.out.String())
	}
	hit := h.hit("stray_no_connection")
	if hit == nil || hit.UID != 0 || hit.Minutes != 30 {
		t.Fatalf("hit %+v", hit)
	}
	if !h.g.Inspect("Accepting Steam Net connection #1 UDP steamid:1@1.2.3.4:1") {
		t.Fatal("lifecycle line must be kept")
	}
}

func TestTrustedIPExemptsStrayButNotRcon(t *testing.T) {
	h := newHarness(t, nil)
	h.feed(
		"CServerSideClientBase::Connect( name='Real', userid=20, fake=0, connectiontypeflags=4, chan->addr=80.250.101.109:53071 )",
		"Client 20 'Real' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL",
	)
	for i := 1; i <= 6; i++ {
		h.feed(stray("80.250.101.109", 53000+i))
	}
	if strings.Contains(h.out.String(), "tripped") || h.punitive() {
		t.Fatal("stray from a FULL ip must not react")
	}
	h.feed("Banning 80.250.101.109 for rcon hacking attempts")
	if !h.tripped("rcon_bruteforce") || !h.sent("addip 1440 80.250.101.109") {
		t.Fatal("rcon must ignore ip trust")
	}
}

func TestLifecycleEventsAndConsoleChatIgnored(t *testing.T) {
	h := newHarness(t, nil)
	h.feed(
		"CServerSideClientBase::Connect( name='Real', userid=20, fake=0, connectiontypeflags=4, chan->addr=80.250.101.109:53071 )",
		"Client 20 'Real' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL",
		"[All Chat][Real (385317739)]: hello (there)",
		"[All Chat][Console (0)]: panel message",
		"SV:  Dropped client 'Real' from server(29): NETWORK_DISCONNECT_TIMEDOUT",
	)
	if len(h.events(proto.EventJoin)) != 1 || len(h.events(proto.EventChat)) != 1 || len(h.events(proto.EventLeave)) != 1 {
		t.Fatalf("events: %+v", h.msgs)
	}
	if h.events(proto.EventChat)[0].IP != "80.250.101.109" || h.events(proto.EventLeave)[0].UID != 20 {
		t.Fatal("event fields")
	}
	if h.punitive() || strings.Contains(h.out.String(), "tripped") {
		t.Fatal("lifecycle lines are not reactions")
	}
}

func TestReconnectRaceIsQuiet(t *testing.T) {
	h := newHarness(t, nil)
	h.feed(
		"CServerSideClientBase::Connect( name='Real', userid=20, fake=0, connectiontypeflags=4, chan->addr=80.250.101.109:53071 )",
		"Client 20 'Real' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL",
		"Accepting Steam Net connection #99 UDP steamid:76561198345583467@80.250.101.109:53090",
		"BOTGUARD_CHANNEL_OK",
		"CServerSideClientBase::Connect( name='Real', userid=21, fake=0, connectiontypeflags=4, chan->addr=80.250.101.109:53090 )",
	)
	for i := 0; i < 6; i++ {
		h.feed(stray("80.250.101.109", 53071), "Receiving C2S_CONNECT [protocol 14178 0 auth 3] from 80.250.101.109:53090")
	}
	h.clock = h.clock.Add(30 * time.Second)
	for i := 1; i <= 8; i++ {
		h.feed(fmt.Sprintf("[#99 UDP steamid:76561198345583467@80.250.101.109:53090 'Real'] %d reply timeouts, last recv 1%d000ms ago.  Sending keepalive.", i, i))
	}
	if strings.Contains(h.out.String(), "tripped") || h.punitive() {
		t.Fatalf("reconnecting player reacted: %s %v", h.out.String(), h.cmds)
	}
}

func TestFloodRconMalformed(t *testing.T) {
	h := newHarness(t, nil)
	for i := 1; i <= 6; i++ {
		h.feed(fmt.Sprintf("Receiving C2S_CONNECT [protocol 14175 0 auth 3] from 9.9.9.9:%d", 40000+i))
	}
	if !h.sent("addip 30 9.9.9.9") {
		t.Fatal("connect_flood")
	}
	h = newHarness(t, nil)
	h.feed("Banning 200.86.3.21 for rcon hacking attempts")
	if !h.sent("addip 1440 200.86.3.21") {
		t.Fatal("rcon_bruteforce")
	}
	h = newHarness(t, nil)
	for i := 1; i <= 4; i++ {
		if h.g.Inspect(fmt.Sprintf("[packet] Ignored bad 8.8.8.8:%d.  Invalid length byte 0x00 at 0x40", 5000+i)) {
			t.Fatal("malformed line must be swallowed")
		}
	}
	if !h.sent("addip 1440 8.8.8.8") {
		t.Fatal("malformed_packet length byte")
	}
	h = newHarness(t, nil)
	for i := 1; i <= 4; i++ {
		h.feed(fmt.Sprintf("[packet] Ignored bad 212.124.7.114:%d.  Invalid lead byte 0x00", 9000+i))
	}
	if !h.sent("addip 1440 212.124.7.114") {
		t.Fatal("malformed_packet lead byte")
	}
}

func TestStuckNoFullWithAndWithoutUserID(t *testing.T) {
	h := newHarness(t, nil)
	h.feed("CServerSideClientBase::Connect( name='cs2commends.com', userid=17, fake=0, connectiontypeflags=4, chan->addr=2.2.2.2:47386 )")
	h.clock = h.clock.Add(30 * time.Second)
	for i := 1; i <= 8; i++ {
		h.feed(fmt.Sprintf("[#2334880826 UDP steamid:76561198750092400@2.2.2.2:47386 'cs2commends.com'] %d reply timeouts, last recv 4143.9ms ago.  Sending keepalive.", i))
	}
	if !h.sent("addip 60 2.2.2.2") || !h.sent("kickid 17") {
		t.Fatalf("stuck with uid: %v", h.cmds)
	}

	h = newHarness(t, nil)
	h.feed("Accepting Steam Net connection #2570935650 UDP steamid:76561199508856674@5.255.117.135:43050")
	h.clock = h.clock.Add(30 * time.Second)
	for i := 1; i <= 8; i++ {
		if h.g.Inspect(fmt.Sprintf("[#2570935650 UDP steamid:76561199508856674@5.255.117.135:43050] %d reply timeouts, last recv 1%d000ms ago.  Sending keepalive.", i, i)) {
			t.Fatal("a userid-less holder's keepalive is noise from the first line")
		}
	}
	if !h.sent("addip 60 5.255.117.135") {
		t.Fatalf("stuck without uid: %v", h.cmds)
	}

	h = newHarness(t, nil)
	h.feed("CServerSideClientBase::Connect( name='Real', userid=30, fake=0, connectiontypeflags=4, chan->addr=6.6.6.7:1000 )")
	if !h.g.Inspect("[#8 UDP steamid:76561198345583467@6.6.6.7:1000 'Real'] 2 reply timeouts, last recv 10701.3ms ago.  Sending keepalive.") {
		t.Fatal("a game client's keepalive line stays visible")
	}

	h = newHarness(t, nil)
	h.feed("Accepting Steam Net connection #1 UDP steamid:1@3.3.3.3:1")
	h.clock = h.clock.Add(5 * time.Second)
	for i := 1; i <= 8; i++ {
		h.feed(fmt.Sprintf("[#1 UDP steamid:1@3.3.3.3:1] %d reply timeouts, last recv 1000ms ago.  Sending keepalive.", i))
	}
	if h.punitive() {
		t.Fatal("under grace must not trip")
	}
}

func TestSelfTestProbeAndWarning(t *testing.T) {
	h := newHarness(t, nil)
	h.feed("Host activate: Loading (de_dust2)")
	if !h.sent("echo BOTGUARD_CHANNEL_OK") {
		t.Fatal("probe not sent")
	}
	h.feed("echo BOTGUARD_CHANNEL_OK")
	if h.g.ChannelVerified() {
		t.Fatal("echoed input must not verify")
	}
	h.feed("BOTGUARD_CHANNEL_OK")
	if !h.g.ChannelVerified() {
		t.Fatal("marker must verify")
	}
	h = newHarness(t, nil)
	h.feed("Host activate: Loading (de_dust2)")
	h.clock = h.clock.Add(30 * time.Second)
	h.feed("some unrelated line")
	if !strings.Contains(h.out.String(), "[KL-GRD-03]") {
		t.Fatal("unverified channel must warn in block mode")
	}
}

func TestWhitelistsAndPrivateIP(t *testing.T) {
	h := newHarness(t, nil)
	for i := 1; i <= 4; i++ {
		h.feed(stray("10.0.0.9", 7000+i))
	}
	if h.punitive() || strings.Contains(h.out.String(), "tripped") {
		t.Fatal("whitelisted ip reacted")
	}
	h = newHarness(t, nil)
	h.feed("Accepting Steam Net connection #5 UDP steamid:76561198000000001@44.44.44.44:5")
	for i := 1; i <= 4; i++ {
		h.feed(stray("44.44.44.44", 7000+i))
	}
	if h.punitive() {
		t.Fatal("whitelisted steamid reacted")
	}
	h = newHarness(t, nil)
	for i := 1; i <= 4; i++ {
		h.feed(stray("172.18.0.1", 6000+i))
	}
	if !h.tripped("stray_no_connection") || h.sent("addip") || h.hit("stray_no_connection") == nil {
		t.Fatalf("private ip: tripped but no addip, hit reported: %v %s", h.cmds, h.out.String())
	}
}

func TestNoIPNoHitAndDisabledRule(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 3; i++ {
		h.feed("SV:  Forcing client reconnect (SIGNONSTATE_CONNECTED) for client 'ghost'")
	}
	if !h.tripped("reconnect_stuck") || h.hit("reconnect_stuck") != nil {
		t.Fatal("unregistered client trips but reports nothing")
	}
	off := false
	h = newHarness(t, func(c *configs.Guard) { c.Rules[3].Enabled = &off })
	for i := 1; i <= 6; i++ {
		h.feed(fmt.Sprintf("Receiving C2S_CONNECT [protocol 14175 0 auth 3] from 9.9.9.9:%d", 40000+i))
	}
	if h.punitive() {
		t.Fatal("disabled rule must be inert")
	}
}

func TestNodePresenceSilencesEggLineForPublicIPs(t *testing.T) {
	h := newHarness(t, nil)
	h.node = true
	for i := 1; i <= 4; i++ {
		h.feed(stray("77.238.141.49", 9100+i))
	}
	if strings.Contains(h.out.String(), "KL-GRD-01") || h.hit("stray_no_connection") == nil {
		t.Fatalf("node present: no egg line, hit still reported: %s", h.out.String())
	}
	for i := 1; i <= 4; i++ {
		h.feed(stray("172.18.0.1", 9200+i))
	}
	if !strings.Contains(h.out.String(), "KL-GRD-01") {
		t.Fatal("private ip is still the egg's to report")
	}
}

func TestHostNoticesMuteAndKick(t *testing.T) {
	h := newHarness(t, nil)
	h.feed("Accepting Steam Net connection #9 UDP steamid:76561198345583467@4.4.4.4:9")
	h.g.HostNotice(&proto.GuardBlock{IP: "4.4.4.4", Minutes: 30, Rule: "stray_no_connection", Src: "egg", Dry: true})
	if !strings.Contains(h.out.String(), "[KL-GRD-05] Host guard would drop 4.4.4.4 for 30m (stray_no_connection, by this server's report)") {
		t.Fatalf("dry notice: %s", h.out.String())
	}
	if !h.g.Inspect("[#9 UDP steamid:76561198345583467@4.4.4.4:9] 1 reply timeouts") {
		t.Fatal("a dry notice must not mute")
	}
	h.g.HostNotice(&proto.GuardBlock{IP: "4.4.4.4", Minutes: 30, Rule: "pps", Src: "host", UID: 12})
	if !strings.Contains(h.out.String(), "[KL-GRD-05] Host guard dropped 4.4.4.4 for 30m (pps, host rule, uid 12)") || !h.sent("kickid 12") {
		t.Fatalf("real notice: %s %v", h.out.String(), h.cmds)
	}
	for _, l := range []string{"anything 4.4.4.4 here", "[U:1:385317739] said hi", "steamid 76561198345583467 left"} {
		if h.g.Inspect(l) {
			t.Fatalf("must be muted: %q", l)
		}
	}
	if !h.g.Inspect("other client 5.5.5.5 talks") {
		t.Fatal("other clients stay visible")
	}
	h.clock = h.clock.Add(30*time.Minute + time.Second)
	if !h.g.Inspect("anything 4.4.4.4 here") {
		t.Fatal("mute must expire with the block")
	}

	h = newHarness(t, func(c *configs.Guard) { c.Action = "log" })
	h.g.HostNotice(&proto.GuardBlock{IP: "4.4.4.4", Minutes: 30, Rule: "idle", Src: "host", UID: 12})
	if h.sent("kickid") {
		t.Fatal("action=log never kicks")
	}
	silent := false
	h = newHarness(t, func(c *configs.Guard) { c.Rules[3].Log = &silent })
	h.g.HostNotice(&proto.GuardBlock{IP: "7.7.7.7", Minutes: 30, Rule: "connect_flood", Src: "egg"})
	h.g.HostNotice(&proto.GuardBlock{IP: "7.7.7.8", Minutes: 30, Rule: "tiny", Src: "host"})
	if strings.Contains(h.out.String(), "7.7.7.7") || !strings.Contains(h.out.String(), "7.7.7.8") {
		t.Fatalf("log:false rule notice must stay silent, others show: %s", h.out.String())
	}
}

func TestLogFalseRuleActsSilently(t *testing.T) {
	silent := false
	h := newHarness(t, func(c *configs.Guard) { c.Rules[3].Log = &silent })
	for i := 1; i <= 6; i++ {
		h.feed(fmt.Sprintf("Receiving C2S_CONNECT [protocol 14175 0 auth 3] from 9.9.9.9:%d", 40000+i))
	}
	if strings.Contains(h.out.String(), "KL-GRD-01") || !h.sent("addip 30 9.9.9.9") || h.hit("connect_flood") == nil {
		t.Fatalf("out=%s cmds=%v", h.out.String(), h.cmds)
	}
}

func TestActionLogReportsButNeverCommands(t *testing.T) {
	h := newHarness(t, func(c *configs.Guard) { c.Action = "log" })
	for i := 1; i <= 4; i++ {
		h.feed(stray("77.238.141.49", 9300+i))
	}
	if h.punitive() || h.hit("stray_no_connection") == nil {
		t.Fatal("log mode")
	}
}

// Every one of the guard's per-source maps is keyed by something a stranger
// picks. A flood that never joins and never leaves used to leave all of it
// behind for the life of the container.
func TestStateDoesNotGrowForever(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 500; i++ {
		ip := fmt.Sprintf("203.0.%d.%d", i/256, i%256)
		h.feed("Accepting Steam Net connection from steamid:7656119800000" + fmt.Sprintf("%04d", i) + "@" + ip + ":27005")
		h.feed("Ignored bad " + ip + ":27015")
	}
	before := h.g.StateSize()
	if before == 0 {
		t.Fatal("nothing was recorded, the test proves nothing")
	}
	h.clock = h.clock.Add(2 * time.Hour) // past every window and the stranger ttl
	h.feed("Server is hibernating")
	if after := h.g.StateSize(); after >= before {
		t.Fatalf("state never shrinks: %d entries before, %d after", before, after)
	}
}

// A muted client costs one substring scan per console line, so the table
// cannot be allowed to grow with the size of a flood.
func TestMuteTableIsBounded(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 4000; i++ {
		h.g.HostNotice(&proto.GuardBlock{IP: fmt.Sprintf("198.51.%d.%d", i/256, i%256), Minutes: 60, Rule: "pps"})
	}
	if n := h.g.MuteSize(); n > 256 {
		t.Fatalf("mute table grew to %d keys", n)
	}
}

// A player picks their own name, and the engine prints that name inside its
// own telemetry lines. If the rules match anywhere in a line, a crafted
// name forges a rule hit naming any address the attacker likes, and the
// egg reports it to the node, which drops it at the host firewall. That is
// a remote "block any IP" primitive handed to anyone who can join.
func TestACraftedNameCannotForgeARuleHit(t *testing.T) {
	h := newHarness(t, nil)
	// the engine's reply-timeout line, with the name field under the
	// attacker's control (the fixture format this suite already uses)
	const victim = "9.9.9.9"
	name := "Ignored bad " + victim + ":1 Invalid a byte"
	for i := 1; i <= 4; i++ {
		h.feed(fmt.Sprintf("[#99 UDP steamid:76561198345583467@80.250.101.109:53090 '%s'] %d reply timeouts, last recv 11000ms ago.  Sending keepalive.", name, i))
	}
	for _, m := range h.msgs {
		hit, ok := m.(*proto.GuardHit)
		if ok && hit.IP == victim {
			t.Fatalf("a name forged a %s hit against %s", hit.Rule, victim)
		}
	}
	for _, c := range h.cmds {
		if strings.Contains(c, victim) {
			t.Fatalf("a name forged the console command %q", c)
		}
	}
}

// The same forgery against the steamid map: bind an attacker's address to a
// whitelisted SteamID and every later rule trip on it is ignored.
func TestACraftedNameCannotPoisonTheSteamIDMap(t *testing.T) {
	h := newHarness(t, func(c *configs.Guard) { c.WhitelistSteamIDs = []string{"76561197960287930"} })
	name := "Accepting Steam Net connection #1 UDP steamid:76561197960287930@6.6.6.6:1"
	h.feed(fmt.Sprintf("[#99 UDP steamid:76561198345583467@80.250.101.109:53090 '%s'] 1 reply timeouts, last recv 11000ms ago.  Sending keepalive.", name))
	// 6.6.6.6 must still be blockable: the name did not whitelist it
	for i := 0; i < 4; i++ {
		h.feed(fmt.Sprintf("[packet] Ignored bad 6.6.6.6:%d.  Invalid lead byte 0x00", 9000+i))
	}
	blocked := false
	for _, m := range h.msgs {
		if hit, ok := m.(*proto.GuardHit); ok && hit.IP == "6.6.6.6" {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("a name bound an attacker address to a whitelisted SteamID: it is now immune")
	}
}
