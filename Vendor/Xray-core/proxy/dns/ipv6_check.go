package dns

import (
	"context"
	"crypto/tls"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/routing"
	routing_session "github.com/xtls/xray-core/features/routing/session"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/transport/internet"
)

// Elarni: a server with no IPv6 still gets the IPv6 traffic of every app that was handed an AAAA
// answer. TCP to it is reset a round trip later, UDP is dropped without a word, and a client with
// no IPv4 fallback waits out its own timeout. With checkIPv6 the outbound finds out whether the
// route of the inbound that asks reaches public IPv6, and while it does not, its AAAA answers
// hold no public address that would leave by that route, so apps use the A answer.

type ipv6State int

const (
	// not checked yet, or neither family answered: answers stay as they are
	ipv6Unknown ipv6State = iota
	ipv6Alive
	ipv6Dead
)

func (s ipv6State) String() string {
	return [...]string{"not known", "reachable", "not reachable"}[s]
}

// ipv6Service is one public service, at its IPv6 address and at its IPv4 one
type ipv6Service struct{ v6, v4 net.Destination }

// publicIPv6Services are two operators, so that one of them being down or filtered does not decide
// alone. Port 443 and not 53: DNS from an inbound is routed back to this outbound.
func publicIPv6Services() []ipv6Service {
	return []ipv6Service{
		{tcp443("2606:4700:4700::1111"), tcp443("1.1.1.1")},
		{tcp443("2001:4860:4860::8888"), tcp443("8.8.8.8")},
	}
}

func tcp443(ip string) net.Destination {
	return net.TCPDestination(net.ParseAddress(ip), 443)
}

type ipv6Check struct {
	// dial connects to dest along the route of the traffic of the inbound with this tag
	dial func(ctx context.Context, tag string, dest net.Destination) (net.Conn, error)
	// route names the outbound that the traffic of the inbound with this tag to dest, with name as
	// the domain sniffed from it ("" for none), leaves by in the end; "" when routing drops it
	route func(tag string, dest net.Destination, name string) string
	// the services' addresses of each family, which are tried together
	v6, v4 []net.Destination
	// timeout is how long the services of one family have to answer
	timeout time.Duration
	// how long a result stands; the first AAAA answer after that has it checked again
	unknownFor, deadFor, aliveFor time.Duration
	now                           func() time.Time

	// ctx ends with the handler: no check dials through a core that was closed
	ctx     context.Context
	cancel  context.CancelFunc
	unwatch func()
	// probes counts the dials of answers that are still running: it returns at the first answer,
	// so the others end on their own
	probes sync.WaitGroup

	mu sync.Mutex
	// by inbound tag: routing rules tell inbounds apart
	routes map[string]*ipv6Route
}

type ipv6Route struct {
	state ipv6State
	// outbound is the one every service's address was routed to when the result was found, the
	// only one the result speaks for; "" when they were not routed to one
	outbound string
	checked  time.Time
	running  bool
}

