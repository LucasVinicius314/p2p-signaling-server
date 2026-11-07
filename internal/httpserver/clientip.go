package httpserver

import (
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the address of the real client. X-Forwarded-For is only believed when the
// direct peer is inside trusted. The header is read right to left, skipping trusted proxies
// (CapRover's network, Cloudflare), and the first untrusted address is the client. Entries to
// the left of it are client-supplied and ignored.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	remote := remoteAddr(r)
	if !contains(trusted, remote) {
		return remote
	}
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return remote // malformed header: do not guess
		}
		a = a.Unmap()
		if !contains(trusted, a) {
			return a
		}
	}
	return remote
}

func remoteAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}
	if a, err := netip.ParseAddr(r.RemoteAddr); err == nil {
		return a.Unmap()
	}
	return netip.Addr{}
}

func contains(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
