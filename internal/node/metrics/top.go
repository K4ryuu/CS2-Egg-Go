// SPDX-License-Identifier: GPL-3.0-or-later

package metrics

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

// Layout is the column plan for one terminal width. A width of 0 hides
// the column; the server column takes what is left.
type Layout struct {
	Cols                                        int
	Server, Map, Players, CPU, Mem, Rx, Tx, Egg int
	Bars                                        bool
}

const gap = 2

// LayoutFor picks the widest plan that fits: bars go first, then tx, rx,
// memory; the server name gets at least 20 cells and is cut with an
// ellipsis below its full length.
func LayoutFor(cols int) Layout {
	l := Layout{Cols: cols, Map: 12, Players: 7, CPU: 15, Mem: 21, Rx: 8, Tx: 8, Egg: 3, Bars: true}
	fits := func() bool {
		fixed, n := 0, 1 // server
		for _, w := range []int{l.Map, l.Players, l.CPU, l.Mem, l.Rx, l.Tx, l.Egg} {
			if w > 0 {
				fixed += w
				n++
			}
		}
		l.Server = cols - fixed - gap*(n-1) - 1 // one leading space
		return l.Server >= 20
	}
	for _, shrink := range []func(){
		func() { l.Bars, l.CPU, l.Mem = false, 6, 11 },
		func() { l.Tx = 0 },
		func() { l.Rx = 0 },
		func() { l.Mem = 0 },
	} {
		if fits() {
			return l
		}
		shrink()
	}
	if !fits() {
		l.Server = max(l.Server, 9)
	}
	return l
}

type row struct {
	cont core.Container
	st   core.Stats
	err  error
	rx   float64 // bytes/s since the previous sample
	tx   float64
}

// sortKeys cycled by the s key.
var sortKeys = []string{"cpu", "players", "memory", "name"}

