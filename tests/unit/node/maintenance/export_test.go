// SPDX-License-Identifier: GPL-3.0-or-later

package maintenance_test

import (
	"os/exec"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/maintenance"
)

func execNotFound() error { return exec.ErrNotFound }

func setCfg(m *maintenance.Module, c maintenance.Config) { m.SetConfig(c) }
