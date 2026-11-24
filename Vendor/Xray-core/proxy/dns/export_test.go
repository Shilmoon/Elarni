package dns

import "github.com/xtls/xray-core/common/net"

// SetIPv6Services makes the IPv6 check of handlers built from now on connect to these two
// addresses instead of the public services. The returned func puts those back.
func SetIPv6Services(v6, v4 net.Destination) (restore func()) {
	public := ipv6Services
	ipv6Services = []ipv6Service{{v6: v6, v4: v4}}
	return func() { ipv6Services = public }
}

// IPv6State is what the handler knows of public IPv6 for the traffic of an inbound
func (h *Handler) IPv6State(tag string) string {
	h.ipv6.mu.Lock()
	defer h.ipv6.mu.Unlock()
	if r := h.ipv6.routes[tag]; r != nil && !r.running {
		return r.state.String()
	}
	return ipv6Unknown.String()
}
