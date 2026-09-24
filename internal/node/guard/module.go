// SPDX-License-Identifier: GPL-3.0-or-later

package guard

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/netx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// Client is a joined player the heuristics watch.
type Client struct {
	Container string    `json:"container"`
	UID       int       `json:"uid"`
	Since     time.Time `json:"since"`
	Strikes   int       `json:"strikes"`
}

// Module is the guard feature.
type Module struct {
	Log *slog.Logger
	// FW is the firewall; nil = the real one, opened in Run.
	FW Firewall
	// Now is the clock (tests).
	Now func() time.Time
	// StateDir overrides StateDir (tests).
	StateDir string

	cfg       Config
	bridge    netip.Prefix
	whitelist []netip.Prefix
	quiet     map[string]bool
	offenders *Offenders
	rates     *RatesLog

	mu        sync.Mutex
	clients   map[netip.Addr]*Client
	portOwner map[uint16]string
	lastPorts string
	sample    RateSample // newest dump from the idle sampler
	sampleAt  time.Time
}

// New returns the module.
func New(log *slog.Logger) *Module {
	return &Module{Log: log, Now: time.Now, clients: map[netip.Addr]*Client{}, portOwner: map[uint16]string{}}
}

func (m *Module) Name() string { return "guard" }

func (m *Module) Describe() string {
	return "Host Guard: nftables packet drops on the game ports, fed by the eggs and by traffic heuristics"
}

func (m *Module) Questions() []module.Question { return Questions() }

// Notes is what the installer prints once the module is on.
func (m *Module) Notes() []string {
	return []string{
		"Turn the bot guard on in your servers (ENABLE_GUARD=1) so their detections reach the node; the node does the blocking, the servers only report.",
		"Only the game ports of the CS2 containers are filtered, in the node's own nftables table. SSH, the panel and everything else are untouched.",
	}
}

// setup parses the config into the module's fields.
func (m *Module) setup(c *core.Core) error {
	m.cfg = Defaults()
	if err := c.Config(m.Name(), &m.cfg); err != nil {
		return err
	}
	if err := m.cfg.Check(); err != nil {
		return err
	}
	var err error
	if m.bridge, err = netip.ParsePrefix(m.cfg.DockerBridgeSubnet); err != nil {
		return fmt.Errorf("docker_bridge_subnet: %w", err)
	}
	if m.whitelist, err = netx.ParsePrefixList(m.cfg.WhitelistIPs); err != nil {
		return fmt.Errorf("whitelist_ips: %w", err)
	}
	m.quiet = map[string]bool{}
	for _, r := range strings.Fields(m.cfg.QuietRules) {
		m.quiet[r] = true
	}
	dir := m.StateDir
	if dir == "" {
		dir = StateDir
	}
	m.offenders = LoadOffenders(filepath.Join(dir, "offenders.json"), time.Duration(m.cfg.RepeatWindowHours)*time.Hour, m.cfg.MaxBanMinutes)
	if m.cfg.RatesLog != "" {
		m.rates = &RatesLog{Path: m.cfg.RatesLog, MaxSources: m.cfg.RatesLogMaxSources}
	}
	return nil
}

// allowList is the bridge plus the whitelist: never touched by any rule.
func (m *Module) allowList() []netip.Prefix {
	return append([]netip.Prefix{m.bridge}, m.whitelist...)
}

func (m *Module) whitelisted(ip netip.Addr) bool { return netx.InAny(ip, m.whitelist) }

