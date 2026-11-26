package dns

import (
	"context"
	"io"
	gonet "net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/signal"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/transport/internet"
	"golang.org/x/net/dns/dnsmessage"
)

// fakeRoute stands for the routing of an inbound: which outbound traffic leaves by, and what a
// connection to each address meets there
type fakeRoute struct {
	// meets is asked with the address and how many times it has been dialed, this time included:
	// "answers" (a TLS server), "http" (an HTTP answer written on the way, as by a blackhole),
	// "closes" (the far end of a proxy could not connect), "silent" (packets are dropped), or
	// anything else for a dial that fails here
	meets func(address string, attempt int) string
	// via names the outbound that traffic of tag to dest, with name sniffed from it, leaves by;
	// nil sends everything to "proxy"
	via func(tag string, dest net.Destination, name string) string

	mu    sync.Mutex
	dials map[string]int
}

func (f *fakeRoute) route(tag string, dest net.Destination, name string) string {
	if f.via == nil {
		return "proxy"
	}
	return f.via(tag, dest, name)
}

func (f *fakeRoute) dial(_ context.Context, _ string, dest net.Destination) (net.Conn, error) {
	address := dest.Address.IP().String()
	f.mu.Lock()
	if f.dials == nil {
		f.dials = make(map[string]int)
	}
	f.dials[address]++
	attempt := f.dials[address]
	f.mu.Unlock()

	client, server := gonet.Pipe()
	switch f.meets(address, attempt) {
	case "answers":
		go func() {
			defer server.Close()
			if _, err := server.Read(make([]byte, 16384)); err == nil {
				server.Write([]byte{22, 3, 3, 0, 1, 0})
			}
		}()
	case "http":
		go func() {
			defer server.Close()
			if _, err := server.Read(make([]byte, 16384)); err == nil {
				server.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
			}
		}()
	case "closes":
		server.Close()
	case "silent":
		go io.Copy(io.Discard, server)
	default:
		client.Close()
		server.Close()
		return nil, errors.New("no route")
	}
	return client, nil
}

func (f *fakeRoute) dialed(address string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials[address]
}

const (
	testV6a, testV6b = "2001:db8::a", "2001:db8::b"
	testV4a, testV4b = "192.0.2.1", "192.0.2.2"
)

func testIPv6Check(t *testing.T, route *fakeRoute) *ipv6Check {
	c := newIPv6Check(route.dial, route.route, []ipv6Service{{tcp443(testV6a), tcp443(testV4a)}, {tcp443(testV6b), tcp443(testV4b)}})
	c.timeout = 100 * time.Millisecond
	t.Cleanup(c.close)
	return c
}

func isIPv6(address string) bool { return address == testV6a || address == testV6b }

// withoutIPv6 is a server that cannot connect over IPv6
func withoutIPv6(address string, _ int) string {
	if isIPv6(address) {
		return "closes"
	}
	return "answers"
}

func dead(c *ipv6Check, tag string) bool { return c.deadOutbound(tag) != "" }

// settled waits for the check of tag that is running to end, and returns its result
func settled(t *testing.T, c *ipv6Check, tag string) ipv6State {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		c.mu.Lock()
		r := c.routes[tag]
		if r != nil && !r.running && !r.checked.IsZero() {
			state := r.state
			c.mu.Unlock()
			c.probes.Wait() // the dials it did not wait for are counted too
			return state
		}
		c.mu.Unlock()
	}
	t.Fatal("the check did not end")
	return ipv6Unknown
}

