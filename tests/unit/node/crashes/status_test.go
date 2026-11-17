// SPDX-License-Identifier: GPL-3.0-or-later

package crashes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/crashes"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// a crash before the first map load has no map
func TestUnknownMapIsLeftOut(t *testing.T) {
	dir := t.TempDir()
	meta, err := crashes.Bundle(dir, "srv-1", t.TempDir(), &proto.Crash{ExitCode: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	m := crashes.New(log)
	raw, _ := json.Marshal(map[string]crashes.Meta{"srv-1": meta})
	var out bytes.Buffer
	m.PrintStatus(&out, nil, core.Status{Containers: []core.ContainerStatus{{Name: "srv-1"}}, Module: map[string]json.RawMessage{m.Name(): raw}})
	ccfg := crashes.Defaults()
	ccfg.Dir = dir
	cfg := nodeconfig.Default()
	cfg.SetSection(m.Name(), ccfg)
	for _, ch := range m.Doctor(context.Background(), core.New(nil, cfg, nil, log)) {
		out.WriteString(ch.Text + "\n")
	}
	if strings.Count(out.String(), "exit") != 2 {
		t.Fatalf("want a status and a doctor line:\n%s", out.String())
	}
	for line := range strings.Lines(out.String()) {
		if line = strings.TrimSpace(line); strings.HasSuffix(line, " on") || strings.Contains(line, ", )") {
			t.Errorf("empty map printed: %q", line)
		}
	}
}
