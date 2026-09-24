// SPDX-License-Identifier: GPL-3.0-or-later

// Package guard is the in-container bot guard: it reads the server log,
// keeps a picture of every client, trips behaviour rules, and reacts with
// console commands. It cannot drop packets; the node's guard does that from
// the hits and lifecycle events this package reports.
package guard

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/configs"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/netx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// Rule is the tunable part of one behaviour rule.
type Rule struct {
	Threshold  int
	Window     time.Duration
	BanMinutes int
	Log        bool
}

// Guard is one server's guard state. Safe for concurrent use.
type Guard struct {
	Action     string // log | kick | block
	BanMinutes int
	Cooldown   time.Duration
	StuckGrace time.Duration
	Rules      map[string]Rule // absent = disabled
	Log        *logx.Console
	Now        func() time.Time
	// Send writes a console command to the server.
	Send func(cmd string)
	// Report sends a message to the node; nil when there is no node.
	Report func(msg proto.Message)
	// NodeActive says whether the node will report its own block for a
	// public ip, which makes the egg's own line redundant.
	NodeActive func() bool

	mu          sync.Mutex
	wlIPs       []netip.Prefix
	wlSteamIDs  map[string]bool
	clients     map[int]*client
	ip2uid      map[string]int
	name2uid    map[string]int
	ipTrusted   map[string]bool
	connSince   map[string]time.Time
	ip2steamid  map[string]string
	counters    map[string]*counter
	cooldown    map[string]time.Time
	mute        map[string]time.Time
	probeSent   time.Time
	swept       time.Time
	channelOK   bool
	channelWarn bool
}

type client struct {
	ip, name string
	trusted  bool
}

type counter struct {
	start time.Time
	n     int
}

const steamID64Base = 76561197960265728

// maxRuleWindow is the widest window any rule can have, so a counter older
// than this can never count towards anything again. strangerTTL is how long
// an address that connected but never joined is remembered.
const (
	maxRuleWindow = 5 * time.Minute
	strangerTTL   = 15 * time.Minute
	sweepEvery    = 30 * time.Second
)

// New builds a guard from guard.json.
func New(cfg configs.Guard, log *logx.Console) (*Guard, error) {
	g := &Guard{
		Action: cfg.Action, BanMinutes: cfg.BanMinutes,
		Cooldown: time.Duration(cfg.CooldownSecs) * time.Second, StuckGrace: time.Duration(cfg.StuckGraceSecs) * time.Second,
		Rules: map[string]Rule{}, Log: log, Now: time.Now,
		wlSteamIDs: map[string]bool{}, clients: map[int]*client{}, ip2uid: map[string]int{}, name2uid: map[string]int{},
		ipTrusted: map[string]bool{}, connSince: map[string]time.Time{}, ip2steamid: map[string]string{},
		counters: map[string]*counter{}, cooldown: map[string]time.Time{}, mute: map[string]time.Time{},
	}
	switch g.Action {
	case "log", "kick", "block":
	default:
		g.Action = "log"
	}
	if g.BanMinutes <= 0 {
		g.BanMinutes = 10
	}
	for _, r := range cfg.Rules {
		if !r.IsEnabled() {
			continue
		}
		rule := Rule{Threshold: r.Threshold, Window: time.Duration(r.WindowSecs) * time.Second, BanMinutes: r.BanMinutes, Log: r.Logs()}
		if rule.Threshold < 1 {
			rule.Threshold = 1
		}
		if rule.Window <= 0 {
			rule.Window = 10 * time.Second
		}
		if rule.BanMinutes <= 0 {
			rule.BanMinutes = g.BanMinutes
		}
		g.Rules[r.Name] = rule
	}
	var err error
	if g.wlIPs, err = netx.ParsePrefixList(strings.Join(cfg.WhitelistIPs, " ")); err != nil {
		return nil, fmt.Errorf("whitelist_ips: %w", err)
	}
	for _, s := range cfg.WhitelistSteamIDs {
		g.wlSteamIDs[strings.TrimSpace(s)] = true
	}
	return g, nil
}

// RuleCount is for the startup line.
func (g *Guard) RuleCount() int { return len(g.Rules) }

// ChannelVerified reports whether the console command channel echoed the probe.
func (g *Guard) ChannelVerified() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.channelOK
}