// Elarni: only a server that carries IPv4 to the same services and no IPv6 has "no IPv6". One
// service that is down, a tunnel that is down and a proxy that was still connecting when IPv6 was
// tried must not take IPv6 answers from a server that has IPv6. Bytes that are not the service's
// TLS answer, such as the HTTP 403 of a blackhole, are no answer.
func TestIPv6CheckTellsAServerWithoutIPv6FromEveryOtherFailure(t *testing.T) {
	for _, c := range []struct {
		name  string
		meets func(address string, attempt int) string
		want  ipv6State
		// how many times the IPv4 addresses may be dialed
		v4Dials int
	}{
		{"a server with IPv6", func(string, int) string { return "answers" }, ipv6Alive, 0},
		{"a server with IPv6 while one service is down", func(address string, _ int) string {
			if address == testV6a {
				return "closes"
			}
			return "answers"
		}, ipv6Alive, 0},
		{"a server that cannot connect over IPv6", withoutIPv6, ipv6Dead, 2},
		{"a server that drops IPv6 without a word", func(address string, _ int) string {
			if isIPv6(address) {
				return "silent"
			}
			return "answers"
		}, ipv6Dead, 2},
		{"a profile that blocks IPv6 with an HTTP answer", func(address string, _ int) string {
			if isIPv6(address) {
				return "http"
			}
			return "answers"
		}, ipv6Dead, 2},
		{"a route that answers everything with HTTP", func(string, int) string { return "http" }, ipv6Unknown, 2},
		{"a tunnel that is down", func(string, int) string { return "fails" }, ipv6Unknown, 2},
		{"a tunnel that carries nothing", func(string, int) string { return "silent" }, ipv6Unknown, 2},
		{"a proxy that was still connecting at the first try", func(address string, attempt int) string {
			if isIPv6(address) && attempt == 1 {
				return "closes"
			}
			return "answers"
		}, ipv6Alive, 2},
	} {
		route := &fakeRoute{meets: c.meets}
		check := testIPv6Check(t, route)
		if got := check.check("apps"); got != c.want {
			t.Errorf("%s: IPv6 is %v, want %v", c.name, got, c.want)
		}
		// check returns at the first address that answers; the other dial may start after that
		check.probes.Wait()
		if dials := route.dialed(testV4a) + route.dialed(testV4b); dials != c.v4Dials {
			t.Errorf("%s: the IPv4 addresses were dialed %d times, want %d", c.name, dials, c.v4Dials)
		}
	}
}

// Elarni: every AAAA answer asks, so a check must not run per answer, and its result must not
// stand forever: a server gets IPv6, or a check went wrong once.
func TestIPv6CheckRunsOnceAndAgainOnlyWhenItsResultIsOld(t *testing.T) {
	var mu sync.Mutex
	ipv6, clock := "closes", time.Unix(1_000_000, 0)
	route := &fakeRoute{meets: func(address string, _ int) string {
		mu.Lock()
		defer mu.Unlock()
		if isIPv6(address) {
			return ipv6
		}
		return "answers"
	}}
	check := testIPv6Check(t, route)
	check.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	pass := func(d time.Duration) {
		mu.Lock()
		clock = clock.Add(d)
		mu.Unlock()
	}

	for range 20 {
		if dead(check, "apps") {
			t.Fatal("IPv6 is not reachable before any check ended")
		}
	}
	if state := settled(t, check, "apps"); state != ipv6Dead {
		t.Fatalf("IPv6 is %v, want %v", state, ipv6Dead)
	}
	// one check of a server without IPv6 tries each IPv6 address twice
	if dials := route.dialed(testV6a); dials != 2 {
		t.Fatalf("20 questions dialed an IPv6 address %d times, want the 2 of one check", dials)
	}

	pass(check.deadFor - time.Second)
	if !dead(check, "apps") || route.dialed(testV6a) != 2 {
		t.Fatal("a result that is not old was checked again, or forgotten")
	}

	mu.Lock()
	ipv6 = "answers"
	mu.Unlock()
	pass(time.Second)
	if !dead(check, "apps") {
		t.Fatal("the old result did not stand while it was checked again")
	}
	if state := settled(t, check, "apps"); state != ipv6Alive || dead(check, "apps") {
		t.Fatalf("after the server got IPv6 it is %v, want %v", state, ipv6Alive)
	}
	if dials := route.dialed(testV6a); dials != 3 {
		t.Fatalf("an IPv6 address was dialed %d times, want 3", dials)
	}

	// another inbound has its own route, and its own result
	if dead(check, "other") {
		t.Fatal("an inbound that was not checked has no IPv6")
	}
	if state := settled(t, check, "other"); state != ipv6Alive || route.dialed(testV6a) != 4 {
		t.Fatalf("the other inbound: IPv6 is %v after %d dials, want %v after 4", state, route.dialed(testV6a), ipv6Alive)
	}
}

