package main

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestDNSGuardFilterList(t *testing.T) {
	loopback4, loopback6 := netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")
	server := netip.MustParsePrefix("2001:db8::/32")
	var want []guardFilter
	for family, v6 := range []bool{false, true} {
		want = append(want,
			guardFilter{v6: v6, permit: true, weight: 2, program: 1},
			guardFilter{v6: v6, permit: true, weight: 2, program: 2})
		for _, protocol := range []uintptr{6, 17} {
			want = append(want,
				guardFilter{v6: v6, permit: true, weight: 2, dns: protocol, program: 1},
				guardFilter{v6: v6, permit: true, weight: 2, dns: protocol, remote: [2]netip.Prefix{loopback4, loopback6}[family]},
				guardFilter{v6: v6, weight: 1, dns: protocol, offLink: true})
		}
	}
	want = append(want, guardFilter{v6: true, weight: 1, remote: server, offLink: true})
	if got := guardFilters(2, []netip.Prefix{server}); !reflect.DeepEqual(got, want) {
		t.Fatalf("filters:\n got %+v\nwant %+v", got, want)
	}
	// Without protected routes no program needs a filter of its own beyond the helper's DNS permit.
	if got := guardFilters(2, nil); len(got) != 12 {
		t.Fatalf("got %d filters without protected routes, want 12: %+v", len(got), got)
	}
}

// guardDecision is what the guard's sublayer decides for a connection: the matching
// filter with the highest weight, or no decision (the connection goes on) when none matches.
func guardDecision(filters []guardFilter, program int, protocol uintptr, remote netip.AddrPort, onTunnel bool) (permit, matched bool) {
	var weight uintptr
	for _, filter := range filters {
		if filter.v6 != remote.Addr().Is6() || filter.program != 0 && filter.program != program ||
			filter.dns != 0 && (filter.dns != protocol || remote.Port() != 53) ||
			filter.remote.IsValid() && !filter.remote.Contains(remote.Addr()) || filter.offLink && onTunnel {
			continue
		}
		if !matched || filter.weight > weight {
			permit, matched, weight = filter.permit, true, filter.weight
		}
	}
	return permit, matched
}

func TestDNSGuardLetsLoopbackResolversThrough(t *testing.T) {
	filters := guardFilters(1, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")})
	const app = 3 // a program the guard does not name
	for _, test := range []struct {
		name     string
		program  int
		protocol uintptr
		remote   string
		onTunnel bool
		permit   bool
		matched  bool
	}{
		{"a resolver on IPv4 loopback", app, 17, "127.0.0.1:53", false, true, true},
		{"a resolver on another loopback address", app, 6, "127.0.0.53:53", false, true, true},
		{"a resolver on IPv6 loopback", app, 17, "[::1]:53", false, true, true},
		{"a resolver on IPv6 loopback over TCP", app, 6, "[::1]:53", false, true, true},
		{"a LAN resolver", app, 17, "192.168.1.1:53", false, false, true},
		{"a LAN resolver over TCP", app, 6, "192.168.1.1:53", false, false, true},
		{"a public resolver", app, 17, "198.51.100.53:53", false, false, true},
		{"an IPv6 link-local resolver", app, 17, "[fe80::1]:53", false, false, true},
		{"the helper's own lookup", 1, 17, "198.51.100.53:53", false, true, true},
		{"a lookup through the tunnel", app, 17, "198.51.100.53:53", true, false, false},
		{"loopback on another port", app, 17, "127.0.0.1:5353", false, false, false},
		{"a protected route off the tunnel", app, 6, "192.0.2.7:443", false, false, true},
		{"a protected route from the helper", 1, 6, "192.0.2.7:443", false, true, true},
	} {
		permit, matched := guardDecision(filters, test.program, test.protocol, netip.MustParseAddrPort(test.remote), test.onTunnel)
		if permit != test.permit || matched != test.matched {
			t.Errorf("%s: permit %v, matched %v; want %v, %v", test.name, permit, matched, test.permit, test.matched)
		}
	}
}