var (
	reConnect  = regexp.MustCompile(`^CServerSideClientBase::Connect\( name='(.*)', userid=([0-9]+),.*chan->addr=([0-9.]+):[0-9]+`)
	reSignon   = regexp.MustCompile(`Client ([0-9]+) '(.*)' signon state [A-Z_]+ -> SIGNONSTATE_([A-Z]+)`)
	reChat     = regexp.MustCompile(`^\[(All|Team) Chat\]\[(.*) \([0-9]+\)\]: `)
	reDropped  = regexp.MustCompile(`^SV:  Dropped client '(.*)' from server`)
	reAccept   = regexp.MustCompile(`^Accepting Steam Net connection [^\']*@([0-9.]+):[0-9]+`)
	reSteamID  = regexp.MustCompile(`steamid:([0-9]{17})@`)
	reBadHost  = regexp.MustCompile(`Ignored bad ([0-9.]+):[0-9]+`)
	reC2S      = regexp.MustCompile(`^Receiving C2S_CONNECT .* from ([0-9.]+):[0-9]+`)
	reRcon     = regexp.MustCompile(`^Banning ([0-9.]+) for rcon hacking attempts`)
	reForce    = regexp.MustCompile(`^(?:SV:\s+)?Forcing client reconnect .* for client '(.*)'`)
	reMove     = regexp.MustCompile(`^\[(.*)\] sent command that failed delta decode`)
	reKeep     = regexp.MustCompile(`@([0-9.]+):[0-9]+`)
	reInvalidB = regexp.MustCompile(`Invalid \w+ byte`)
	// the engine prefixes some of its lines with a short channel tag:
	// "[Data] Ignored bad ...", "[packet] Ignored bad ...". The tag is
	// plain and short, which is what makes it safe to step over.
	reChannelTag = regexp.MustCompile(`^\[[A-Za-z0-9_. -]{1,20}\]\s*`)
)

// engineText strips the engine's channel tag so a rule can be matched at
// the START of what the engine itself wrote.
//
// This is the difference between a rule and a forgery. A player picks their
// own name, and the engine prints that name inside its own telemetry, for
// example a reply-timeout line:
//
//	[#99 UDP steamid:7656...@1.2.3.4:53090 'NAME'] 2 reply timeouts ...
//
// A player called "Ignored bad 9.9.9.9:1 Invalid a byte" used to trip the
// malformed_packet rule against 9.9.9.9, and because a hit is reported to
// the node before the action is even considered, the host firewall dropped
// that address for a day. Anyone who could join could block anyone.
//
// A name can contain anything, but it can never be the first thing on the
// line: the engine's own keyword is. That is the invariant these rules now
// rest on.
func engineText(line string) string {
	return line[len(reChannelTag.FindString(line)):]
}

// Inspect feeds one server line. It returns false when the line is noise
// the guard accounts for itself (stray/malformed junk, a userid-less holder's
// keepalives, anything naming a muted client) and should not reach the panel.
func (g *Guard) Inspect(line string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	keep := g.inspect(line)
	if g.mutedLine(line) {
		return false
	}
	return keep
}

