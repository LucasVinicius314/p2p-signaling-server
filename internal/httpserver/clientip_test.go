package httpserver

import (
	"net/http"
	"net/netip"
	"testing"
)

func req(remote string, xff ...string) *http.Request {
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("173.245.48.0/20")}
	cases := []struct {
		name string
		r    *http.Request
		want string
	}{
		{"direct, no proxy", req("203.0.113.9:5000"), "203.0.113.9"},
		{"untrusted peer cannot spoof", req("203.0.113.9:5000", "1.2.3.4"), "203.0.113.9"},
		{"trusted proxy, one hop", req("10.0.0.2:80", "198.51.100.7"), "198.51.100.7"},
		{"cloudflare then capRover", req("10.0.0.2:80", "198.51.100.7, 173.245.48.5"), "198.51.100.7"},
		{"client-supplied prefix ignored", req("10.0.0.2:80", "6.6.6.6, 198.51.100.7, 173.245.48.5"), "198.51.100.7"},
		{"multiple header lines", req("10.0.0.2:80", "6.6.6.6", "198.51.100.7"), "198.51.100.7"},
		{"malformed header falls back to peer", req("10.0.0.2:80", "garbage"), "10.0.0.2"},
		{"trusted peer, no header", req("10.0.0.2:80"), "10.0.0.2"},
		{"ipv4-mapped ipv6", req("[::ffff:203.0.113.9]:1"), "203.0.113.9"},
	}
	for _, c := range cases {
		if got := ClientIP(c.r, trusted); got.String() != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	if got := ClientIP(req("10.0.0.2:80", "6.6.6.6"), nil); got.String() != "10.0.0.2" {
		t.Errorf("with no trusted proxies the header must be ignored, got %s", got)
	}
}