// answersFor asks h for the records of qType for name as the inbound with tag
func answersFor(t *testing.T, h *Handler, qType dnsmessage.Type, name, tag string) []string {
	t.Helper()
	messages := make(capturedMessages, 1)
	timer := signal.CancelAfterInactivity(context.Background(), func() {}, time.Minute)
	h.handleIPQuery(7, qType, name, tag, messages, timer, 0)
	timer.SetTimeout(0)
	var msg dnsmessage.Message
	common.Must(msg.Unpack((<-messages).Bytes()))
	if msg.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("rcode %v", msg.RCode)
	}
	var ips []string
	for _, answer := range msg.Answers {
		switch body := answer.Body.(type) {
		case *dnsmessage.AAAAResource:
			ips = append(ips, net.IP(body.AAAA[:]).String())
		case *dnsmessage.AResource:
			ips = append(ips, net.IP(body.A[:]).String())
		}
	}
	return ips
}

func aaaaAnswers(t *testing.T, h *Handler, qType dnsmessage.Type, tag string) []string {
	t.Helper()
	return answersFor(t, h, qType, "example.test.", tag)
}

// Elarni: with a server that has no IPv6, apps were handed AAAA answers and sent it IPv6 it could
// only drop. The answers lose their public addresses, and nothing else changes: not the first
// answers, which do not wait for the check, not an address of the LAN or of FakeDNS, not A answers.
func TestAAAAAnswersHoldNoPublicAddressWhileTheServerHasNoIPv6(t *testing.T) {
	route := &fakeRoute{meets: withoutIPv6}
	public, lan, fake := net.ParseIP("2a00:1450:4001::7"), net.ParseIP("fd00::7"), net.ParseIP("fc00::1234")
	check := testIPv6Check(t, route)
	h := &Handler{client: staticClient{ips: []net.IP{public, lan, fake}}, ipv6: check}

	if got := aaaaAnswers(t, h, dnsmessage.TypeAAAA, "apps"); len(got) != 3 {
		t.Fatalf("the first answer, before the check ended, is %v, want all three addresses", got)
	}
	if state := settled(t, check, "apps"); state != ipv6Dead {
		t.Fatalf("IPv6 is %v, want %v", state, ipv6Dead)
	}
	if got := aaaaAnswers(t, h, dnsmessage.TypeAAAA, "apps"); len(got) != 2 || got[0] != lan.String() || got[1] != fake.String() {
		t.Fatalf("the answer is %v, want only %v and %v", got, lan, fake)
	}

	h.client = staticClient{ips: []net.IP{public}}
	if got := aaaaAnswers(t, h, dnsmessage.TypeAAAA, "apps"); len(got) != 0 {
		t.Fatalf("the answer is %v, want an empty one", got)
	}
	h.client = staticClient{ips: []net.IP{net.ParseIP("192.0.2.7")}}
	if got := aaaaAnswers(t, h, dnsmessage.TypeA, "apps"); len(got) != 1 {
		t.Fatalf("the A answer is %v, want its address", got)
	}
}

// Elarni: one result for the route of an inbound was applied to every name, also to names that
// routing sends elsewhere, so a name reached directly over a network with IPv6 lost its IPv6 for a
// proxy that has none. The result speaks for the outbound the services were routed to, and only
// addresses that would leave by that outbound, by their name or by the address itself (a LAN host
// with a global address), lose it.
func TestTheCheckSpeaksOnlyForTheOutboundItsServicesWereRoutedTo(t *testing.T) {
	public, lanHost := net.ParseIP("2a00:1450:4001::7"), net.ParseIP("2a00:1450:4001::50")
	route := &fakeRoute{meets: withoutIPv6, via: func(tag string, dest net.Destination, name string) string {
		if tag != "apps" {
			t.Errorf("routed as the traffic of %q", tag)
		}
		switch {
		case name == "":
			if dest.Port != 443 || dest.Network != net.Network_TCP {
				t.Errorf("a service routed as %v", dest)
			}
			return "proxy"
		case name == "direct.test" || dest.Address.IP().Equal(lanHost):
			return "direct"
		case name == "example.test":
			return "proxy"
		}
		t.Errorf("the name %q was asked", name)
		return ""
	}}
	check := testIPv6Check(t, route)
	h := &Handler{client: staticClient{ips: []net.IP{public, lanHost}}, ipv6: check}
	aaaaAnswers(t, h, dnsmessage.TypeAAAA, "apps")
	if state := settled(t, check, "apps"); state != ipv6Dead {
		t.Fatalf("IPv6 is %v, want %v", state, ipv6Dead)
	}
	if got := answersFor(t, h, dnsmessage.TypeAAAA, "Example.Test.", "apps"); !slices.Equal(got, []string{lanHost.String()}) {
		t.Fatalf("a proxied name: the answer is %v, want only the LAN host %v", got, lanHost)
	}
	if got := answersFor(t, h, dnsmessage.TypeAAAA, "direct.test.", "apps"); len(got) != 2 {
		t.Fatalf("a name routed direct: the answer is %v, want both addresses", got)
	}
}

