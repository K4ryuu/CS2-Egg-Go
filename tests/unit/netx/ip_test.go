// SPDX-License-Identifier: GPL-3.0-or-later

package netx_test

import (
	"net/netip"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/netx"
)

var bridge = netip.MustParsePrefix("172.18.0.0/16")

func TestIsPublicV4(t *testing.T) {
	private := []string{"172.18.0.1", "10.1.2.3", "127.0.0.1", "169.254.9.9", "192.168.1.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "240.0.0.1"}
	public := []string{"8.8.8.8", "172.32.0.1", "100.64.0.1", "213.189.218.189"}
	for _, s := range private {
		if netx.IsPublic(netip.MustParseAddr(s), bridge) {
			t.Errorf("%s must be private", s)
		}
	}
	for _, s := range public {
		if !netx.IsPublic(netip.MustParseAddr(s), bridge) {
			t.Errorf("%s must be public", s)
		}
	}
}

func TestIsPublicV6(t *testing.T) {
	private := []string{"::1", "::", "fe80::1", "fd00::1", "fc00::1", "ff02::1", "FE80::1"}
	public := []string{"2001:db8::1", "2a00:1450::8"}
	for _, s := range private {
		if netx.IsPublic(netip.MustParseAddr(s), bridge) {
			t.Errorf("%s must be private", s)
		}
	}
	for _, s := range public {
		if !netx.IsPublic(netip.MustParseAddr(s), bridge) {
			t.Errorf("%s must be public", s)
		}
	}
}

func TestCustomBridgeSubnetIsPrivate(t *testing.T) {
	custom := netip.MustParsePrefix("100.99.0.0/24")
	ip := netip.MustParseAddr("100.99.0.5")
	if netx.IsPublic(ip, custom) {
		t.Fatal("address inside the configured bridge must be private")
	}
	if !netx.IsPublic(ip, bridge) {
		t.Fatal("same address is public under the default bridge")
	}
}

func TestParseAddrIsStrict(t *testing.T) {
	bad := []string{"1.2.3.4;flush", "1.2.3", "999.1.1.1", "1.2.3.4 timeout", "", "1.2.3.4/32", "::g", "1:::2", "$(reboot)", "08.1.1.1"}
	for _, s := range bad {
		if _, ok := netx.ParseAddr(s); ok {
			t.Errorf("%q must be rejected", s)
		}
	}
	good := map[string]string{"1.2.3.4": "1.2.3.4", "2001:db8::1": "2001:db8::1", "::1": "::1", "255.255.255.255": "255.255.255.255", "::ffff:1.2.3.4": "1.2.3.4"}
	for s, want := range good {
		ip, ok := netx.ParseAddr(s)
		if !ok || ip.String() != want {
			t.Errorf("ParseAddr(%q) = %v, %v; want %s", s, ip, ok, want)
		}
	}
}

func TestPrefixListAndMembership(t *testing.T) {
	list, err := netx.ParsePrefixList("1.2.3.4 5.0.0.0/8, 2001:DB8::5")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d prefixes, want 3", len(list))
	}
	in := []string{"1.2.3.4", "5.1.1.1", "2001:db8::5"}
	out := []string{"1.2.3.5", "6.1.1.1", "2001:db8::6"}
	for _, s := range in {
		if !netx.InAny(netip.MustParseAddr(s), list) {
			t.Errorf("%s must match", s)
		}
	}
	for _, s := range out {
		if netx.InAny(netip.MustParseAddr(s), list) {
			t.Errorf("%s must not match", s)
		}
	}
	if _, err := netx.ParsePrefixList("1.2.3.4 junk/99"); err == nil {
		t.Fatal("junk CIDR must be rejected")
	}
	if list, _ := netx.ParsePrefixList("  "); len(list) != 0 {
		t.Fatal("blank list must be empty")
	}
}
