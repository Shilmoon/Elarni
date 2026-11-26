package tun

import "net/netip"

// routeNextHop is the next hop a route of the Windows adapter gets (Elarni): another address of
// the subnet of the adapter's first IPv4 address, which makes it a gateway route. Wintun has no
// link layer, so the packets are the same; what differs is how others read the route. WSL in
// mirrored mode copies an on-link route to Linux as "scope link", and Docker gives no network a
// subnet inside one, so with the tunnel's wide routes it found none in its pools
// (moby/moby#51863); a gateway route it passes. Unspecified, which is on-link, stay: IPv6, where
// on-link routes do no harm; a route that lies wholly in multicast or is the limited broadcast,
// which go to no gateway and have to match the routes Windows sets itself (a route that also
// holds unicast addresses gets the next hop); a route inside any of the adapter's IPv4 subnets,
// where the next hop is; and an adapter with no second address.
func routeNextHop(addresses []netip.Prefix, destination netip.Prefix) netip.Addr {
	if !destination.Addr().Is4() {
		return netip.IPv6Unspecified()
	}
	onLink := netip.IPv4Unspecified()
	multicast := netip.PrefixFrom(netip.AddrFrom4([4]byte{224, 0, 0, 0}), 4)
	limitedBroadcast := netip.PrefixFrom(netip.AddrFrom4([4]byte{255, 255, 255, 255}), 32)
	if multicast.Bits() <= destination.Bits() && multicast.Contains(destination.Addr()) || destination == limitedBroadcast {
		return onLink
	}
	for _, address := range addresses {
		if subnet := address.Masked(); address.Addr().Is4() && subnet.Bits() <= destination.Bits() && subnet.Contains(destination.Addr()) {
			return onLink
		}
	}
	for _, address := range addresses {
		if !address.Addr().Is4() {
			continue
		}
		// The first address that is not the adapter's own: below /31 that skips the network
		// address, and the broadcast address is never reached.
		subnet := address.Masked()
		hop := subnet.Addr()
		if subnet.Bits() < 31 {
			hop = hop.Next()
		}
		if hop == address.Addr() {
			hop = hop.Next()
		}
		if !subnet.Contains(hop) {
			return onLink
		}
		return hop
	}
	return onLink
}
