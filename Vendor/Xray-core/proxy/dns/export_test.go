package dns

import (
	"time"

	"github.com/xtls/xray-core/common/net"
)

// SetIPv6Services makes the IPv6 check of this handler connect to these two addresses instead of
// the public services, and give each family timeout to answer. Call it before the handler answers
// its first query.
func (h *Handler) SetIPv6Services(v6, v4 net.Destination, timeout time.Duration) {
	h.ipv6.mu.Lock()
	defer h.ipv6.mu.Unlock()
	h.ipv6.v6, h.ipv6.v4, h.ipv6.timeout = []net.Destination{v6}, []net.Destination{v4}, timeout
}

// IPv6CheckEnded reports whether the handler's IPv6 check was ended
func (h *Handler) IPv6CheckEnded() bool { return h.ipv6.ctx.Err() != nil }

// IPv6State is what the handler knows of public IPv6 for the traffic of an inbound
func (h *Handler) IPv6State(tag string) string {
	h.ipv6.mu.Lock()
	defer h.ipv6.mu.Unlock()
	if r := h.ipv6.routes[tag]; r != nil && !r.running {
		return r.state.String()
	}
	return ipv6Unknown.String()
}
