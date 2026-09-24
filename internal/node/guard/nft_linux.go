// SPDX-License-Identifier: GPL-3.0-or-later

package guard

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/google/nftables/userdata"
	"golang.org/x/sys/unix"
)

// nftFirewall is the real table, driven over netlink (no nft binary).
type nftFirewall struct {
	mu    sync.Mutex
	conn  *nftables.Conn
	table *nftables.Table
	chain *nftables.Chain
	sets  map[string]*nftables.Set
}

// NewFirewall opens a netlink connection to nf_tables.
func NewFirewall() (Firewall, error) {
	conn, err := nftables.New(nftables.AsLasting())
	if err != nil {
		return nil, fmt.Errorf("nftables: %w", err)
	}
	return &nftFirewall{conn: conn}, nil
}

const tableName = "cs2guard"

func (f *nftFirewall) tableRef() *nftables.Table {
	return &nftables.Table{Family: nftables.TableFamilyINet, Name: tableName}
}

// setDefs mirrors the bash ruleset: name, key type, flags, timeout, size.
func (f *nftFirewall) setDefs(t *nftables.Table) map[string]*nftables.Set {
	ip4, ip6, svc := nftables.TypeIPAddr, nftables.TypeIP6Addr, nftables.TypeInetService
	dyn := func(name string, kt nftables.SetDatatype, timeout time.Duration, size uint32) *nftables.Set {
		return &nftables.Set{Table: t, Name: name, KeyType: kt, Dynamic: true, HasTimeout: true, Timeout: timeout, Size: size}
	}
	defs := map[string]*nftables.Set{
		"ports":  {Table: t, Name: "ports", KeyType: svc, KeyByteOrder: binaryutil.BigEndian},
		"allow4": {Table: t, Name: "allow4", KeyType: ip4, Interval: true},
		"allow6": {Table: t, Name: "allow6", KeyType: ip6, Interval: true},
		"block4": dyn("block4", ip4, DefaultBanMinutes*time.Minute, 262144),
		"block6": dyn("block6", ip6, DefaultBanMinutes*time.Minute, 262144),
		"pps4":   dyn("pps4", ip4, 10*time.Second, 65535),
		"pps6":   dyn("pps6", ip6, 10*time.Second, 65535),
		"rcon4":  dyn("rcon4", ip4, 60*time.Second, 65535),
		"rcon6":  dyn("rcon6", ip6, 60*time.Second, 65535),
		"rate4":  dyn("rate4", ip4, 30*time.Second, 65535),
		"rate6":  dyn("rate6", ip6, 30*time.Second, 65535),
		// bounded like the block sets: the entries come from a container
		// saying "this address joined", which the node cannot check
		"players4": {Table: t, Name: "players4", KeyType: ip4, HasTimeout: true, Size: 262144},
		"players6": {Table: t, Name: "players6", KeyType: ip6, HasTimeout: true, Size: 262144},
		"a2s4":     dyn("a2s4", ip4, 10*time.Second, 65535),
		"a2s6":     dyn("a2s6", ip6, 10*time.Second, 65535),
		"tcp4":     dyn("tcp4", ip4, 10*time.Second, 65535),
		"tcp6":     dyn("tcp6", ip6, 10*time.Second, 65535),
		"stranger": {Table: t, Name: "stranger", KeyType: svc, KeyByteOrder: binaryutil.BigEndian, Dynamic: true, HasTimeout: true, Timeout: 10 * time.Second, Size: 1024},
	}
	return defs
}

// --- expression builders ---------------------------------------------------

func fam(v6 bool) []expr.Any {
	proto := byte(unix.NFPROTO_IPV4)
	if v6 {
		proto = unix.NFPROTO_IPV6
	}
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
	}
}

func l4(proto byte) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
	}
}

// saddr loads the source address into register 1.
func saddr(v6 bool) expr.Any {
	if v6 {
		return &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 8, Len: 16}
	}
	return &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4}
}

// dport loads the transport destination port into register 1.
func dport() expr.Any {
	return &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2}
}

