package main

import "net/netip"

// guardFilter is one filter of the Windows DNS guard before protectDNS encodes it for WFP.
// Within the guard's sublayer the matching filter with the highest weight decides.
// It has no build tag, so its tests run on every platform.
type guardFilter struct {
	v6      bool         // FWPM_LAYER_ALE_AUTH_CONNECT_V6, else _V4
	permit  bool         // else block
	weight  uintptr      // FWP_UINT8
	dns     uintptr      // the IP protocol of a connection to remote port 53; 0 for any connection
	program int          // 1 + the index of the one program it matches; 0 for every program
	remote  netip.Prefix // the remote addresses it matches; the zero prefix for any
	offLink bool         // only a connection whose local interface is not the tunnel
}

// A resolver the user runs on this PC (dnscrypt-proxy, Acrylic, AdGuard Home) listens on
// loopback. Its own lookups to its upstream servers are other connections: a plain one
// to port 53 that would leave off the tunnel is still blocked.
var loopbackDNS = [2]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}

// guardFilters is the DNS guard of a tunnel. Off the tunnel, port 53 is blocked except for
// the helper (program 1) and loopback. With protected routes, connections to them are
// blocked off the tunnel too, except for the programs that must reach the servers.
func guardFilters(programs int, protected []netip.Prefix) []guardFilter {
	var filters []guardFilter
	for family, v6 := range []bool{false, true} {
		if len(protected) > 0 {
			for program := 1; program <= programs; program++ {
				filters = append(filters, guardFilter{v6: v6, permit: true, weight: 2, program: program})
			}
		}
		for _, protocol := range []uintptr{6, 17} {
			filters = append(filters,
				guardFilter{v6: v6, permit: true, weight: 2, dns: protocol, program: 1},
				guardFilter{v6: v6, permit: true, weight: 2, dns: protocol, remote: loopbackDNS[family]},
				guardFilter{v6: v6, weight: 1, dns: protocol, offLink: true})
		}
	}
	for _, prefix := range protected {
		filters = append(filters, guardFilter{v6: !prefix.Addr().Is4(), weight: 1, remote: prefix, offLink: true})
	}
	return filters
}