func (g *Guard) inspect(line string) bool {
	now := g.Now()
	if now.Sub(g.swept) >= sweepEvery {
		g.swept = now
		g.sweep(now)
	}
	g.selftest(line, now)

	// everything below reads what the engine wrote, with its channel tag
	// stepped over, so no pattern can be satisfied by a player's name
	// carried inside the line
	body := engineText(line)
	if m := reConnect.FindStringSubmatch(body); m != nil {
		uid, _ := strconv.Atoi(m[2])
		g.clients[uid] = &client{ip: m[3], name: m[1]}
		g.ip2uid[m[3]] = uid
		g.name2uid[m[1]] = uid
		return true
	}
	if m := reSignon.FindStringSubmatch(body); m != nil {
		uid, _ := strconv.Atoi(m[1])
		g.name2uid[m[2]] = uid
		if m[3] == "FULL" {
			c := g.clients[uid]
			if c == nil {
				c = &client{name: m[2]}
				g.clients[uid] = c
			}
			c.trusted = true
			if c.ip != "" {
				g.ipTrusted[c.ip] = true
				g.report(&proto.GuardEvent{Kind: proto.EventJoin, IP: c.ip, UID: uid})
			}
		}
		return true
	}
	if m := reChat.FindStringSubmatch(line); m != nil {
		if uid, ok := g.name2uid[m[2]]; ok {
			if c := g.clients[uid]; c != nil && c.ip != "" {
				g.report(&proto.GuardEvent{Kind: proto.EventChat, IP: c.ip, UID: uid})
			}
		}
		return true
	}
	if m := reDropped.FindStringSubmatch(body); m != nil {
		if uid, ok := g.name2uid[m[1]]; ok {
			if c := g.clients[uid]; c != nil && c.ip != "" {
				g.report(&proto.GuardEvent{Kind: proto.EventLeave, IP: c.ip, UID: uid})
				g.forget(c.ip, uid)
			}
			delete(g.clients, uid)
		}
		delete(g.name2uid, m[1])
		return true
	}
	if m := reAccept.FindStringSubmatch(body); m != nil {
		if _, seen := g.connSince[m[1]]; !seen {
			g.connSince[m[1]] = now
		}
		if s := reSteamID.FindStringSubmatch(body); s != nil {
			g.ip2steamid[m[1]] = s[1]
		}
		return true
	}

	// --- the rules ---
	if strings.HasPrefix(body, "Ignored bad") && strings.Contains(body, "Stray data packet from host with no connection") {
		if m := reBadHost.FindStringSubmatch(body); m != nil {
			// a dropped or reconnecting real player looks exactly like this for a moment
			if !g.ipTrusted[m[1]] && g.bump("stray_no_connection", m[1], now) {
				g.react("stray_no_connection", 0, m[1], now)
			}
			return false
		}
	}
	if strings.HasPrefix(body, "Ignored bad") && reInvalidB.MatchString(body) {
		if m := reBadHost.FindStringSubmatch(body); m != nil {
			if g.bump("malformed_packet", m[1], now) {
				g.react("malformed_packet", 0, m[1], now)
			}
			return false
		}
	}
	if m := reC2S.FindStringSubmatch(body); m != nil {
		if !g.ipTrusted[m[1]] && g.bump("connect_flood", m[1], now) {
			g.react("connect_flood", g.ip2uid[m[1]], m[1], now)
		}
		return true
	}
	if m := reRcon.FindStringSubmatch(body); m != nil {
		if g.bump("rcon_bruteforce", m[1], now) {
			g.react("rcon_bruteforce", 0, m[1], now)
		}
		return true
	}
	if m := reForce.FindStringSubmatch(line); m != nil {
		uid := g.name2uid[m[1]]
		if !g.trusted(uid) && g.bump("reconnect_stuck", m[1], now) {
			g.react("reconnect_stuck", uid, g.ipOf(uid), now)
		}
		return true
	}
	if m := reMove.FindStringSubmatch(line); m != nil {
		uid := g.name2uid[m[1]]
		if !g.trusted(uid) && g.bump("move_not_joined", m[1], now) {
			g.react("move_not_joined", uid, g.ipOf(uid), now)
		}
		return true
	}
	if strings.Contains(line, "Sending keepalive") {
		if m := reKeep.FindStringSubmatch(line); m != nil {
			ip := m[1]
			uid := g.ip2uid[ip]
			if g.trusted(uid) || g.ipTrusted[ip] {
				return true
			}
			since, ok := g.connSince[ip]
			if !ok || now.Sub(since) >= g.StuckGrace {
				if g.bump("stuck_no_full", ip, now) {
					g.react("stuck_no_full", uid, ip, now)
				}
			}
			// no userid: a transport-only holder, its timeouts are pure noise
			return uid != 0
		}
	}
	return true
}

func (g *Guard) trusted(uid int) bool {
	c := g.clients[uid]
	return c != nil && c.trusted
}

func (g *Guard) ipOf(uid int) string {
	if c := g.clients[uid]; c != nil {
		return c.ip
	}
	return ""
}

// bump is the sliding-window counter; true when the rule's threshold is hit.
func (g *Guard) bump(rule, key string, now time.Time) bool {
	r, ok := g.Rules[rule]
	if !ok {
		return false
	}
	ck := rule + "|" + key
	c := g.counters[ck]
	if c == nil || now.Sub(c.start) > r.Window {
		g.counters[ck] = &counter{start: now, n: 1}
		return 1 >= r.Threshold
	}
	c.n++
	return c.n >= r.Threshold
}

