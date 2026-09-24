// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package core

import (
	"os"
	"path/filepath"
)

// listenPath without /proc: the plain path (tests on macOS).
func listenPath(dir *os.File, name string) string { return filepath.Join(dir.Name(), name) }