// Top is `cs2node top`: a full-screen live table of every server in the
// style of htop, refreshed until q or ctx ends. states supplies the
// daemon's map/players/egg per server (nil when the daemon is down).
func Top(ctx context.Context, w io.Writer, c *core.Core, reader core.StatsReader, states func() map[string]core.ContainerStatus, every time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tty := os.Stdout.Fd()
	restore := ui.RawMode(int(os.Stdin.Fd()))
	defer restore()
	leave := ui.Screen(w)
	defer leave()

	var mu sync.Mutex
	var rows []row
	var known map[string]core.ContainerStatus
	sampled := false // the first frame must not claim there is nothing here
	sortBy, offset := 0, 0
	cpuBusy, cpuTotal := uint64(0), uint64(0)
	hostCPUPct := -1.0

	redraw := make(chan struct{}, 1)
	kick := func() {
		select {
		case redraw <- struct{}{}:
		default:
		}
	}

	// one docker stats stream per running container: docker pushes a
	// sample a second, the same way `docker stats` works, so a 1 s refresh
	// costs nothing extra. Containers that come and go are followed.
	type feed struct {
		cancel context.CancelFunc
	}
	feeds := map[string]feed{}
	latest := map[string]row{}
	follow := func(cont core.Container) {
		fctx, fcancel := context.WithCancel(ctx)
		feeds[cont.Name] = feed{cancel: fcancel}
		go func() {
			ch, err := reader.Stream(fctx, cont.ID)
			if err != nil {
				mu.Lock()
				latest[cont.Name] = row{cont: cont, err: err}
				mu.Unlock()
				kick()
				return
			}
			var prev core.Stats
			var prevAt time.Time
			for st := range ch {
				now := time.Now()
				r := row{cont: cont, st: st}
				if !prevAt.IsZero() && st.RxBytes >= prev.RxBytes && st.TxBytes >= prev.TxBytes {
					secs := now.Sub(prevAt).Seconds()
					if secs > 0 {
						r.rx = float64(st.RxBytes-prev.RxBytes) / secs
						r.tx = float64(st.TxBytes-prev.TxBytes) / secs
					}
				}
				prev, prevAt = st, now
				mu.Lock()
				latest[cont.Name] = r
				mu.Unlock()
			}
		}()
	}
	go func() {
		for {
			conts := c.Containers()
			mu.Lock()
			seen := map[string]bool{}
			for _, cont := range conts {
				seen[cont.Name] = true
				if _, ok := feeds[cont.Name]; !ok {
					latest[cont.Name] = row{cont: cont}
					follow(cont)
				}
			}
			for name, f := range feeds {
				if !seen[name] {
					f.cancel()
					delete(feeds, name)
					delete(latest, name)
				}
			}
			busy, total, _, ok := hostCPU()
			if ok && cpuTotal > 0 && total > cpuTotal {
				hostCPUPct = float64(busy-cpuBusy) / float64(total-cpuTotal) * 100
			}
			cpuBusy, cpuTotal = busy, total
			rows = rows[:0]
			for _, r := range latest {
				rows = append(rows, r)
			}
			if states != nil {
				known = states()
			}
			sampled = true
			mu.Unlock()
			kick()
			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
			}
		}
	}()

	// keys: q quits, s cycles the sort, arrows and page keys scroll
	go func() {
		buf := make([]byte, 8)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || ctx.Err() != nil {
				return
			}
			key := string(buf[:n])
			mu.Lock()
			switch key {
			case "q", "Q", "\x03":
				mu.Unlock()
				cancel()
				return
			case "s", "S":
				sortBy = (sortBy + 1) % len(sortKeys)
			case "\x1b[A", "k":
				offset--
			case "\x1b[B", "j":
				offset++
			case "\x1b[5~":
				offset -= 10
			case "\x1b[6~":
				offset += 10
			case "\x1b[H", "g":
				offset = 0
			}
			mu.Unlock()
			kick()
		}
	}()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	for {
		cols, lines := ui.TermSize(int(tty))
		mu.Lock()
		frame := render(cols, lines, rows, known, sortKeys[sortBy], &offset, hostCPUPct, every, sampled)
		mu.Unlock()
		ui.Frame(w, frame)
		select {
		case <-ctx.Done():
			return nil
		case <-redraw:
		case <-winch:
		}
	}
}