func (g *Guard) whitelisted(ip string) bool {
	if a, ok := netx.ParseAddr(ip); ok && netx.InAny(a, g.wlIPs) {
		return true
	}
	if sid := g.ip2steamid[ip]; sid != "" && g.wlSteamIDs[sid] {
		return true
	}
	return false
}

func (g *Guard) publicIP(ip string) bool {
	a, ok := netx.ParseAddr(ip)
	return ok && netx.IsPublic(a, netip.Prefix{})
}

// react fires a rule: console line, report to the node, then the action.
func (g *Guard) react(rule string, uid int, ip string, now time.Time) {
	if ip != "" && g.whitelisted(ip) {
		return
	}
	if uid != 0 && g.trusted(uid) {
		return
	}
	ck := ip
	if ck == "" {
		ck = "uid" + strconv.Itoa(uid)
	}
	if last, ok := g.cooldown[ck]; ok && now.Sub(last) < g.Cooldown {
		return
	}
	g.cooldown[ck] = now
	r := g.Rules[rule]
	who := ip
	if who == "" {
		who = "?"
	}
	if uid != 0 {
		who += fmt.Sprintf(" (uid %d)", uid)
	}
	// one line per hit, no hint/docs lines: this fires often. Skipped when the
	// rule is silent, or when the node reports its own block for this ip.
	nodeActs := ip != "" && g.publicIP(ip) && g.NodeActive != nil && g.NodeActive()
	if r.Log && !nodeActs {
		g.Log.Logf(logx.Guard, "[KL-GRD-01] Bot-guard tripped '%s' from %s (action=%s, block %dm)", rule, who, g.Action, r.BanMinutes)
	}
	if ip != "" {
		g.report(&proto.GuardHit{IP: ip, Minutes: r.BanMinutes, Rule: rule, UID: uid})
	}
	switch g.Action {
	case "kick":
		if uid != 0 {
			g.send(fmt.Sprintf("kickid %d", uid))
		}
	case "block":
		if uid != 0 {
			g.send(fmt.Sprintf("kickid %d", uid))
		}
		// addip on a private/bridge address would ban the whole docker-proxy path
		if ip != "" && g.publicIP(ip) {
			g.send(fmt.Sprintf("addip %d %s", r.BanMinutes, ip))
			g.muteAdd(ip, r.BanMinutes, now)
		}
	}
}

func (g *Guard) send(cmd string) {
	if g.Send != nil {
		g.Send(cmd)
	}
}

func (g *Guard) report(m proto.Message) {
	if g.Report != nil {
		g.Report(m)
	}
}

// maxMute bounds the mute table. Every muted key costs one substring scan
// per console line, so an unbounded table turns a flood into a console the
// egg cannot keep up with: measured, 5000 keys is 350us a line, which is
// slower than the server prints. Muting is console cosmetics, so past this
// many blocked clients the newest ones simply stay visible.
const maxMute = 256

// muteAdd silences every later line naming a blocked client: its ip and
// both SteamID forms, until the block ends.
func (g *Guard) muteAdd(ip string, minutes int, now time.Time) {
	if minutes <= 0 {
		minutes = g.BanMinutes
	}
	g.sweepMute(now)
	if len(g.mute) >= maxMute {
		return
	}
	until := now.Add(time.Duration(minutes) * time.Minute)
	g.mute[ip] = until
	if sid := g.ip2steamid[ip]; len(sid) == 17 {
		g.mute[sid] = until
		if n, err := strconv.ParseInt(sid, 10, 64); err == nil {
			g.mute[fmt.Sprintf("[U:1:%d]", n-steamID64Base)] = until
		}
	}
}

// forget drops what a client left behind. Every one of these maps is keyed
// by something a stranger picks (an address, a name), and without this a
// flood of a hundred thousand sources stays in memory for the life of the
// container.
func (g *Guard) forget(ip string, uid int) {
	delete(g.ip2uid, ip)
	delete(g.ipTrusted, ip)
	delete(g.connSince, ip)
	delete(g.ip2steamid, ip)
}

