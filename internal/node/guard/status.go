// SPDX-License-Identifier: GPL-3.0-or-later

package guard

import (
	"io"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
)

// PrintStatus is the Host guard section of `cs2node status`. It reads the
// live nftables table rather than the daemon snapshot, so it still says
// something useful when the daemon is down.
func (m *Module) PrintStatus(w io.Writer, c *core.Core, st core.Status) {
	cfg := Defaults()
	if c.Config(m.Name(), &cfg) != nil {
		return
	}
	t, err := NewTools(cfg, &st, w)
	if err != nil {
		return
	}
	t.StatusReport()
}