// render lays out one frame: three header lines, a blank, the table
// header, the rows that fit, and the key bar on the last line.
func render(cols, lines int, rows []row, known map[string]core.ContainerStatus, sortBy string, offset *int, hostCPUPct float64, every time.Duration, sampled bool) []string {
	cols -= 3 // right margin: a line reaching the last column wraps, and some terminals float a scrollbar over it
	l := LayoutFor(cols)
	host, _ := os.Hostname()
	var out []string

	// line 1: title, host, clock; version right-aligned when it fits
	left := " " + ui.Bold(ui.Blue("cs2node top")) + "  " + ui.Bold(host) + "  " + time.Now().Format("15:04:05")
	right := ui.Gray("cs2node "+version.Version+" "+version.Channel) + " "
	if ui.Visible(left)+ui.Visible(right)+1 > cols {
		right = ""
	}
	out = append(out, left+strings.Repeat(" ", max(0, cols-ui.Visible(left)-ui.Visible(right)))+right)

	// line 2: host cpu and memory bars (the servers' sums off Linux)
	var sumCPU float64
	var sumMem uint64
	players, eggs := 0, 0
	for _, r := range rows {
		if r.err == nil {
			sumCPU += r.st.CPUPercent
			sumMem += r.st.MemBytes
		}
		if s, ok := known[r.cont.Name]; ok {
			if s.Connected {
				eggs++
			}
			if s.Reported {
				players += s.Players
			}
		}
	}
	// the two bars share what is left after the labels (~56 cells)
	barW := max(4, min(30, (cols-56)/2))
	wide := cols >= 90
	cpuText, cpuFrac := fmt.Sprintf("%5.1f%%", sumCPU), sumCPU/100/float64(max(1, len(rows)))
	if _, _, cores, ok := hostCPU(); ok {
		if hostCPUPct >= 0 {
			cpuText, cpuFrac = fmt.Sprintf("%5.1f%%", hostCPUPct), hostCPUPct/100
		} else {
			cpuText, cpuFrac = "  ...", 0
		}
		if wide {
			cpuText += ui.Gray(fmt.Sprintf("  %d cores", cores))
		}
	} else if wide {
		cpuText += ui.Gray("  servers")
	}
	memText, memFrac := ui.Short(int64(sumMem)), 0.0
	if used, total, ok := hostMem(); ok {
		memText, memFrac = ui.Short(int64(used))+ui.Gray("/"+ui.Short(int64(total))), float64(used)/float64(total)
	} else if wide {
		memText += ui.Gray("  servers")
	}
	out = append(out, " "+ui.Bold("CPU")+" "+ui.Bar(barW, cpuFrac)+" "+cpuText+"   "+ui.Bold("MEM")+" "+ui.Bar(barW, memFrac)+" "+memText)

	// line 3: totals
	eggText := fmt.Sprintf("%d/%d", eggs, len(rows))
	switch {
	case !sampled:
		eggText = ui.Gray("reading...")
	case known == nil:
		eggText = ui.Yellow("daemon down")
	}
	servers := strconv.Itoa(len(rows))
	if !sampled {
		servers = ui.Gray("...")
	}
	totals := " " + ui.Gray("servers ") + servers + ui.Gray("   players ") + strconv.Itoa(players) + ui.Gray("   egg ") + eggText
	if wide {
		totals += ui.Gray("   sort ") + sortBy + ui.Gray("   refresh ") + every.String()
	}
	out = append(out, totals)
	out = append(out, "")

	// table header in reverse video across the width
	cells := []string{
		ui.Fit("SERVER", l.Server, false), ui.Fit("MAP", l.Map, false), ui.Fit("PLAYERS", l.Players, true), ui.Fit("CPU", l.CPU, true),
	}
	if l.Mem > 0 {
		cells = append(cells, ui.Fit("MEMORY", l.Mem, true))
	}
	if l.Rx > 0 {
		cells = append(cells, ui.Fit("RX/s", l.Rx, true))
	}
	if l.Tx > 0 {
		cells = append(cells, ui.Fit("TX/s", l.Tx, true))
	}
	cells = append(cells, ui.Fit("EGG", l.Egg, false))
	header := " " + strings.Join(cells, strings.Repeat(" ", gap))
	out = append(out, ui.Bold(strings.TrimRight(header, " ")))
	out = append(out, " "+ui.Dim(strings.Repeat("─", cols-1)))

	sorted := append([]row(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].cont.Name < sorted[j].cont.Name })
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		switch sortBy {
		case "cpu":
			return a.st.CPUPercent > b.st.CPUPercent
		case "players":
			return known[a.cont.Name].Players > known[b.cont.Name].Players
		case "memory":
			return a.st.MemBytes > b.st.MemBytes
		}
		return a.cont.Name < b.cont.Name
	})
	avail := max(1, lines-len(out)-2) // minus the rule and the key line
	*offset = max(0, min(*offset, max(0, len(sorted)-avail)))
	switch {
	case !sampled:
		out = append(out, " "+ui.Gray("Reading docker..."))
	case len(sorted) == 0:
		out = append(out, " "+ui.Gray("(no running CS2 container matches the configured images; cs2node status lists what the node sees)"))
	}
	for i := *offset; i < len(sorted) && i < *offset+avail; i++ {
		out = append(out, renderRow(l, sorted[i], known))
	}
	for len(out) < lines-2 {
		out = append(out, "")
	}
	keys := " " + ui.Bold("q") + ui.Gray(" quit  ") + ui.Bold("s") + ui.Gray(" sort  ") + ui.Bold("↑↓") + ui.Gray(" scroll")
	if len(sorted) > avail {
		keys += ui.Gray(fmt.Sprintf("   %d-%d of %d", *offset+1, min(len(sorted), *offset+avail), len(sorted)))
	}
	out = append(out, " "+ui.Dim(strings.Repeat("─", cols-1)))
	out = append(out, keys)
	for i := range out { // nothing may wrap, whatever the width
		out[i] = ui.Clip(out[i], cols)
	}
	return out
}

