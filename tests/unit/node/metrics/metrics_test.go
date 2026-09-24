// SPDX-License-Identifier: GPL-3.0-or-later

package metrics_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/metrics"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

func TestCPUAndMemoryFollowDockerStats(t *testing.T) {
	sr := container.StatsResponse{}
	sr.CPUStats.CPUUsage.TotalUsage = 3_000_000_000
	sr.PreCPUStats.CPUUsage.TotalUsage = 1_000_000_000
	sr.CPUStats.SystemUsage = 8_000_000_000
	sr.PreCPUStats.SystemUsage = 4_000_000_000
	sr.CPUStats.OnlineCPUs = 4
	sr.MemoryStats.Usage = 1000
	sr.MemoryStats.Limit = 4000
	sr.MemoryStats.Stats = map[string]uint64{"inactive_file": 200}
	sr.Networks = map[string]container.NetworkStats{"eth0": {RxBytes: 10, TxBytes: 20}, "eth1": {RxBytes: 1, TxBytes: 2}}
	sr.PidsStats.Current = 42
	s := core.FromStatsResponse(sr)
	if s.CPUPercent != 200 || s.MemBytes != 800 || s.MemLimit != 4000 || s.RxBytes != 11 || s.TxBytes != 22 || s.PIDs != 42 || s.CPUs != 4 {
		t.Fatalf("%+v", s)
	}
}

func TestRenderIsPrometheusText(t *testing.T) {
	out := metrics.Render(map[string]metrics.Sample{
		"srv-b": {Stats: core.Stats{CPUPercent: 12.5, MemBytes: 1 << 30, RxBytes: 5}, State: proto.ServerState{Map: "de_dust2", Players: 7, Up: true}, Egg: true},
		"srv-a": {Stats: core.Stats{}, State: proto.ServerState{Map: "de_\"quote\"\nnext"}, Egg: false},
	}, "1.2.3", "dev", 90*time.Second)
	for _, want := range []string{
		`cs2node_info{version="1.2.3",channel="dev"} 1`,
		"cs2node_uptime_seconds 90",
		"cs2node_servers 2",
		`cs2_server_players{server="srv-b"} 7`,
		`cs2_server_map_info{server="srv-b",map="de_dust2"} 1`,
		`cs2_server_cpu_percent{server="srv-b"} 12.50`,
		`cs2_server_memory_bytes{server="srv-b"} 1073741824`,
		`cs2_server_up{server="srv-a"} 0`,
		`cs2_server_egg_connected{server="srv-b"} 1`,
		`cs2_server_network_receive_bytes_total{server="srv-b"} 5`,
		"# TYPE cs2_server_network_receive_bytes_total counter",
		`cs2_server_map_info{server="srv-a",map="de_\"quote\"\nnext"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, `server="srv-a"`) > strings.Index(out, `server="srv-b"`) {
		t.Fatal("servers must be sorted")
	}
}

type fakeReader struct{}

func (fakeReader) Read(context.Context, string) (core.Stats, error) {
	return core.Stats{CPUPercent: 3, MemBytes: 7}, nil
}
func (fakeReader) Stream(ctx context.Context, _ string) (<-chan core.Stats, error) {
	ch := make(chan core.Stats)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}
func (fakeReader) Close() error { return nil }

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestEndpointServesTheSnapshot(t *testing.T) {
	addr := freePort(t)
	cfg := nodeconfig.Default()
	cfg.SetSection("metrics", metrics.Config{Enabled: true, Listen: addr, SampleSeconds: 2})
	c := core.New(nil, cfg, []string{"metrics"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m := metrics.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Open = func() (core.StatsReader, error) { return fakeReader{}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx, c)
	var body string
	for i := 0; i < 50; i++ {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, "cs2node_servers 0") {
		t.Fatalf("endpoint body:\n%s", body)
	}
	if resp, err := http.Get("http://" + addr + "/nope"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatal("other paths must 404")
	}
}
