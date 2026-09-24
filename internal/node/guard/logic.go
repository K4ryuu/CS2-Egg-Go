// SPDX-License-Identifier: GPL-3.0-or-later

package guard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Clamp keeps a block length inside [MinBanMinutes, max]; nonsense = default.
func Clamp(minutes, max int) int {
	if minutes <= 0 {
		minutes = DefaultBanMinutes
	}
	if minutes < MinBanMinutes {
		minutes = MinBanMinutes
	}
	if minutes > max {
		minutes = max
	}
	return minutes
}

// BanForRule is the static rules' fixed block length.
func BanForRule(rule string) int {
	switch rule {
	case "tiny":
		return TinyBanMinutes
	case "pps":
		return FloodBanMinutes
	case "rcon":
		return RconBanMinutes
	}
	return DefaultBanMinutes
}

// Offenders remembers repeat offenders across restarts.
type Offenders struct {
	Path   string
	Window time.Duration
	Max    int

	mu    sync.Mutex
	seen  map[string]offender
	dirty bool
}

type offender struct {
	Count int    `json:"count"`
	Last  int64  `json:"last"`
	Why   string `json:"why,omitempty"`   // why the current block was applied
	Until int64  `json:"until,omitempty"` // when it lifts; the record outlives Window until then
}

// LoadOffenders reads the state file (missing = empty).
func LoadOffenders(path string, window time.Duration, max int) *Offenders {
	o := &Offenders{Path: path, Window: window, Max: max, seen: map[string]offender{}}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &o.seen)
	}
	return o
}

// MaxOffenders bounds the state file. The addresses come from containers
// reporting hits, so a server that reports a hundred thousand of them would
// otherwise grow the map, the file and every flush without limit. Past this
// the oldest entries whose block has lifted are dropped.
const MaxOffenders = 20000

// Escalate records a hit and returns the block length to apply: the
// requested minutes on the first hit inside the window, 4x on the second,
// the cap from the third on. Always clamped.
func (o *Offenders) Escalate(ip string, requested int, now time.Time) (minutes, count int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.seen) >= MaxOffenders {
		o.evict(now)
	}
	rec := o.seen[ip]
	if now.Sub(time.Unix(rec.Last, 0)) > o.Window {
		rec.Count = 0
	}
	rec.Count++
	rec.Last = now.Unix()
	o.seen[ip] = rec
	o.dirty = true
	minutes = requested
	switch {
	case rec.Count == 2:
		minutes = requested * 4
	case rec.Count >= 3:
		minutes = o.Max
	}
	return Clamp(minutes, o.Max), rec.Count
}

// Record stores why an address is blocked and when the block lifts, so the
// reason lasts exactly as long as the block does. The rates log rotates and
// the kernel rate limits its own lines: neither can be asked afterwards.
func (o *Offenders) Record(ip, why string, until time.Time, now time.Time) {
	if why == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	rec := o.seen[ip]
	rec.Why = why
	rec.Until = until.Unix()
	if rec.Last == 0 {
		rec.Last = now.Unix()
	}
	o.seen[ip] = rec
	o.dirty = true
}

// Reason is why this address is blocked, as recorded when it was.
func (o *Offenders) Reason(ip string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rec, ok := o.seen[ip]
	return rec.Why, ok && rec.Why != ""
}

// Count returns the recorded hits and the last one.
func (o *Offenders) Count(ip string) (int, time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rec, ok := o.seen[ip]
	if !ok {
		return 0, time.Time{}
	}
	return rec.Count, time.Unix(rec.Last, 0)
}

// Flush writes the state file if anything changed. A block used to rewrite
// the whole file twice, under the lock that serialises every block on the
// node, so a flood paid O(offenders) of JSON per dropped address. The
// daemon calls this on a ticker and on the way out instead.
func (o *Offenders) Flush(now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.dirty {
		return
	}
	o.save(now)
}

