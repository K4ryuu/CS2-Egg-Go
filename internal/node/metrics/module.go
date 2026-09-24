// SPDX-License-Identifier: GPL-3.0-or-later

// Package metrics samples every server's CPU, memory and network through
// docker, exposes them with map and player counts as Prometheus text on a
// local port, and drives `cs2node top`.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

// Config is the "metrics" section of the node config.
type Config struct {
	Enabled       bool   `json:"enabled"`
	Listen        string `json:"listen"`         // host:port of the /metrics endpoint
	SampleSeconds int    `json:"sample_seconds"` // docker stats period
}

// Defaults is what the wizard offers.
func Defaults() Config {
	return Config{Enabled: true, Listen: "127.0.0.1:9151", SampleSeconds: 10}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return errors.New("listen must be host:port")
	}
	if c.SampleSeconds < 2 {
		return errors.New("sample_seconds must be at least 2 (one sample takes a second)")
	}
	return nil
}

// Sample is one server's latest numbers.
type Sample struct {
	Stats core.Stats
	State proto.ServerState
	Egg   bool
	At    time.Time
}

// Module is the metrics feature.
type Module struct {
	Log   *slog.Logger
	Open  func() (core.StatsReader, error) // docker by default, a fake in tests
	Since time.Time

	cfg     Config
	mu      sync.Mutex
	samples map[string]Sample
	scrapes int
}

// New returns the module with logging wired.
func New(log *slog.Logger) *Module {
	return &Module{Log: log, Open: core.NewStatsReader, Since: time.Now(), samples: map[string]Sample{}}
}

func (m *Module) Name() string { return "metrics" }

func (m *Module) Describe() string {
	return "Metrics: cs2node top (live players, map, cpu, ram, net) and a Prometheus /metrics endpoint"
}

func (m *Module) Questions() []module.Question {
	d := Defaults()
	return []module.Question{
		{Key: "listen", Prompt: "Prometheus endpoint address", Default: d.Listen, Help: "127.0.0.1 keeps it local; use 0.0.0.0:9151 (and a firewall) to scrape from elsewhere"},
		{Key: "sample_seconds", Prompt: "Seconds between docker samples", Default: strconv.Itoa(d.SampleSeconds), Kind: module.Int},
	}
}

