package netutil

import (
	"net/netip"
	"testing"
)

func TestKey(t *testing.T) {
	k := func(s string) string { return Key(netip.MustParseAddr(s)) }

	if k("203.0.113.7") != "203.0.113.7" {
		t.Fatal("IPv4 is its own key")
	}
	if k("::ffff:203.0.113.7") != "203.0.113.7" {
		t.Fatal("an IPv4-mapped address counts as IPv4")
	}
	if k("2001:db8:1:2:aaaa::1") != k("2001:db8:1:2:bbbb::2") {
		t.Fatal("addresses in one /64 must share a key")
	}
	if k("2001:db8:1:2::1") == k("2001:db8:1:3::1") {
		t.Fatal("different /64s must not share a key")
	}
	if Key(netip.Addr{}) != "" {
		t.Fatal("an invalid address has no key")
	}
}