func lookup(s *nftables.Set, invert bool) expr.Any {
	return &expr.Lookup{SourceRegister: 1, SetName: s.Name, SetID: s.ID, Invert: invert}
}

func counter() expr.Any { return &expr.Counter{} }
func drop() expr.Any    { return &expr.Verdict{Kind: expr.VerdictDrop} }
func accept() expr.Any  { return &expr.Verdict{Kind: expr.VerdictAccept} }

func logPrefix(p string) expr.Any {
	return &expr.Log{Key: 1 << unix.NFTA_LOG_PREFIX, Data: []byte(p)}
}

// addTimeout is "add @set { <reg1> timeout d }".
func addTimeout(s *nftables.Set, d time.Duration) expr.Any {
	return &expr.Dynset{SrcRegKey: 1, SetName: s.Name, SetID: s.ID, Operation: unix.NFT_DYNSET_OP_ADD, Timeout: d}
}

// updateLimit is "update @set { <reg1> limit rate over n/unit burst n }":
// the rule continues only when the source is over its rate.
func updateLimit(s *nftables.Set, rate int, unit expr.LimitTime, burst int) expr.Any {
	return &expr.Dynset{SrcRegKey: 1, SetName: s.Name, SetID: s.ID, Operation: unix.NFT_DYNSET_OP_UPDATE,
		Exprs: []expr.Any{&expr.Limit{Type: expr.LimitTypePkts, Rate: uint64(rate), Unit: unit, Burst: uint32(burst), Over: true}}}
}

// updateCounter is "update @set { <reg1> counter }".
func updateCounter(s *nftables.Set) expr.Any {
	return &expr.Dynset{SrcRegKey: 1, SetName: s.Name, SetID: s.ID, Operation: unix.NFT_DYNSET_OP_UPDATE, Exprs: []expr.Any{&expr.Counter{}}}
}

// udpLengthUnder is "udp length < n".
func udpLengthUnder(n uint16) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 4, Len: 2},
		&expr.Cmp{Op: expr.CmpOpLt, Register: 1, Data: binaryutil.BigEndian.PutUint16(n)},
	}
}

// a2sMagic is "@th,64,32 0xffffffff": the four 0xff bytes every A2S query starts with.
func a2sMagic() []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 8, Len: 4},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0xff, 0xff, 0xff, 0xff}},
	}
}

// synOnly is "tcp flags & (syn|ack) == syn".
func synOnly() []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 13, Len: 1},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 1, Mask: []byte{0x12}, Xor: []byte{0x00}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x02}},
	}
}