// Elarni: IPv4 that works through one outbound does not tell that another one has no IPv6, so
// services routed to more than one outbound are not checked, and answers stay as they are.
func TestServicesRoutedToMoreThanOneOutboundAreNotChecked(t *testing.T) {
	route := &fakeRoute{meets: withoutIPv6, via: func(_ string, dest net.Destination, name string) string {
		if name == "" && dest.Address.Family().IsIPv4() {
			return "direct"
		}
		return "proxy"
	}}
	check := testIPv6Check(t, route)
	h := &Handler{client: staticClient{ips: []net.IP{net.ParseIP("2a00:1450:4001::7")}}, ipv6: check}
	aaaaAnswers(t, h, dnsmessage.TypeAAAA, "apps")
	if state := settled(t, check, "apps"); state != ipv6Unknown {
		t.Fatalf("IPv6 is %v, want %v", state, ipv6Unknown)
	}
	if got := aaaaAnswers(t, h, dnsmessage.TypeAAAA, "apps"); len(got) != 1 {
		t.Fatalf("the answer is %v, want its address", got)
	}
	route.mu.Lock()
	defer route.mu.Unlock()
	if len(route.dials) != 0 {
		t.Fatalf("the services were dialed %v", route.dials)
	}
}

// hostsClient answers the names in hosts from them, as static hosts do
type hostsClient struct {
	staticClient
	hosts map[string][]net.IP
}

func (c hostsClient) LookupIP(domain string, option dns.IPOption) ([]net.IP, uint32, error) {
	if ips, ok := c.hosts[strings.TrimSuffix(domain, ".")]; ok {
		return ips, 10, nil
	}
	return c.staticClient.LookupIP(domain, option)
}

func (c hostsClient) LookupHosts(domain string) []net.IP {
	return c.hosts[strings.TrimSuffix(domain, ".")]
}

// Elarni: an IPv6 address in the static hosts, the user's or the provider's, was set on purpose,
// and stays in the answer while the server has no IPv6.
func TestStaticHostsKeepTheirIPv6(t *testing.T) {
	pinned := net.ParseIP("2a00:1450:4001::8")
	check := testIPv6Check(t, &fakeRoute{meets: withoutIPv6})
	h := &Handler{client: hostsClient{staticClient{ips: []net.IP{net.ParseIP("2a00:1450:4001::7")}}, map[string][]net.IP{"pinned.test": {pinned}}}, ipv6: check}
	aaaaAnswers(t, h, dnsmessage.TypeAAAA, "apps")
	if state := settled(t, check, "apps"); state != ipv6Dead {
		t.Fatalf("IPv6 is %v, want %v", state, ipv6Dead)
	}
	if got := aaaaAnswers(t, h, dnsmessage.TypeAAAA, "apps"); len(got) != 0 {
		t.Fatalf("a resolved name: the answer is %v, want an empty one", got)
	}
	if got := answersFor(t, h, dnsmessage.TypeAAAA, "pinned.test.", "apps"); !slices.Equal(got, []string{pinned.String()}) {
		t.Fatalf("a name of the hosts: the answer is %v, want %v", got, pinned)
	}
}