// Run serves until ctx ends.
func (m *Module) Run(ctx context.Context, c *core.Core) error {
	m.cfg = Defaults()
	if err := c.Config(m.Name(), &m.cfg); err != nil {
		return err
	}
	if err := m.cfg.Check(); err != nil {
		return err
	}
	reader, err := m.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	ln, err := net.Listen("tcp", m.cfg.Listen)
	if err != nil {
		return fmt.Errorf("metrics listen %s: %w", m.cfg.Listen, err)
	}
	m.Log.Info("metrics up", "listen", "http://"+m.cfg.Listen+"/metrics", "sample_seconds", m.cfg.SampleSeconds)
	c.ProvideStatus(m.Name(), func() any {
		m.mu.Lock()
		defer m.mu.Unlock()
		return status{Listen: m.cfg.Listen, Scrapes: m.scrapes, Samples: m.snapshot()}
	})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		m.mu.Lock()
		m.scrapes++
		snap := m.snapshot()
		m.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Write([]byte(Render(snap, version.Version, version.Channel, time.Since(m.Since))))
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go srv.Serve(ln)
	t := time.NewTicker(time.Duration(m.cfg.SampleSeconds) * time.Second)
	defer t.Stop()
	for {
		m.sampleAll(ctx, c, reader)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// sampleAll reads every running container in parallel (each read waits a
// second inside docker) and drops samples of containers that went away.
func (m *Module) sampleAll(ctx context.Context, c *core.Core, reader core.StatsReader) {
	conts := c.Containers()
	results := make([]Sample, len(conts))
	var wg sync.WaitGroup
	for i, cont := range conts {
		wg.Add(1)
		go func(i int, cont core.Container) {
			defer wg.Done()
			st, err := reader.Read(ctx, cont.ID)
			if err != nil {
				if ctx.Err() == nil {
					m.Log.Debug("stats read failed", "container", cont.Name, "err", err)
				}
				return
			}
			state, _ := c.State(cont.Name)
			results[i] = Sample{Stats: st, State: state, Egg: c.Connected(cont.Name), At: time.Now()}
		}(i, cont)
	}
	wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples = map[string]Sample{}
	for i, cont := range conts {
		if !results[i].At.IsZero() {
			m.samples[cont.Name] = results[i]
		}
	}
}

func (m *Module) snapshot() map[string]Sample {
	out := make(map[string]Sample, len(m.samples))
	for k, v := range m.samples {
		out[k] = v
	}
	return out
}

// status is the metrics section of the control snapshot.
type status struct {
	Listen  string            `json:"listen"`
	Scrapes int               `json:"scrapes"`
	Samples map[string]Sample `json:"samples"`
}

// Render writes the Prometheus text exposition of a snapshot.
func Render(samples map[string]Sample, ver, channel string, uptime time.Duration) string {
	names := make([]string, 0, len(samples))
	for n := range samples {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP cs2node_info Build of the running node daemon.\n# TYPE cs2node_info gauge\ncs2node_info{version=%q,channel=%q} 1\n", ver, channel)
	fmt.Fprintf(&b, "# HELP cs2node_uptime_seconds Seconds since the daemon started.\n# TYPE cs2node_uptime_seconds gauge\ncs2node_uptime_seconds %d\n", int(uptime.Seconds()))
	fmt.Fprintf(&b, "# HELP cs2node_servers Running CS2 containers the node follows.\n# TYPE cs2node_servers gauge\ncs2node_servers %d\n", len(samples))
	gauge := func(name, help string, f func(s Sample) string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		for _, n := range names {
			fmt.Fprintf(&b, "%s{server=%s} %s\n", name, label(n), f(samples[n]))
		}
	}
	counter := func(name, help string, f func(s Sample) uint64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
		for _, n := range names {
			fmt.Fprintf(&b, "%s{server=%s} %d\n", name, label(n), f(samples[n]))
		}
	}
	bit := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	gauge("cs2_server_up", "1 while the game process runs (as the egg reports it).", func(s Sample) string { return bit(s.State.Up) })
	gauge("cs2_server_egg_connected", "1 while the egg holds its socket to the node.", func(s Sample) string { return bit(s.Egg) })
	gauge("cs2_server_players", "Players fully joined.", func(s Sample) string { return strconv.Itoa(s.State.Players) })
	fmt.Fprintf(&b, "# HELP cs2_server_map_info Current map as a label.\n# TYPE cs2_server_map_info gauge\n")
	for _, n := range names {
		fmt.Fprintf(&b, "cs2_server_map_info{server=%s,map=%s} 1\n", label(n), label(samples[n].State.Map))
	}
	gauge("cs2_server_cpu_percent", "CPU of one core in percent (200 = two cores).", func(s Sample) string { return strconv.FormatFloat(s.Stats.CPUPercent, 'f', 2, 64) })
	gauge("cs2_server_memory_bytes", "Memory in use, page cache excluded.", func(s Sample) string { return strconv.FormatUint(s.Stats.MemBytes, 10) })
	gauge("cs2_server_memory_limit_bytes", "Memory limit of the container.", func(s Sample) string { return strconv.FormatUint(s.Stats.MemLimit, 10) })
	gauge("cs2_server_pids", "Processes and threads in the container.", func(s Sample) string { return strconv.FormatUint(s.Stats.PIDs, 10) })
	counter("cs2_server_network_receive_bytes_total", "Bytes received since the container started.", func(s Sample) uint64 { return s.Stats.RxBytes })
	counter("cs2_server_network_transmit_bytes_total", "Bytes sent since the container started.", func(s Sample) uint64 { return s.Stats.TxBytes })
	return b.String()
}

// label quotes a value the Prometheus way (the map name is the server's
// word, so it is escaped, never trusted).
func label(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

// Doctor checks the endpoint answers.
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "Metrics"
	cfg := Defaults()
	if err := c.Config(m.Name(), &cfg); err != nil {
		return []doctor.Check{doctor.Failf(s, "config section unreadable: "+err.Error())}
	}
	if err := cfg.Check(); err != nil {
		return []doctor.Check{doctor.Failf(s, "config: "+err.Error())}
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + cfg.Listen + "/metrics")
	if err != nil {
		return []doctor.Check{doctor.Failf(s, "endpoint http://"+cfg.Listen+"/metrics not answering (daemon down, or the port is taken: journalctl -u cs2node)")}
	}
	resp.Body.Close()
	out := []doctor.Check{doctor.Ok(s, "endpoint http://"+cfg.Listen+"/metrics answers")}
	var st status
	if c.ModuleStatus(m.Name(), &st) {
		out = append(out, doctor.Ok(s, fmt.Sprintf("%d server(s) sampled, %d scrape(s) since start", len(st.Samples), st.Scrapes)))
	}
	return out
}