func newIPv6Check(dial func(context.Context, string, net.Destination) (net.Conn, error), route func(string, net.Destination, string) string, services []ipv6Service) *ipv6Check {
	c := &ipv6Check{
		dial:       dial,
		route:      route,
		timeout:    5 * time.Second,
		unknownFor: 30 * time.Second,
		deadFor:    10 * time.Minute,
		aliveFor:   30 * time.Minute,
		now:        time.Now,
		routes:     make(map[string]*ipv6Route),
	}
	for _, s := range services {
		c.v6, c.v4 = append(c.v6, s.v6), append(c.v4, s.v4)
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	// Another network or interface may reach IPv6 where the last one did not, or the other way round.
	c.unwatch = internet.OnSocketPathChange(c.forget)
	return c
}

// dialThrough dials as the traffic of an inbound of instance
func dialThrough(instance *core.Instance) func(context.Context, string, net.Destination) (net.Conn, error) {
	return func(ctx context.Context, tag string, dest net.Destination) (net.Conn, error) {
		return core.Dial(session.ContextWithInbound(ctx, &session.Inbound{Tag: tag}), instance, dest)
	}
}

// routeThrough asks router as the dispatcher does: with the dialed address, and with the name a
// TLS client's hello carries as the route target, the way sniffing for routing only sets it. A
// route that ends at a loopback outbound goes on with the routing of its inbound.
func routeThrough(router routing.Router, handlers outbound.Manager) func(string, net.Destination, string) string {
	return func(tag string, dest net.Destination, name string) string {
		ob := &session.Outbound{Target: dest}
		content := &session.Content{}
		if name != "" {
			ob.RouteTarget = net.Destination{Network: dest.Network, Address: net.DomainAddress(name), Port: dest.Port}
			content.Protocol = "tls"
		}
		// a loopback that leads back to itself is dropped by no rule, so the hops are counted
		for range 8 {
			var handler outbound.Handler
			if route, err := router.PickRoute(&routing_session.Context{Inbound: &session.Inbound{Tag: tag}, Outbound: ob, Content: content}); err == nil {
				// the dispatcher drops traffic to an outbound that does not exist
				if handler = handlers.GetHandler(route.GetOutboundTag()); handler == nil {
					return ""
				}
			} else if handler = handlers.GetDefaultHandler(); handler == nil {
				return ""
			}
			holder, ok := handler.(interface{ GetOutbound() proxy.Outbound })
			if !ok {
				return handler.Tag()
			}
			next, ok := holder.GetOutbound().(interface{ InboundTag() string }) // proxy/loopback
			if !ok {
				return handler.Tag()
			}
			tag = next.InboundTag()
		}
		return ""
	}
}

// close ends the checks: a running one stops dialing, and no other starts
func (c *ipv6Check) close() {
	c.cancel()
	c.unwatch()
}

// forget drops every result. A check that is running finds its route gone when it ends, so its
// result, from the path before, is dropped too.
func (c *ipv6Check) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.routes) != 0 {
		c.routes = make(map[string]*ipv6Route)
	}
}

// reachable is ips without the public IPv6 addresses that the traffic of the inbound with tag
// would carry to the outbound the check found without public IPv6. An address that routing sends
// elsewhere, by the name or by the address, stays.
func (c *ipv6Check) reachable(tag, domain string, ips []net.IP) []net.IP {
	// a lookup with no public IPv6 in it, every one under an IPv4 preference, starts no check
	if !slices.ContainsFunc(ips, publicIPv6) {
		return ips
	}
	dead := c.deadOutbound(tag)
	if dead == "" {
		return ips
	}
	name := strings.ToLower(strings.TrimSuffix(domain, "."))
	// a new slice: ips may belong to the resolver's cache
	kept := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if !publicIPv6(ip) || c.route(tag, net.TCPDestination(net.IPAddress(ip), 443), name) != dead {
			kept = append(kept, ip)
		}
	}
	return kept
}

// publicIPv6 tells an address of the IPv6 Internet, which the check speaks for, from every other:
// of the LAN, of this device, of FakeDNS, of a NAT64 prefix. 2000::/3 is all the global unicast
// space in use. 2001:db8::/32 and 3fff::/20 in it are for documentation, and are what a test
// network without the Internet uses.
func publicIPv6(ip net.IP) bool {
	return len(ip) == net.IPv6len && ip[0]&0xe0 == 0x20 &&
		!(ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8) &&
		!(ip[0] == 0x3f && ip[1] == 0xff && ip[2]&0xf0 == 0)
}

// deadOutbound is the outbound through which the traffic of the inbound with tag does not reach
// public IPv6, as far as is known now, or "". It starts a check when nothing is known or the
// result is old, and never waits: until the first check ends, answers are what they were without
// it.
func (c *ipv6Check) deadOutbound(tag string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.routes[tag]
	if r == nil {
		r = new(ipv6Route)
		c.routes[tag] = r
	}
	if !r.running && c.ctx.Err() == nil && (r.checked.IsZero() || c.now().Sub(r.checked) >= c.standsFor(r.state)) {
		r.running = true
		go c.run(tag, r)
	}
	if r.state != ipv6Dead {
		return ""
	}
	return r.outbound
}