// Run serves until ctx ends.
func (m *Module) Run(ctx context.Context, c *core.Core) error {
	if err := m.setup(c); err != nil {
		return err
	}
	if m.FW == nil {
		fw, err := NewFirewall()
		if err != nil {
			return err
		}
		m.FW = fw
	}
	recreated, err := m.FW.Apply(m.cfg, m.allowList())
	if err != nil {
		return fmt.Errorf("nftables: %w", err)
	}
	if recreated {
		m.Log.Info("nftables table cs2guard created", "rules", RuleTotal)
	} else {
		m.Log.Info("nftables table cs2guard refreshed, blocks kept", "rules", RuleTotal)
	}
	m.Log.Info("guard up", "mode", m.cfg.BlockMode, "udp_pps", m.cfg.UDPPPSLimit, "rcon_syn_per_min", m.cfg.RconSynPerMinute, "max_ban_min", m.cfg.MaxBanMinutes, "bridge", m.bridge.String())

	c.ProvideStatus(m.Name(), m.status)
	c.Handle(proto.TypeGuardHit, func(container string, msg proto.Message) { m.onHit(c, container, msg.(*proto.GuardHit)) })
	c.Handle(proto.TypeGuardEvent, func(container string, msg proto.Message) { m.onEvent(c, container, msg.(*proto.GuardEvent)) })
	m.refreshPorts(c)

	go func() {
		if err := readKmsg(ctx, func(line string) { m.onKmsg(c, line) }); err != nil {
			m.Log.Warn("kernel log unreadable, static-rule blocks will not reach the consoles", "err", err)
		}
	}()
	go m.idleLoop(ctx, c)
	defer m.offenders.Flush(m.Now()) // whatever the last blocks recorded

	events, unsub := c.Subscribe()
	defer unsub()
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if ev.Kind == core.Start || ev.Kind == core.Die {
				m.refreshPorts(c)
			}
		case <-tick.C:
			if !m.FW.Loaded() {
				m.Log.Warn("cs2guard table vanished (another tool flushed nftables?), reloading")
				if _, err := m.FW.Apply(m.cfg, m.allowList()); err != nil {
					m.Log.Error("reload failed", "err", err)
					continue
				}
				m.lastPorts = ""
			}
			m.refreshPorts(c)
		}
	}
}

// refreshPorts protects the union of every container's published ports and
// remembers which container owns which port for kernel-log notices.
func (m *Module) refreshPorts(c *core.Core) {
	owner := map[uint16]string{}
	var ports []uint16
	for _, cont := range c.Containers() {
		for _, p := range cont.Ports {
			if _, seen := owner[p]; !seen {
				owner[p] = cont.Name
				ports = append(ports, p)
			}
		}
	}
	key := fmt.Sprint(ports)
	m.mu.Lock()
	m.portOwner = owner
	changed := key != m.lastPorts
	m.mu.Unlock()
	if !changed {
		return
	}
	if err := m.FW.RefreshPorts(ports); err != nil {
		m.Log.Warn("port set refresh failed", "err", err)
		return
	}
	m.mu.Lock()
	m.lastPorts = key
	m.mu.Unlock()
	m.Log.Info("protected ports", "ports", ports)
}

// reason is the one-line why for the rates log and `cs2node blocks`: the
// rule, the server it happened on, and whatever measurement there was.
func reason(rule, container, detail string) string {
	parts := []string{rule}
	if container != "" {
		parts = append(parts, container)
	}
	if detail != "" {
		parts = append(parts, detail)
	}
	return strings.Join(parts, ", ")
}

// notify tells the container's egg about a block (or a dry one).
func (m *Module) notify(c *core.Core, container string, b *proto.GuardBlock) {
	if container == "" || m.quiet[b.Rule] {
		return
	}
	c.Send(container, b)
}

// block applies one decision from the egg or the heuristics: whitelist,
// dry mode, escalation, the drop, the log line, the notice.
// detail is the measurement behind a heuristic block ("64 pps, 79 B/pkt"),
// never the rule or the container: those are their own notice fields, and
// repeating them is what made the Discord embed say everything twice.
func (m *Module) block(c *core.Core, container string, ip netip.Addr, minutes int, rule, src string, uid int, detail string) bool {
	if m.whitelisted(ip) {
		m.Log.Info("whitelisted, not blocked", "ip", ip, "rule", rule, "container", container)
		return false
	}
	minutes = Clamp(minutes, m.cfg.MaxBanMinutes)
	why := reason(rule, container, detail)
	if m.cfg.BlockMode == "log" {
		m.Log.Info("would block (block_mode=log)", "ip", ip, "minutes", minutes, "rule", rule, "container", container, "why", why)
		m.notify(c, container, &proto.GuardBlock{IP: ip.String(), Minutes: minutes, Rule: rule, Src: src, UID: uid, Dry: true})
		return false
	}
	minutes, count := m.offenders.Escalate(ip.String(), minutes, m.Now())
	if count > 1 {
		why += fmt.Sprintf(", repeat #%d", count)
	}
	if err := m.FW.Block(ip, time.Duration(minutes)*time.Minute); err != nil {
		m.Log.Warn("block failed", "ip", ip, "err", err)
		return false
	}
	m.Log.Info("blocked", "ip", ip, "minutes", minutes, "rule", rule, "container", container, "why", why)
	now := m.Now()
	m.offenders.Record(ip.String(), why, now.Add(time.Duration(minutes)*time.Minute), now)
	m.rates.Block(now, ip, minutes, why)
	c.Notify(core.NoticeGuardBlock, container, map[string]any{
		"ip": ip.String(), "minutes": minutes, "rule": rule, "why": why,
		"src": src, "repeat": count, "detail": detail})
	m.notify(c, container, &proto.GuardBlock{IP: ip.String(), Minutes: minutes, Rule: rule, Src: src, UID: uid})
	return true
}

