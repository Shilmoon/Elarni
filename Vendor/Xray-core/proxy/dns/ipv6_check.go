package dns

import (
	"context"
	"crypto/tls"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
)

// Elarni: a server with no IPv6 still gets the IPv6 traffic of every app that was handed an AAAA
// answer. TCP to it is reset a round trip later, UDP is dropped without a word, and a client with
// no IPv4 fallback waits out its own timeout. With checkIPv6 the outbound finds out whether the
// route of the inbound that asks reaches public IPv6, and while it does not, its AAAA answers
// hold no public address, so apps use the A answer.

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

// Two operators, so that one of them being down or filtered does not decide alone. Port 443 and
// not 53: DNS from an inbound is routed back to this outbound.
var ipv6Services = []ipv6Service{
	{tcp443("2606:4700:4700::1111"), tcp443("1.1.1.1")},
	{tcp443("2001:4860:4860::8888"), tcp443("8.8.8.8")},
}

func tcp443(ip string) net.Destination {
	return net.TCPDestination(net.ParseAddress(ip), 443)
}

type ipv6Check struct {
	// dial connects to dest along the route of the traffic of the inbound with this tag
	dial     func(ctx context.Context, tag string, dest net.Destination) (net.Conn, error)
	services []ipv6Service
	// timeout is how long the services of one family have to answer
	timeout time.Duration
	// how long a result stands; the first AAAA answer after that has it checked again
	unknownFor, deadFor, aliveFor time.Duration
	now                           func() time.Time

	mu sync.Mutex
	// by inbound tag: routing rules tell inbounds apart
	routes map[string]*ipv6Route
}

type ipv6Route struct {
	state   ipv6State
	checked time.Time
	running bool
}

func newIPv6Check(instance *core.Instance) *ipv6Check {
	return &ipv6Check{
		dial: func(ctx context.Context, tag string, dest net.Destination) (net.Conn, error) {
			return core.Dial(session.ContextWithInbound(ctx, &session.Inbound{Tag: tag}), instance, dest)
		},
		services:   ipv6Services,
		timeout:    5 * time.Second,
		unknownFor: 30 * time.Second,
		deadFor:    10 * time.Minute,
		aliveFor:   30 * time.Minute,
		now:        time.Now,
		routes:     make(map[string]*ipv6Route),
	}
}

// reachable is ips without the public IPv6 addresses that the route of tag does not reach
func (c *ipv6Check) reachable(tag string, ips []net.IP) []net.IP {
	// a lookup with no public IPv6 in it, every one under an IPv4 preference, starts no check
	if !slices.ContainsFunc(ips, publicIPv6) || !c.dead(tag) {
		return ips
	}
	// a new slice: ips may belong to the resolver's cache
	kept := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if !publicIPv6(ip) {
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

// dead reports what is known now, and starts a check when that is nothing or old. It never waits:
// until the first check ends, answers are what they were without it.
func (c *ipv6Check) dead(tag string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.routes[tag]
	if r == nil {
		r = new(ipv6Route)
		c.routes[tag] = r
	}
	if !r.running && (r.checked.IsZero() || c.now().Sub(r.checked) >= c.standsFor(r.state)) {
		r.running = true
		go c.run(tag, r)
	}
	return r.state == ipv6Dead
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
	state := c.check(tag)
	c.mu.Lock()
	was := r.state
	r.state, r.checked, r.running = state, c.now(), false
	c.mu.Unlock()
	if was != state {
		errors.LogInfo(context.Background(), "public IPv6 for traffic of inbound ", tag, " is ", state)
	}
}

func (c *ipv6Check) check(tag string) ipv6State {
	v6 := make([]net.Destination, 0, len(c.services))
	v4 := make([]net.Destination, 0, len(c.services))
	for _, s := range c.services {
		v6, v4 = append(v6, s.v6), append(v4, s.v4)
	}
	// IPv6 first: with a server that has it, the check ends here
	if c.answers(tag, v6) {
		return ipv6Alive
	}
	// only IPv4 to the same services tells a server without IPv6 from a tunnel that is down
	if !c.answers(tag, v4) {
		return ipv6Unknown
	}
	// The route works now. IPv6 gets its second try, in case it failed above because the proxy was
	// still connecting.
	if c.answers(tag, v6) {
		return ipv6Alive
	}
	return ipv6Dead
}

// answers reports whether one of dests sent anything back in time
func (c *ipv6Check) answers(tag string, dests []net.Destination) bool {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	results := make(chan bool, len(dests))
	for _, dest := range dests {
		go func() { results <- c.answered(ctx, tag, dest) }()
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
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	// A proxy tells of a connect that failed at its far end only by closing, so the service has to
	// speak: a TLS hello gets its answer. The handshake ends at the first bytes of it, and nothing
	// is trusted or sent after them. An IP address as the name is not sent either.
	peer := &firstAnswer{Conn: conn}
	_ = tls.Client(peer, &tls.Config{ServerName: dest.Address.IP().String()}).HandshakeContext(ctx)
	return peer.came.Load()
}

// firstAnswer ends the connection for its reader after the first bytes
type firstAnswer struct {
	net.Conn
	came atomic.Bool
}

func (c *firstAnswer) Read(b []byte) (int, error) {
	if c.came.Load() {
		return 0, io.EOF
	}
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.came.Store(true)
	}
	return n, err
}