func cat(parts ...[]expr.Any) []expr.Any {
	var out []expr.Any
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func one(e ...expr.Any) []expr.Any { return e }

// rules is the chain, in order, for one config. The count is what the
// doctor checks.
func (f *nftFirewall) rules(cfg Config, s map[string]*nftables.Set) [][]expr.Any {
	udp, tcp := l4(unix.IPPROTO_UDP), l4(unix.IPPROTO_TCP)
	ports := one(dport(), lookup(s["ports"], false))
	var out [][]expr.Any
	// allow first: the bridge and the whitelist never see another rule
	out = append(out, cat(fam(false), one(saddr(false), lookup(s["allow4"], false), accept())))
	out = append(out, cat(fam(true), one(saddr(true), lookup(s["allow6"], false), accept())))
	for _, v6 := range []bool{false, true} {
		blk := s["block4"]
		if v6 {
			blk = s["block6"]
		}
		out = append(out, cat(fam(v6), udp, ports, one(saddr(v6), lookup(blk, false), counter(), drop())))
		out = append(out, cat(fam(v6), tcp, ports, one(saddr(v6), lookup(blk, false), counter(), drop())))
	}
	out = append(out, cat(fam(false), udp, ports, one(saddr(false), updateCounter(s["rate4"]))))
	out = append(out, cat(fam(true), udp, ports, one(saddr(true), updateCounter(s["rate6"]))))
	// tiny: log rule is verdict-less with its own rate limit, never gating the drop
	out = append(out, cat(udp, ports, udpLengthUnder(17), one(&expr.Limit{Type: expr.LimitTypePkts, Rate: 30, Unit: expr.LimitTimeMinute, Burst: 5}, logPrefix("cs2guard tiny "))))
	out = append(out, cat(fam(false), udp, ports, udpLengthUnder(17), one(saddr(false), addTimeout(s["block4"], TinyBanMinutes*time.Minute), counter(), drop())))
	out = append(out, cat(fam(true), udp, ports, udpLengthUnder(17), one(saddr(true), addTimeout(s["block6"], TinyBanMinutes*time.Minute), counter(), drop())))
	// spoofable sources: a2s, tcp and the per-port stranger limiter only drop, never block
	out = append(out, cat(fam(false), udp, ports, a2sMagic(), one(saddr(false), updateLimit(s["a2s4"], cfg.A2SPPSPerSource, expr.LimitTimeSecond, cfg.A2SPPSPerSource), counter(), drop())))
	out = append(out, cat(fam(true), udp, ports, a2sMagic(), one(saddr(true), updateLimit(s["a2s6"], cfg.A2SPPSPerSource, expr.LimitTimeSecond, cfg.A2SPPSPerSource), counter(), drop())))
	out = append(out, cat(fam(false), tcp, ports, one(saddr(false), updateLimit(s["tcp4"], cfg.TCPPPSPerSource, expr.LimitTimeSecond, cfg.TCPPPSPerSource), counter(), drop())))
	out = append(out, cat(fam(true), tcp, ports, one(saddr(true), updateLimit(s["tcp6"], cfg.TCPPPSPerSource, expr.LimitTimeSecond, cfg.TCPPPSPerSource), counter(), drop())))
	out = append(out, cat(fam(false), udp, ports, one(saddr(false), lookup(s["players4"], true), dport(), updateLimit(s["stranger"], cfg.StrangerPPSPerPort, expr.LimitTimeSecond, cfg.StrangerPPSPerPort), counter(), drop())))
	out = append(out, cat(fam(true), udp, ports, one(saddr(true), lookup(s["players6"], true), dport(), updateLimit(s["stranger"], cfg.StrangerPPSPerPort, expr.LimitTimeSecond, cfg.StrangerPPSPerPort), counter(), drop())))
	// pps and rcon block: log inline, the next packet hits the block rule, so one log line per block
	out = append(out, cat(fam(false), udp, ports, one(saddr(false), updateLimit(s["pps4"], cfg.UDPPPSLimit, expr.LimitTimeSecond, cfg.UDPPPSLimit), logPrefix("cs2guard pps "), addTimeout(s["block4"], FloodBanMinutes*time.Minute), counter(), drop())))
	out = append(out, cat(fam(true), udp, ports, one(saddr(true), updateLimit(s["pps6"], cfg.UDPPPSLimit, expr.LimitTimeSecond, cfg.UDPPPSLimit), logPrefix("cs2guard pps "), addTimeout(s["block6"], FloodBanMinutes*time.Minute), counter(), drop())))
	out = append(out, cat(fam(false), tcp, ports, synOnly(), one(saddr(false), updateLimit(s["rcon4"], cfg.RconSynPerMinute, expr.LimitTimeMinute, 20), logPrefix("cs2guard rcon "), addTimeout(s["block4"], RconBanMinutes*time.Minute), counter(), drop())))
	out = append(out, cat(fam(true), tcp, ports, synOnly(), one(saddr(true), updateLimit(s["rcon6"], cfg.RconSynPerMinute, expr.LimitTimeMinute, 20), logPrefix("cs2guard rcon "), addTimeout(s["block6"], RconBanMinutes*time.Minute), counter(), drop())))
	return out
}

// RuleTotal is how many rules Apply installs.
const RuleTotal = 21

func (f *nftFirewall) findTable() *nftables.Table {
	tables, err := f.conn.ListTables()
	if err != nil {
		return nil
	}
	for _, t := range tables {
		if t.Family == nftables.TableFamilyINet && t.Name == tableName {
			return t
		}
	}
	return nil
}

func (f *nftFirewall) Apply(cfg Config, allow []netip.Prefix) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	recreated := false
	if t := f.findTable(); t != nil {
		rules, err := f.conn.GetRules(t, &nftables.Chain{Name: "pre", Table: t})
		tag := ""
		if err == nil && len(rules) > 0 {
			tag, _ = userdata.GetString(rules[0].UserData, userdata.TypeComment)
		}
		if tag != RulesetTag {
			f.conn.DelTable(t)
			if err := f.conn.Flush(); err != nil {
				return false, fmt.Errorf("delete old table: %w", err)
			}
			recreated = true
		}
	} else {
		recreated = true
	}
	t := f.conn.AddTable(f.tableRef())
	sets := f.setDefs(t)
	for _, name := range setNames() {
		if err := f.conn.AddSet(sets[name], nil); err != nil {
			return recreated, fmt.Errorf("set %s: %w", name, err)
		}
	}
	policy := nftables.ChainPolicyAccept
	chain := f.conn.AddChain(&nftables.Chain{
		Name: "pre", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityRef(-300), Policy: &policy,
	})
	if !recreated {
		f.conn.FlushChain(chain)
		f.conn.FlushSet(sets["allow4"])
		f.conn.FlushSet(sets["allow6"])
	}
	var a4, a6 []nftables.SetElement
	for _, p := range allow {
		p = p.Masked()
		first, last := prefixRange(p)
		if p.Addr().Is4() {
			a4 = append(a4, nftables.SetElement{Key: first}, nftables.SetElement{Key: last, IntervalEnd: true})
		} else {
			a6 = append(a6, nftables.SetElement{Key: first}, nftables.SetElement{Key: last, IntervalEnd: true})
		}
	}
	if len(a4) > 0 {
		f.conn.SetAddElements(sets["allow4"], a4)
	}
	if len(a6) > 0 {
		f.conn.SetAddElements(sets["allow6"], a6)
	}
	for i, exprs := range f.rules(cfg, sets) {
		r := &nftables.Rule{Table: t, Chain: chain, Exprs: exprs}
		if i == 0 {
			r.UserData = userdata.AppendString(nil, userdata.TypeComment, RulesetTag)
		}
		f.conn.AddRule(r)
	}
	if err := f.conn.Flush(); err != nil {
		return recreated, fmt.Errorf("load ruleset: %w", err)
	}
	f.table, f.chain, f.sets = t, chain, sets
	return recreated, nil
}