// Elarni: a result held for up to 10 min after the device joined a network that reaches IPv6 (or
// lost one). A new path forgets every result, also that of a check that was running on the old one.
func TestANewSocketPathForgetsTheResults(t *testing.T) {
	route := &fakeRoute{meets: withoutIPv6}
	check := testIPv6Check(t, route)
	dead(check, "apps")
	if state := settled(t, check, "apps"); state != ipv6Dead {
		t.Fatalf("IPv6 is %v, want %v", state, ipv6Dead)
	}
	internet.SocketPathChanged()
	if dead(check, "apps") {
		t.Fatal("the result from the old path still stands")
	}
	if state := settled(t, check, "apps"); state != ipv6Dead || route.dialed(testV6a) != 4 {
		t.Fatalf("on the new path IPv6 is %v after %d dials, want %v after 4", state, route.dialed(testV6a), ipv6Dead)
	}

	// a check that started on the old path and ends on the new one leaves no result
	r := &ipv6Route{running: true}
	check.mu.Lock()
	check.routes["late"] = r
	check.mu.Unlock()
	internet.SocketPathChanged()
	check.run("late", r)
	check.mu.Lock()
	_, kept := check.routes["late"]
	check.mu.Unlock()
	if r.state != ipv6Dead || kept {
		t.Fatalf("the late check found %v, and its result stands: %t", r.state, kept)
	}
}

// Elarni: the check ran on a context of its own, so after the core closed it went on dialing
// through it for up to three tries of 5 s. Closing the handler ends the try that runs, and no check
// starts after it.
func TestTheCheckEndsWithItsHandler(t *testing.T) {
	route := &fakeRoute{meets: func(address string, _ int) string {
		if isIPv6(address) {
			return "silent"
		}
		return "answers"
	}}
	check := testIPv6Check(t, route)
	check.timeout = time.Minute // only the handler's end stops the IPv6 tries
	h := &Handler{client: staticClient{ips: []net.IP{net.ParseIP("2a00:1450:4001::7")}}, ipv6: check}
	aaaaAnswers(t, h, dnsmessage.TypeAAAA, "apps")
	for deadline := time.Now().Add(10 * time.Second); route.dialed(testV6a) == 0 || route.dialed(testV6b) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the check did not dial")
		}
	}
	common.Must(h.Close())
	if state := settled(t, check, "apps"); state != ipv6Unknown {
		t.Fatalf("IPv6 is %v, want %v", state, ipv6Unknown)
	}
	check.now = func() time.Time { return time.Now().Add(time.Hour) } // every result is old
	aaaaAnswers(t, h, dnsmessage.TypeAAAA, "apps")
	check.mu.Lock()
	running := check.routes["apps"].running
	check.mu.Unlock()
	if dials := route.dialed(testV4a) + route.dialed(testV4b); dials != 0 || running || route.dialed(testV6a) != 1 {
		t.Fatalf("after the handler closed: %d IPv4 dials, %d IPv6 dials, a check running %t", dials, route.dialed(testV6a), running)
	}
}

// Elarni: a lookup that brings no public IPv6, every one under an IPv4 preference and every name
// of the LAN or of a test network, has nothing to lose and must not send a check to the Internet.
func TestAnswersWithoutPublicIPv6StartNoCheck(t *testing.T) {
	route := &fakeRoute{meets: func(string, int) string { return "answers" }}
	check := testIPv6Check(t, route)
	for _, client := range []staticClient{
		{err: dns.ErrEmptyResponse},
		{ips: []net.IP{net.ParseIP("fd00::7"), net.ParseIP("fe80::1"), net.ParseIP("::1"), net.ParseIP("64:ff9b::c000:207")}},
		{ips: []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("3fff:fff::1"), net.ParseIP("::ffff:192.0.2.7")}},
	} {
		aaaaAnswers(t, &Handler{client: client, ipv6: check}, dnsmessage.TypeAAAA, "apps")
	}
	aaaaAnswers(t, &Handler{client: staticClient{ips: []net.IP{net.ParseIP("192.0.2.7")}}, ipv6: check}, dnsmessage.TypeA, "apps")
	check.mu.Lock()
	defer check.mu.Unlock()
	if len(check.routes) != 0 || len(route.dials) != 0 {
		t.Fatalf("%d routes were checked with %d dials, want none", len(check.routes), len(route.dials))
	}
}
