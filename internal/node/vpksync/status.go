// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync

import (
	"encoding/json"
	"io"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// PrintStatus is the VPK sync section of `cs2node status`.
func (m *Module) PrintStatus(w io.Writer, c *core.Core, st core.Status) {
	ui.Section(w, "VPK sync")
	var vs status
	json.Unmarshal(st.Module[m.Name()], &vs)
	build := vs.Build
	if build == "" {
		build = ui.Yellow("no central install yet")
	}
	ui.KV(w, "central build", build)
	var cfg Config
	if c.Config(m.Name(), &cfg) != nil {
		return
	}
	ui.KV(w, "central dir", cfg.CS2Dir)
	ui.KV(w, "push method", string(cfg.PushMethod))
	if !cfg.AutoRestart {
		return
	}
	policy := string(cfg.RestartPolicy)
	if policy == "" {
		policy = "immediate"
	}
	if cfg.RestartPolicy == InWindow {
		policy += " " + cfg.RestartWindow
	}
	ui.KV(w, "restart policy", policy)
}

// ServerState is what the servers table shows for one container: the last
// push verdict and whether a restart is being held back.
func ServerState(st core.Status, container string) (last string, restartPending bool) {
	var vs status
	if json.Unmarshal(st.Module["vpksync"], &vs) != nil {
		return "", false
	}
	_, restartPending = vs.Pending[container]
	return vs.Last[container], restartPending
}