// prefixRange gives the interval element pair for a CIDR: the first
// address, and the address right after the last one as the open end.
func prefixRange(p netip.Prefix) (first, end []byte) {
	a := p.Masked().Addr()
	first = a.AsSlice()
	bits := a.BitLen()
	last := a.AsSlice()
	for i := p.Bits(); i < bits; i++ {
		last[i/8] |= 1 << (7 - uint(i%8))
	}
	// increment the last address for the open interval end
	end = make([]byte, len(last))
	copy(end, last)
	for i := len(end) - 1; i >= 0; i-- {
		end[i]++
		if end[i] != 0 {
			break
		}
	}
	return first, end
}

func setNames() []string {
	return []string{"ports", "allow4", "allow6", "block4", "block6", "pps4", "pps6", "rcon4", "rcon6", "rate4", "rate6", "players4", "players6", "a2s4", "a2s6", "tcp4", "tcp6", "stranger"}
}

// set returns a handle for an existing set (after Apply, or by name).
func (f *nftFirewall) set(name string) (*nftables.Set, error) {
	if f.sets != nil {
		if s, ok := f.sets[name]; ok {
			return s, nil
		}
	}
	t := f.findTable()
	if t == nil {
		return nil, errors.New("cs2guard table not loaded (daemon not running?)")
	}
	s, err := f.conn.GetSetByName(t, name)
	if err != nil {
		return nil, err
	}
	// element timeouts only marshal when the set object says it has them
	if def := f.setDefs(t)[name]; def != nil {
		s.HasTimeout = def.HasTimeout
	}
	return s, nil
}

func (f *nftFirewall) Loaded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.findTable()
	if t == nil {
		return false
	}
	_, err := f.conn.GetRules(t, &nftables.Chain{Name: "pre", Table: t})
	return err == nil
}

