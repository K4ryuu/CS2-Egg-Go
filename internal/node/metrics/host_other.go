// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package metrics

// No /proc off Linux: the header falls back to the servers' sums.
func hostCPU() (busy, total uint64, cores int, ok bool) { return 0, 0, 0, false }
func hostMem() (used, total uint64, ok bool)            { return 0, 0, false }
