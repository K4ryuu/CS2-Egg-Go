// SPDX-License-Identifier: GPL-3.0-or-later

package guard

import (
	"net/netip"
	"time"
)

// BlockEntry is one active block.
type BlockEntry struct {
	IP      netip.Addr
	Timeout time.Duration // the block's full length
	Expires time.Duration // time left
}

// Counter is a per-source packet count.
type Counter struct {
	Packets, Bytes uint64
}

// RateSample is the cumulative per-source counters of the rate sets.
type RateSample map[netip.Addr]Counter

// Firewall is the nftables table as the module needs it. Tests fake it.
type Firewall interface {
	// Apply creates or refreshes the table: sets, chain and rules. Blocks
	// survive; a layout change (tag mismatch) recreates everything.
	Apply(cfg Config, allow []netip.Prefix) (recreated bool, err error)
	// Loaded reports whether the table and chain exist.
	Loaded() bool
	// RefreshPorts replaces the protected port set in one transaction.
	RefreshPorts(ports []uint16) error
	// Block (re)adds ip with a fresh timeout.
	Block(ip netip.Addr, d time.Duration) error
	Unblock(ip netip.Addr) error
	UnblockAll() error
	// AddPlayer marks ip as a joined player for d.
	AddPlayer(ip netip.Addr, d time.Duration) error
	Blocks() ([]BlockEntry, error)
	RateSample() (RateSample, error)
	// Ports lists the protected ports.
	Ports() ([]uint16, error)
	// RuleCount is the number of rules in the chain.
	RuleCount() (int, error)
}
