// SPDX-License-Identifier: GPL-3.0-or-later

// Package netx classifies addresses the same way on the node and inside the
// egg: what may be blocked, what is the docker bridge, what a whitelist covers.
package netx

import (
	"fmt"
	"net/netip"
	"strings"
)

// never blockable, independent of the configured bridge subnet
var privateV4 = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4",
)

var privateV6 = mustPrefixes("fc00::/7", "fe80::/10", "ff00::/8")

// ParseAddr accepts a bare IPv4 or IPv6 address and nothing else: no CIDR,
// no zone, no leading zeros, no trailing junk. IPv4-mapped IPv6 is unmapped.
// This is the trust boundary in front of every nftables operation.
func ParseAddr(s string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(s)
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// IsPublic reports whether ip may ever be blocked: not loopback, link-local,
// private, multicast, reserved, and not inside the docker bridge subnet
// (its gateway fronts every docker-proxied client).
func IsPublic(ip netip.Addr, bridge netip.Prefix) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() {
		return false
	}
	if bridge.IsValid() && bridge.Contains(ip) {
		return false
	}
	if ip.Is4() {
		return !InAny(ip, privateV4)
	}
	return !InAny(ip, privateV6)
}

// ParsePrefixList parses a whitelist: addresses or CIDRs separated by spaces
// or commas. A bare address becomes a /32 or /128.
func ParsePrefixList(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' }) {
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
			continue
		}
		ip, ok := ParseAddr(f)
		if !ok {
			return nil, fmt.Errorf("invalid address or cidr %q", f)
		}
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return out, nil
}

// InAny reports whether ip falls inside any of the prefixes.
func InAny(ip netip.Addr, list []netip.Prefix) bool {
	ip = ip.Unmap()
	for _, p := range list {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}