// onHit is an egg's rule trip.
func (m *Module) onHit(c *core.Core, container string, h *proto.GuardHit) {
	ip, ok := netx.ParseAddr(h.IP)
	if !ok {
		m.Log.Warn("egg reported an invalid ip", "container", container, "ip", h.IP)
		return
	}
	if !netx.IsPublic(ip, m.bridge) {
		m.Log.Info("egg reported a private/bridge ip, ignored", "container", container, "ip", ip, "rule", h.Rule)
		return
	}
	// the rule name reaches the rates log, the blocks table and a Discord
	// embed, and it arrives from inside a container: a newline in it would
	// forge a whole log line, and an escape sequence would reach the root
	// operator's terminal
	m.block(c, container, ip, h.Minutes, cleanField(h.Rule, 64), "egg", h.UID, "")
}

// cleanField bounds and de-fangs a string that came over the socket.
func cleanField(s string, max int) string {
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			continue
		}
		if n == max {
			break
		}
		b.WriteRune(r)
		n++
	}
	if b.Len() == 0 {
		return "unnamed"
	}
	return b.String()
}

// onEvent is a client lifecycle fact from an egg.
func (m *Module) onEvent(c *core.Core, container string, e *proto.GuardEvent) {
	ip, ok := netx.ParseAddr(e.IP)
	if !ok || !netx.IsPublic(ip, m.bridge) {
		return
	}
	switch e.Kind {
	case proto.EventJoin:
		// Membership skips the per-port stranger limiter, on every port on
		// the node, and the node cannot check that anyone actually joined:
		// the claim comes from the container. One server saying so for a
		// botnet's addresses would exempt them for every other tenant, so
		// a container gets a player slot count, not an open door.
		if !m.claimSlot(container, ip, e.UID) {
			m.Log.Warn("too many exempt addresses for one server, ignoring join",
				"container", container, "ip", ip, "max", MaxExemptPerServer)
			return
		}
		// the set entry outlives the leave on purpose: a reconnect under a flood must pass
		if err := m.FW.AddPlayer(ip, PlayerExempt); err != nil {
			m.Log.Warn("players set update failed", "ip", ip, "err", err)
		}
	case proto.EventLeave:
		m.mu.Lock()
		delete(m.clients, ip)
		m.mu.Unlock()
	case proto.EventChat:
		m.chatCheck(c, container, ip, e.UID)
	}
}

// chatCheck: a chat right after joining from a client whose packets carry
// no usercmds is an ad bot saying its piece.
func (m *Module) chatCheck(c *core.Core, container string, ip netip.Addr, uid int) {
	m.mu.Lock()
	cl, ok := m.clients[ip]
	m.mu.Unlock()
	if !ok {
		return
	}
	age := m.Now().Sub(cl.Since)
	if age > time.Duration(m.cfg.ChatEarlySecs)*time.Second {
		return
	}
	// the idle sampler already dumps both rate sets every few seconds, and
	// that dump walks two 65535-element nftables sets under the firewall
	// lock. Doing it again per chat message is the same work at the rate
	// players talk, so the cached one is used: a few seconds of staleness
	// does not change a "chatted right after joining" verdict.
	sample := m.lastSample()
	if sample == nil {
		// the sampler has not produced one yet, or has stopped: fall back
		// to a live dump rather than miss the check
		var err error
		if sample, err = m.FW.RateSample(); err != nil {
			return
		}
		m.cacheSample(sample, m.Now())
	}
	cnt, ok := sample[ip]
	if !ok || cnt.Packets < 30 {
		return // too few packets to judge
	}
	bpp := int(cnt.Bytes / cnt.Packets)
	if bpp >= m.cfg.IdleBytesMin {
		return
	}
	if m.block(c, container, ip, m.cfg.IdleBanMinutes, "chatbot", "host", uid, fmt.Sprintf("chat %ds after join at %d B/pkt, uid %d", int(age.Seconds()), bpp, uid)) {
		m.mu.Lock()
		delete(m.clients, ip)
		m.mu.Unlock()
	}
}

// cacheSample keeps the newest rate dump for the cheap readers.
func (m *Module) cacheSample(s RateSample, now time.Time) {
	m.mu.Lock()
	m.sample, m.sampleAt = s, now
	m.mu.Unlock()
}

