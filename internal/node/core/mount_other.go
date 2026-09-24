// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package core

import "errors"

// Mounted is linux-only; elsewhere nothing is ever mounted.
func Mounted(int, string) bool { return false }

// BindReadOnly needs linux mount namespaces.
func BindReadOnly(int, string, string) error {
	return errors.New("bind mounts into containers need linux")
}