// evict drops the oldest records whose block has already lifted, down to
// three quarters of the cap so this does not run on every hit.
func (o *Offenders) evict(now time.Time) {
	type aged struct {
		ip   string
		last int64
	}
	var free []aged
	for ip, rec := range o.seen {
		if rec.Until <= now.Unix() {
			free = append(free, aged{ip, rec.Last})
		}
	}
	sort.Slice(free, func(i, j int) bool { return free[i].last < free[j].last })
	target := len(o.seen) - MaxOffenders*3/4
	for i := 0; i < len(free) && i < target; i++ {
		delete(o.seen, free[i].ip)
	}
	o.dirty = true
}

func (o *Offenders) save(now time.Time) {
	o.dirty = false
	if o.Path == "" {
		return
	}
	for ip, rec := range o.seen {
		// a record whose block is still live stays, however old the last hit
		if rec.Until > now.Unix() {
			continue
		}
		if now.Sub(time.Unix(rec.Last, 0)) > o.Window {
			delete(o.seen, ip)
		}
	}
	data, err := json.Marshal(o.seen)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(o.Path), 0o755)
	tmp := o.Path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, o.Path)
	}
}

// RateLine is one source in a rate report.
type RateLine struct {
	IP  netip.Addr
	PPS int
	BPP int
	Who string // "joined(<container8>,uid<N>)" or "-"
}

// Report turns two samples into per-source pps and bytes/packet, sorted by
// pps descending. An element that expired and came back counts from zero.
func Report(prev, cur RateSample, secs int, who func(netip.Addr) string) []RateLine {
	if secs < 1 {
		secs = 1
	}
	out := make([]RateLine, 0, len(cur))
	for ip, c := range cur {
		p := prev[ip]
		dp := int64(c.Packets) - int64(p.Packets)
		db := int64(c.Bytes) - int64(p.Bytes)
		if dp < 0 {
			dp, db = int64(c.Packets), int64(c.Bytes)
		}
		pps := int(dp / int64(secs))
		bpp := 0
		if dp > 0 {
			bpp = int(db / dp)
		}
		w := "-"
		if who != nil {
			if s := who(ip); s != "" {
				w = s
			}
		}
		out = append(out, RateLine{IP: ip, PPS: pps, BPP: bpp, Who: w})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PPS != out[j].PPS {
			return out[i].PPS > out[j].PPS
		}
		return out[i].IP.Less(out[j].IP)
	})
	return out
}

// Verdict of the idle watcher for one joined client.
type Verdict int

const (
	Gone Verdict = iota // no packets at all: it left or timed out, never a strike
	Idle                // still talking, but too few or too small packets to carry usercmds
	Active
)

// IdleVerdict judges one client from two samples.
func IdleVerdict(ip netip.Addr, prev, cur RateSample, secs, ppsMax, bytesMin int) (Verdict, int, int) {
	c, ok := cur[ip]
	if !ok {
		return Gone, 0, 0
	}
	p := prev[ip]
	dp := int64(c.Packets) - int64(p.Packets)
	db := int64(c.Bytes) - int64(p.Bytes)
	if dp < 0 {
		dp, db = int64(c.Packets), int64(c.Bytes)
	}
	if dp <= 0 {
		return Gone, 0, 0
	}
	if secs < 1 {
		secs = 1
	}
	pps := int(dp / int64(secs))
	bpp := int(db / dp)
	if pps < ppsMax || bpp < bytesMin {
		return Idle, pps, bpp
	}
	return Active, pps, bpp
}

