// Package netutil holds small helpers for treating client addresses consistently.
package netutil

import "net/netip"

// Key returns the identity under which a client address is rate limited. An IPv4 address stands for
// itself. An IPv6 address stands for its whole /64: a single subscriber normally controls an entire /64,
// so counting individual IPv6 addresses would let one machine dodge every per-address limit by rotating
// addresses. IPv4-mapped IPv6 addresses count as IPv4. An invalid address yields "".
func Key(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	a = a.Unmap()
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return a.String()
}