// serverLabel is the short id plus the name players see. The id stays
// first because it is what the commands take; the name is what an operator
// actually recognises.
func serverLabel(container, server string) string {
	id := ui.ServerID(container)
	if server == "" {
		return id
	}
	return ui.Gray(id) + "  " + server
}

func renderRow(l Layout, r row, known map[string]core.ContainerStatus) string {
	name := serverLabel(r.cont.Name, known[r.cont.Name].Server)
	game, players, egg := "-", "-", ui.Gray(ui.Fit("-", l.Egg, false))
	gameStyle := ui.Gray
	if s, ok := known[r.cont.Name]; ok {
		if s.Connected {
			egg = ui.Green(ui.Fit("ok", l.Egg, false))
			// connected but silent: an image from before the watcher, so
			// say so instead of showing a blank map and zero players
			game, gameStyle = "no report", ui.Yellow
		} else {
			egg = ui.Yellow(ui.Fit("no", l.Egg, false))
		}
		if s.Reported {
			players = strconv.Itoa(s.Players)
			switch {
			case !s.Up:
				game, gameStyle = "stopped", ui.Yellow
			case s.Map != "":
				game, gameStyle = s.Map, func(x string) string { return x }
			default:
				game, gameStyle = "loading", ui.Gray
			}
		}
	}
	cells := []string{ui.Fit(name, l.Server, false), gameStyle(ui.Fit(game, l.Map, false)), ui.Fit(players, l.Players, true)}
	if r.err != nil {
		msg := ui.Red(ui.Fit("stats: "+short(r.err.Error()), l.CPU+gap+l.Mem+gap+l.Rx+gap+l.Tx, false))
		return strings.TrimRight(" "+strings.Join(append(cells, msg, egg), strings.Repeat(" ", gap)), " ")
	}
	pct := fmt.Sprintf("%.1f%%", r.st.CPUPercent)
	cpu := ui.Fit(pct, 6, true)
	if l.Bars {
		cpu = ui.Bar(l.CPU-9, r.st.CPUPercent/100) + " " + cpu // brackets + space + 6
	}
	switch {
	case r.st.CPUPercent >= 90:
		cpu = strings.Replace(cpu, pct, ui.Red(pct), 1)
	case r.st.CPUPercent >= 60:
		cpu = strings.Replace(cpu, pct, ui.Yellow(pct), 1)
	}
	cells = append(cells, cpu)
	if l.Mem > 0 {
		used := ui.Short(int64(r.st.MemBytes))
		text := used
		frac := 0.0
		if r.st.MemLimit > 0 && r.st.MemLimit < 1<<60 {
			text = used + "/" + ui.Short(int64(r.st.MemLimit))
			frac = float64(r.st.MemBytes) / float64(r.st.MemLimit)
		}
		mem := ui.Fit(text, 11, true)
		if l.Bars {
			mem = ui.Bar(l.Mem-14, frac) + " " + mem // brackets + space + 11
		}
		if frac >= 0.9 {
			mem = strings.Replace(mem, used, ui.Red(used), 1)
		}
		cells = append(cells, mem)
	}
	if l.Rx > 0 {
		cells = append(cells, ui.Fit(rate(r.rx), l.Rx, true))
	}
	if l.Tx > 0 {
		cells = append(cells, ui.Fit(rate(r.tx), l.Tx, true))
	}
	cells = append(cells, egg)
	return strings.TrimRight(" "+strings.Join(cells, strings.Repeat(" ", gap)), " ")
}

func rate(bps float64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1fM", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0fK", bps/(1<<10))
	}
	return fmt.Sprintf("%.0fB", bps)
}

func short(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}