// ParseKmsg reads a "cs2guard <rule> ... SRC=<ip> ... DPT=<port>" kernel
// log line, with or without the /dev/kmsg header.
func ParseKmsg(line string) (rule string, ip netip.Addr, port uint16, ok bool) {
	if i := strings.IndexByte(line, ';'); i >= 0 && !strings.HasPrefix(line, "cs2guard") {
		line = line[i+1:]
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "cs2guard" {
		return "", netip.Addr{}, 0, false
	}
	rule = fields[1]
	for _, f := range fields[2:] {
		switch {
		case strings.HasPrefix(f, "SRC="):
			ip, _ = netip.ParseAddr(strings.TrimPrefix(f, "SRC="))
		case strings.HasPrefix(f, "DPT="):
			n, _ := strconv.Atoi(strings.TrimPrefix(f, "DPT="))
			port = uint16(n)
		}
	}
	ok = ip.IsValid() && port != 0
	return
}

// RatesLog is the measurement file: one line per source per sample, block
// lines, and a flood summary when a sample is too wide. Rotated by size.
type RatesLog struct {
	Path       string
	MaxSources int
	MaxBytes   int64 // rotate above this; 0 = 20 MB
	Keep       int   // rotated files to keep; 0 = 14

	mu sync.Mutex
}

func (r *RatesLog) write(lines []string) {
	if r == nil || r.Path == "" || len(lines) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rotate()
	os.MkdirAll(filepath.Dir(r.Path), 0o755)
	f, err := os.OpenFile(r.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, l := range lines {
		w.WriteString(l)
		w.WriteByte('\n')
	}
	w.Flush()
}

func (r *RatesLog) rotate() {
	max, keep := r.MaxBytes, r.Keep
	if max == 0 {
		max = 20 << 20
	}
	if keep == 0 {
		keep = 14
	}
	st, err := os.Stat(r.Path)
	if err != nil || st.Size() < max {
		return
	}
	for i := keep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", r.Path, i), fmt.Sprintf("%s.%d", r.Path, i+1))
	}
	os.Rename(r.Path, r.Path+".1")
}

// Sample logs one report: per-source lines, or a flood summary above the cap.
func (r *RatesLog) Sample(now time.Time, report []RateLine) {
	if r == nil || len(report) == 0 {
		return
	}
	ts := now.Unix()
	if r.MaxSources > 0 && len(report) > r.MaxSources {
		total := 0
		for _, l := range report {
			total += l.PPS
		}
		r.write([]string{fmt.Sprintf("%d flood %d %d", ts, len(report), total)})
		return
	}
	lines := make([]string, 0, len(report))
	for _, l := range report {
		lines = append(lines, fmt.Sprintf("%d rate %s %d %d %s", ts, l.IP, l.PPS, l.BPP, l.Who))
	}
	r.write(lines)
}

// Block logs one block decision.
func (r *RatesLog) Block(now time.Time, ip netip.Addr, minutes int, why string) {
	if r == nil {
		return
	}
	r.write([]string{fmt.Sprintf("%d block %s %d %s", now.Unix(), ip, minutes, why)})
}

// grep returns the last n lines containing needle.
func (r *RatesLog) grep(needle string, n int) []string {
	if r == nil || r.Path == "" {
		return nil
	}
	f, err := os.Open(r.Path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var hits []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if strings.Contains(sc.Text(), needle) {
			hits = append(hits, sc.Text())
			if len(hits) > n {
				hits = hits[1:]
			}
		}
	}
	return hits
}

// LastBlockReason is the why of the most recent block line for ip.
func (r *RatesLog) LastBlockReason(ip netip.Addr) string {
	lines := r.grep(" block "+ip.String()+" ", 1)
	if len(lines) == 0 {
		return ""
	}
	f := strings.SplitN(lines[0], " ", 5)
	if len(f) < 5 {
		return ""
	}
	return f[4]
}

// Since is the timestamp of the first line the current log file still
// holds. Rotation drops older lines, so a block that started before this
// can never have its reason looked up.
func (r *RatesLog) Since() (time.Time, bool) {
	if r == nil || r.Path == "" {
		return time.Time{}, false
	}
	f, err := os.Open(r.Path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		var ts int64
		if _, err := fmt.Sscanf(sc.Text(), "%d ", &ts); err == nil && ts > 0 {
			return time.Unix(ts, 0), true
		}
	}
	return time.Time{}, false
}

// BlockHistory returns the last n block lines for ip.
func (r *RatesLog) BlockHistory(ip netip.Addr, n int) []string {
	return r.grep(" block "+ip.String()+" ", n)
}

// LastRates returns the last n rate lines for ip.
func (r *RatesLog) LastRates(ip netip.Addr, n int) []string {
	return r.grep(" rate "+ip.String()+" ", n)
}
