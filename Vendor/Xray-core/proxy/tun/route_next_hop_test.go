package tun

import (
	"net/netip"
	"testing"
)

// Elarni: a route without a next hop is an on-link one, which WSL in mirrored mode shows Linux as
// "scope link", and Docker gives no network a subnet inside such a route: with the tunnel's wide
// routes it found none in its own pools. A next hop inside the adapter's own subnet makes them
// gateway routes; what stays on-link is what a gateway cannot carry or what must match a route
// Windows sets itself.
func TestRouteNextHopIsAnotherAddressOfTheAdapterSubnet(t *testing.T) {
	parse := func(values ...string) []netip.Prefix {
		var prefixes []netip.Prefix
		for _, value := range values {
			prefixes = append(prefixes, netip.MustParsePrefix(value))
		}
		return prefixes
	}
	desktop := parse("198.51.100.254/30", "fd76:6f75:7465::2/126")
	for _, c := range []struct {
		name        string
		addresses   []netip.Prefix
		destination string
		want        string
	}{
		{"wide route", desktop, "128.0.0.0/2", "198.51.100.253"},
		{"low half", desktop, "0.0.0.0/1", "198.51.100.253"},
		{"host route", desktop, "203.0.113.7/32", "198.51.100.253"},
		{"adapter on the first address", parse("10.9.0.1/24"), "192.0.0.0/3", "10.9.0.2"},
		{"adapter on a later address", parse("10.9.0.7/24"), "192.0.0.0/3", "10.9.0.1"},
		{"two addresses", parse("192.0.2.1/31"), "192.0.0.0/3", "192.0.2.0"},
		{"IPv6 address listed first", parse("fd76:6f75:7465::2/126", "198.51.100.254/30"), "128.0.0.0/2", "198.51.100.253"},
		{"no other address", parse("192.0.2.1/32"), "128.0.0.0/2", "0.0.0.0"},
		{"no IPv4 address", parse("fd76:6f75:7465::2/126"), "128.0.0.0/2", "0.0.0.0"},
		{"the adapter's own subnet", desktop, "198.51.100.252/30", "0.0.0.0"},
		{"inside the adapter's own subnet", desktop, "198.51.100.253/32", "0.0.0.0"},
		{"a route holding the adapter's subnet", desktop, "198.51.100.0/24", "198.51.100.253"},
		{"multicast", desktop, "224.0.0.0/4", "0.0.0.0"},
		{"one multicast group", desktop, "224.0.0.251/32", "0.0.0.0"},
		{"limited broadcast", desktop, "255.255.255.255/32", "0.0.0.0"},
		{"IPv6", desktop, "8000::/2", "::"},
		{"IPv6 multicast", desktop, "ff00::/8", "::"},
	} {
		if got := routeNextHop(c.addresses, netip.MustParsePrefix(c.destination)); got != netip.MustParseAddr(c.want) {
			t.Errorf("%s: next hop of %s with %v is %s, want %s", c.name, c.destination, c.addresses, got, c.want)
		}
	}
}