func (c *ipv6Check) standsFor(state ipv6State) time.Duration {
	switch state {
	case ipv6Alive:
		return c.aliveFor
	case ipv6Dead:
		return c.deadFor
	}
	return c.unknownFor
}

func (c *ipv6Check) run(tag string, r *ipv6Route) {
	via := c.probeRoute(tag)
	state := ipv6Unknown
	if via != "" {
		state = c.check(tag)
	}
	c.mu.Lock()
	was := r.state
	r.state, r.outbound, r.checked, r.running = state, via, c.now(), false
	c.mu.Unlock()
	if was != state {
		errors.LogInfo(context.Background(), "public IPv6 for traffic of inbound ", tag, " through ", via, " is ", state)
	}
}

// probeRoute is the outbound that every service's address is routed to from tag, or "". Only
// answers that come through one outbound speak for it: IPv4 that works elsewhere does not tell a
// server without IPv6 from a tunnel that is down.
func (c *ipv6Check) probeRoute(tag string) string {
	via := ""
	for _, dest := range slices.Concat(c.v6, c.v4) {
		switch picked := c.route(tag, dest, ""); {
		case picked == "", via != "" && picked != via:
			errors.LogInfo(context.Background(), "public IPv6 for traffic of inbound ", tag, " is not checked: the services are not routed to one outbound")
			return ""
		default:
			via = picked
		}
	}
	return via
}

func (c *ipv6Check) check(tag string) ipv6State {
	// IPv6 first: with a server that has it, the check ends here
	if c.answers(tag, c.v6) {
		return ipv6Alive
	}
	// only IPv4 to the same services tells a server without IPv6 from a tunnel that is down
	if !c.answers(tag, c.v4) {
		return ipv6Unknown
	}
	// The route works now. IPv6 gets its second try, in case it failed above because the proxy was
	// still connecting.
	if c.answers(tag, c.v6) {
		return ipv6Alive
	}
	return ipv6Dead
}

// answers reports whether one of dests began a TLS handshake in time
func (c *ipv6Check) answers(tag string, dests []net.Destination) bool {
	if c.ctx.Err() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
	defer cancel()
	results := make(chan bool, len(dests))
	for _, dest := range dests {
		c.probes.Add(1)
		go func() {
			defer c.probes.Done()
			results <- c.answered(ctx, tag, dest)
		}()
	}
	for range dests {
		if <-results {
			return true
		}
	}
	return false
}

func (c *ipv6Check) answered(ctx context.Context, tag string, dest net.Destination) bool {
	conn, err := c.dial(ctx, tag, dest)
	if err != nil {
		return false
	}
	defer conn.Close()
	// A proxy tells of a connect that failed at its far end only by closing, so the service has to
	// speak: a TLS hello gets its answer. The handshake ends at the first bytes of it, and nothing
	// is trusted or sent after them. An IP address as the name is not sent either. HandshakeContext
	// closes conn when ctx ends.
	peer := &firstAnswer{Conn: conn}
	_ = tls.Client(peer, &tls.Config{ServerName: dest.Address.IP().String()}).HandshakeContext(ctx)
	return peer.handshake
}

// firstAnswer ends the connection for its reader after the first bytes, and tells whether they
// begin a TLS handshake record. Anything else is not the service: a blackhole's HTTP 403, or a
// captive portal, written by a proxy or a router on the way.
type firstAnswer struct {
	net.Conn
	// came and handshake are only read after the handshake, in the goroutine that ran it
	came, handshake bool
}

func (c *firstAnswer) Read(b []byte) (int, error) {
	if c.came {
		return 0, io.EOF
	}
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.came = true
		c.handshake = b[0] == 0x16 // the content type of a TLS handshake record
	}
	return n, err
}