// lastSample is the newest rate dump, or nil when the sampler has not run
// or has stopped: a stale dump would keep answering with traffic that is
// minutes old, which is worse than not answering.
func (m *Module) lastSample() RateSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sample == nil || m.Now().Sub(m.sampleAt) > maxSampleAge(m.cfg.IdleSampleSecs) {
		return nil
	}
	return m.sample
}

// maxSampleAge is three sampler periods, so one missed tick is tolerated
// and a dead sampler is not.
func maxSampleAge(sampleSecs int) time.Duration {
	if sampleSecs < 1 {
		sampleSecs = 5
	}
	return time.Duration(3*sampleSecs) * time.Second
}

// idleLoop samples the rate sets and strikes joined clients that keep a
// connection without sending gameplay.
func (m *Module) idleLoop(ctx context.Context, c *core.Core) {
	period := time.Duration(m.cfg.IdleSampleSecs) * time.Second
	var prev RateSample
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur, err := m.FW.RateSample()
		now := m.Now()
		m.offenders.Flush(now)
		m.cacheSample(cur, now)
		if err != nil || prev == nil {
			prev = cur
			continue
		}
		m.rates.Sample(now, Report(prev, cur, m.cfg.IdleSampleSecs, m.who))
		m.judge(c, prev, cur, now)
		prev = cur
	}
}

func (m *Module) who(ip netip.Addr) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cl, ok := m.clients[ip]
	if !ok {
		return ""
	}
	name := cl.Container
	if len(name) > 8 {
		name = name[:8]
	}
	return fmt.Sprintf("joined(%s,uid%d)", name, cl.UID)
}

// judge runs the idle verdict for every watched client.
func (m *Module) judge(c *core.Core, prev, cur RateSample, now time.Time) {
	type hit struct {
		ip       netip.Addr
		cl       Client
		pps, bpp int
	}
	var hits []hit
	m.mu.Lock()
	for ip, cl := range m.clients {
		if now.Sub(cl.Since) < time.Duration(m.cfg.IdleGraceSecs)*time.Second {
			continue
		}
		v, pps, bpp := IdleVerdict(ip, prev, cur, m.cfg.IdleSampleSecs, m.cfg.IdlePPSMax, m.cfg.IdleBytesMin)
		switch v {
		case Gone:
			delete(m.clients, ip)
		case Active:
			cl.Strikes = 0
		case Idle:
			cl.Strikes++
			if cl.Strikes >= m.cfg.IdleStrikes {
				hits = append(hits, hit{ip, *cl, pps, bpp})
				delete(m.clients, ip)
			}
		}
	}
	m.mu.Unlock()
	for _, h := range hits {
		m.block(c, h.cl.Container, h.ip, m.cfg.IdleBanMinutes, "idle", "host", h.cl.UID,
			fmt.Sprintf("%d pps, %d B/pkt, uid %d", h.pps, h.bpp, h.cl.UID))
	}
}

// onKmsg handles a static rule's kernel log line: the kernel already
// applied the rule's timeout, a repeat offender gets it rewritten upward,
// and the container owning the port hears about it.
func (m *Module) onKmsg(c *core.Core, line string) {
	rule, ip, port, ok := ParseKmsg(line)
	if !ok {
		return
	}
	now := m.Now()
	minutes, count := m.offenders.Escalate(ip.String(), BanForRule(rule), now)
	why := rule
	if count > 1 {
		why = fmt.Sprintf("%s, repeat #%d", rule, count)
		if err := m.FW.Block(ip, time.Duration(minutes)*time.Minute); err == nil {
			m.Log.Info("blocked", "ip", ip, "minutes", minutes, "rule", rule, "why", why)
		}
	}
	m.offenders.Record(ip.String(), why, now.Add(time.Duration(minutes)*time.Minute), now)
	m.rates.Block(now, ip, minutes, why)
	m.mu.Lock()
	container := m.portOwner[port]
	m.mu.Unlock()
	m.notify(c, container, &proto.GuardBlock{IP: ip.String(), Minutes: minutes, Rule: rule, Src: "host"})
}

// status is the guard section of the control snapshot.
type status struct {
	Mode    string            `json:"mode"`
	Clients map[string]Client `json:"clients"`
	Ports   []uint16          `json:"ports"`
}