// sweep drops per-source state that no leave line will ever clear: a
// stranger that sprayed packets and went away never "drops". Counters go
// once their window is past, cooldowns once they have lapsed.
func (g *Guard) sweep(now time.Time) {
	for key, c := range g.counters {
		if now.Sub(c.start) > maxRuleWindow {
			delete(g.counters, key)
		}
	}
	for key, until := range g.cooldown {
		if !until.After(now) {
			delete(g.cooldown, key)
		}
	}
	g.sweepMute(now)
	// what never joined leaves no trace behind either
	for ip, since := range g.connSince {
		if now.Sub(since) > strangerTTL {
			delete(g.connSince, ip)
			delete(g.ip2steamid, ip)
			delete(g.ipTrusted, ip)
			if uid, ok := g.ip2uid[ip]; ok {
				if _, live := g.clients[uid]; !live {
					delete(g.ip2uid, ip)
				}
			}
		}
	}
}

// sweepMute drops what has expired. Called where a mute is added rather
// than per line, so the per-line path only ever walks live keys.
func (g *Guard) sweepMute(now time.Time) {
	for key, until := range g.mute {
		if !until.After(now) {
			delete(g.mute, key)
		}
	}
}

func (g *Guard) mutedLine(line string) bool {
	if len(g.mute) == 0 {
		return false
	}
	now := g.Now()
	for key, until := range g.mute {
		if !until.After(now) {
			delete(g.mute, key)
			continue
		}
		if strings.Contains(line, key) {
			return true
		}
	}
	return false
}

// selftest proves the command channel once the engine is live: the probe
// must come back as its own exact line, the echoed input does not count.
func (g *Guard) selftest(line string, now time.Time) {
	if line == "BOTGUARD_CHANNEL_OK" {
		if !g.channelOK {
			g.Log.Log(logx.Success, "Bot-guard command channel verified: kick/block enforcement is live")
		}
		g.channelOK = true
		return
	}
	if g.probeSent.IsZero() {
		if strings.Contains(line, "Host activate") || strings.Contains(line, "hibernating") || strings.Contains(line, "Accepting Steam Net connection") {
			g.probeSent = now
			g.send("echo BOTGUARD_CHANNEL_OK")
		}
		return
	}
	if !g.channelOK && !g.channelWarn && g.Action != "log" && now.Sub(g.probeSent) > 20*time.Second {
		g.channelWarn = true
		g.Log.Code(logx.Warning, "KL-GRD-03", "Bot-guard command channel UNVERIFIED after 20s: action="+g.Action+" may not be enforced",
			"Detections are still logged, but kick/addip could be silently dropped",
			"Check that the server reads console input")
	}
}

// HostNotice handles a block the node made for this server.
func (g *Guard) HostNotice(b *proto.GuardBlock) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if b.IP == "" {
		return
	}
	src := "host rule"
	if b.Src == "egg" {
		if r, ok := g.Rules[b.Rule]; ok && !r.Log {
			return // a rule this server keeps silent stays silent when the node acts on it
		}
		src = "by this server's report"
	}
	who := b.IP
	detail := fmt.Sprintf("%s, %s", b.Rule, src)
	if b.UID != 0 {
		detail += fmt.Sprintf(", uid %d", b.UID)
	}
	if b.Dry {
		g.Log.Logf(logx.Guard, "[KL-GRD-05] Host guard would drop %s for %dm (%s) - block_mode=log on the node", who, b.Minutes, detail)
		return
	}
	g.Log.Logf(logx.Guard, "[KL-GRD-05] Host guard dropped %s for %dm (%s)", who, b.Minutes, detail)
	g.muteAdd(b.IP, b.Minutes, g.Now())
	// the node names the client it caught: drop it now instead of waiting out its timeout
	if b.UID != 0 && g.Action != "log" {
		g.send(fmt.Sprintf("kickid %d", b.UID))
	}
}

// StateSize is how many per-source entries the guard is holding. Tests use
// it to prove the state shrinks again; nothing else should care.
func (g *Guard) StateSize() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.clients) + len(g.ip2uid) + len(g.name2uid) + len(g.ipTrusted) +
		len(g.connSince) + len(g.ip2steamid) + len(g.counters) + len(g.cooldown) + len(g.mute)
}

// MuteSize is how many keys the per-line mute scan walks.
func (g *Guard) MuteSize() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.mute)
}