func (f *nftFirewall) RefreshPorts(ports []uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.set("ports")
	if err != nil {
		return err
	}
	f.conn.FlushSet(s)
	elems := make([]nftables.SetElement, 0, len(ports))
	for _, p := range ports {
		elems = append(elems, nftables.SetElement{Key: binaryutil.BigEndian.PutUint16(p)})
	}
	if len(elems) > 0 {
		if err := f.conn.SetAddElements(s, elems); err != nil {
			return err
		}
	}
	return f.conn.Flush()
}

func blockSetName(ip netip.Addr) string {
	if ip.Is6() {
		return "block6"
	}
	return "block4"
}

func (f *nftFirewall) Block(ip netip.Addr, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.set(blockSetName(ip))
	if err != nil {
		return err
	}
	// delete then add: add on an existing element keeps its old timeout
	f.conn.SetDeleteElements(s, []nftables.SetElement{{Key: ip.AsSlice()}})
	f.conn.Flush()
	if err := f.conn.SetAddElements(s, []nftables.SetElement{{Key: ip.AsSlice(), Timeout: d}}); err != nil {
		return err
	}
	return f.conn.Flush()
}

func (f *nftFirewall) Unblock(ip netip.Addr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.set(blockSetName(ip))
	if err != nil {
		return err
	}
	f.conn.SetDeleteElements(s, []nftables.SetElement{{Key: ip.AsSlice()}})
	return f.conn.Flush()
}

func (f *nftFirewall) UnblockAll() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, name := range []string{"block4", "block6"} {
		s, err := f.set(name)
		if err != nil {
			return err
		}
		f.conn.FlushSet(s)
	}
	return f.conn.Flush()
}

func (f *nftFirewall) AddPlayer(ip netip.Addr, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := "players4"
	if ip.Is6() {
		name = "players6"
	}
	s, err := f.set(name)
	if err != nil {
		return err
	}
	f.conn.SetDeleteElements(s, []nftables.SetElement{{Key: ip.AsSlice()}})
	f.conn.Flush()
	if err := f.conn.SetAddElements(s, []nftables.SetElement{{Key: ip.AsSlice(), Timeout: d}}); err != nil {
		return err
	}
	return f.conn.Flush()
}

func (f *nftFirewall) Blocks() ([]BlockEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []BlockEntry
	for _, name := range []string{"block4", "block6"} {
		s, err := f.set(name)
		if err != nil {
			return nil, err
		}
		elems, err := f.conn.GetSetElements(s)
		if err != nil {
			return nil, err
		}
		for _, e := range elems {
			ip, ok := netip.AddrFromSlice(e.Key)
			if !ok {
				continue
			}
			out = append(out, BlockEntry{IP: ip, Timeout: e.Timeout, Expires: e.Expires})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP.Less(out[j].IP) })
	return out, nil
}

func (f *nftFirewall) RateSample() (RateSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := RateSample{}
	for _, name := range []string{"rate4", "rate6"} {
		s, err := f.set(name)
		if err != nil {
			return nil, err
		}
		elems, err := f.conn.GetSetElements(s)
		if err != nil {
			return nil, err
		}
		for _, e := range elems {
			ip, ok := netip.AddrFromSlice(e.Key)
			if !ok || e.Counter == nil {
				continue
			}
			out[ip] = Counter{Packets: e.Counter.Packets, Bytes: e.Counter.Bytes}
		}
	}
	return out, nil
}

func (f *nftFirewall) Ports() ([]uint16, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.set("ports")
	if err != nil {
		return nil, err
	}
	elems, err := f.conn.GetSetElements(s)
	if err != nil {
		return nil, err
	}
	var out []uint16
	for _, e := range elems {
		if len(e.Key) == 2 {
			out = append(out, binaryutil.BigEndian.Uint16(e.Key))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (f *nftFirewall) RuleCount() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.findTable()
	if t == nil {
		return 0, errors.New("table not loaded")
	}
	rules, err := f.conn.GetRules(t, &nftables.Chain{Name: "pre", Table: t})
	if err != nil {
		return 0, err
	}
	return len(rules), nil
}
