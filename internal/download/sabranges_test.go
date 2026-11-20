package download

import (
	"net/netip"
	"strings"
	"testing"
)

// The ranges are what the Compose seed and the all-in-one seed write too (each pinned to
// this same text by its own test): SABnzbd's own private ranges and Tailscale's, no wider.
func TestSABLocalRangesAreTheLANAndTailscale(t *testing.T) {
	const want = "10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, fc00::/7, fe80::/10, 100.64.0.0/10"
	if SABLocalRanges != want {
		t.Fatalf("SABLocalRanges = %q", SABLocalRanges)
	}
	var nets []netip.Prefix
	for _, r := range strings.Split(SABLocalRanges, ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(r))
		if err != nil {
			t.Fatalf("%q isn't a range: %v", r, err)
		}
		nets = append(nets, p)
	}
	in := func(ip string) bool {
		a := netip.MustParseAddr(ip)
		for _, n := range nets {
			if n.Contains(a) {
				return true
			}
		}
		return false
	}
	for _, ip := range []string{"192.168.50.20", "10.1.2.3", "172.20.0.1", "100.123.119.5", "100.64.0.1", "100.127.255.254", "fd7a:115c:a1e0::1"} {
		if !in(ip) {
			t.Errorf("%s should reach the page", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "100.63.255.255", "100.128.0.1", "172.32.0.1", "2001:4860:4860::8888"} {
		if in(ip) {
			t.Errorf("%s is the internet and must stay refused", ip)
		}
	}
}