func (m *Module) status() any {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := status{Mode: m.cfg.BlockMode, Clients: map[string]Client{}}
	for ip, cl := range m.clients {
		st.Clients[ip.String()] = *cl
	}
	for p := range m.portOwner {
		st.Ports = append(st.Ports, p)
	}
	return st
}

// Doctor checks the firewall and the wiring.
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "Host guard"
	var out []doctor.Check
	cfg := Defaults()
	if err := c.Config(m.Name(), &cfg); err != nil {
		return append(out, doctor.Failf(s, "config section unreadable: "+err.Error()))
	}
	if err := cfg.Check(); err != nil {
		return append(out, doctor.Failf(s, "config: "+err.Error()))
	}
	if _, err := netip.ParsePrefix(cfg.DockerBridgeSubnet); err != nil {
		out = append(out, doctor.Failf(s, "docker_bridge_subnet invalid: "+cfg.DockerBridgeSubnet))
	}
	if _, err := netx.ParsePrefixList(cfg.WhitelistIPs); err != nil {
		out = append(out, doctor.Failf(s, "whitelist_ips: "+err.Error()))
	}
	fw := m.FW
	if fw == nil {
		var err error
		if fw, err = NewFirewall(); err != nil {
			return append(out, doctor.Failf(s, err.Error()))
		}
	}
	if !fw.Loaded() {
		out = append(out, doctor.Failf(s, "nftables table cs2guard not loaded (daemon not running, or nf_tables missing: modprobe nf_tables)"))
		return out
	}
	if n, err := fw.RuleCount(); err != nil || n < RuleTotal {
		out = append(out, doctor.Warnf(s, fmt.Sprintf("chain has %d rules, expected %d: systemctl restart cs2node", n, RuleTotal)))
	} else {
		out = append(out, doctor.Ok(s, fmt.Sprintf("table loaded, %d rules, mode %s", n, cfg.BlockMode)))
	}
	conts := c.Containers()
	ports, _ := fw.Ports()
	switch {
	case len(conts) > 0 && len(ports) == 0:
		out = append(out, doctor.Failf(s, "no protected ports while servers run: nothing is protected"))
	case len(ports) > 0:
		out = append(out, doctor.Ok(s, fmt.Sprintf("protected ports %v", ports)))
	default:
		out = append(out, doctor.Ok(s, "no servers running, port set empty"))
	}
	if blocks, err := fw.Blocks(); err == nil {
		out = append(out, doctor.Ok(s, fmt.Sprintf("%d active block(s)", len(blocks))))
	}
	for _, cont := range conts {
		if len(core.SocketPath(cont.Volume)) > 100 {
			out = append(out, doctor.Warnf(s, cont.Name+": socket path over 100 bytes, the egg cannot connect (unix socket limit)"))
		}
		if !c.Connected(cont.Name) {
			out = append(out, doctor.Warnf(s, cont.Name+": egg not connected, only the static rules cover it"))
		}
	}
	if cfg.RatesLog != "" {
		if st, err := os.Stat(cfg.RatesLog); err == nil {
			out = append(out, doctor.Ok(s, fmt.Sprintf("rates log %s (%.1f MB)", cfg.RatesLog, float64(st.Size())/1e6)))
		}
	}
	if hasGlobalIPv6() {
		if data, err := os.ReadFile("/etc/docker/daemon.json"); err != nil || !strings.Contains(string(data), `"ip6tables": true`) {
			out = append(out, doctor.Warnf(s, "host has IPv6 but docker runs without ip6tables: IPv6 clients reach the servers as the bridge gateway, only the static rules see their real source"))
		}
	}
	return out
}

func hasGlobalIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipn.IP); ok && ip.Is6() && !ip.Is4In6() && ip.IsGlobalUnicast() && !ip.IsPrivate() {
				return true
			}
		}
	}
	return false
}

// MaxExemptPerServer bounds how many addresses one server may hold in the
// players set. A full CS2 server is 64 slots; this leaves room for churn
// and reconnects while keeping one container from filling a kernel set that
// every other server on the node shares.
const MaxExemptPerServer = 256

// claimSlot records a joined address for a container, or refuses when that
// container already holds its share.
func (m *Module) claimSlot(container string, ip netip.Addr, uid int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cl, ok := m.clients[ip]; ok {
		cl.Container, cl.UID, cl.Since = container, uid, m.Now()
		return true // already counted
	}
	n := 0
	for _, cl := range m.clients {
		if cl.Container == container {
			n++
		}
	}
	if n >= MaxExemptPerServer {
		return false
	}
	m.clients[ip] = &Client{Container: container, UID: uid, Since: m.Now()}
	return true
}
