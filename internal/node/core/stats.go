// SPDX-License-Identifier: GPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"io"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Stats is one resource sample of a container.
type Stats struct {
	CPUPercent float64 // of one core: 200 = two cores busy
	CPUs       int
	MemBytes   uint64
	MemLimit   uint64
	RxBytes    uint64 // cumulative
	TxBytes    uint64 // cumulative
	PIDs       uint64
}

// StatsReader samples container resource usage through docker.
type StatsReader interface {
	// Read takes one sample; docker waits a second for the CPU delta.
	Read(ctx context.Context, container string) (Stats, error)
	// Stream delivers a sample every second (docker's own cadence, what
	// `docker stats` uses) until ctx ends or the container stops; the
	// channel closes then.
	Stream(ctx context.Context, container string) (<-chan Stats, error)
	Close() error
}

type mobyStats struct{ cli *client.Client }

// NewStatsReader opens docker for stats sampling.
func NewStatsReader() (StatsReader, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	return &mobyStats{cli: cli}, nil
}

func (m *mobyStats) Read(ctx context.Context, name string) (Stats, error) {
	res, err := m.cli.ContainerStats(ctx, name, client.ContainerStatsOptions{IncludePreviousSample: true})
	if err != nil {
		return Stats{}, err
	}
	defer res.Body.Close()
	var sr container.StatsResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&sr); err != nil {
		return Stats{}, err
	}
	return FromStatsResponse(sr), nil
}

func (m *mobyStats) Stream(ctx context.Context, name string) (<-chan Stats, error) {
	res, err := m.cli.ContainerStats(ctx, name, client.ContainerStatsOptions{Stream: true})
	if err != nil {
		return nil, err
	}
	out := make(chan Stats, 1)
	go func() {
		defer close(out)
		defer res.Body.Close()
		dec := json.NewDecoder(res.Body)
		for {
			var sr container.StatsResponse
			if err := dec.Decode(&sr); err != nil {
				return
			}
			if sr.PreCPUStats.SystemUsage == 0 {
				continue // the first record has no previous sample
			}
			select {
			case out <- FromStatsResponse(sr):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (m *mobyStats) Close() error { return m.cli.Close() }

// FromStatsResponse reduces a docker stats record the way `docker stats`
// does.
func FromStatsResponse(sr container.StatsResponse) Stats {
	s := Stats{MemLimit: sr.MemoryStats.Limit, PIDs: sr.PidsStats.Current, CPUs: int(sr.CPUStats.OnlineCPUs)}
	if s.CPUs == 0 {
		s.CPUs = len(sr.CPUStats.CPUUsage.PercpuUsage)
	}
	// docker stats subtracts the page cache so a server reading VPKs does
	// not look like it eats all the RAM
	s.MemBytes = sr.MemoryStats.Usage
	if cache, ok := sr.MemoryStats.Stats["inactive_file"]; ok && cache < s.MemBytes {
		s.MemBytes -= cache
	} else if cache, ok := sr.MemoryStats.Stats["cache"]; ok && cache < s.MemBytes {
		s.MemBytes -= cache
	}
	cpuDelta := float64(sr.CPUStats.CPUUsage.TotalUsage) - float64(sr.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(sr.CPUStats.SystemUsage) - float64(sr.PreCPUStats.SystemUsage)
	if cpuDelta > 0 && sysDelta > 0 && s.CPUs > 0 {
		s.CPUPercent = cpuDelta / sysDelta * float64(s.CPUs) * 100
	}
	for _, n := range sr.Networks {
		s.RxBytes += n.RxBytes
		s.TxBytes += n.TxBytes
	}
	return s
}
