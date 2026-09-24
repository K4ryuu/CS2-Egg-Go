// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package guard

import "errors"

// RuleTotal is how many rules Apply installs.
const RuleTotal = 21

// NewFirewall needs Linux nf_tables.
func NewFirewall() (Firewall, error) {
	return nil, errors.New("nftables needs linux")
}
