package dns

import (
	"context"
	"io"
	gonet "net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/signal"
	"github.com/xtls/xray-core/features/dns"
	"golang.org/x/net/dns/dnsmessage"
)

// fakeRoute stands for the route of an inbound: what a connection to each address meets there
type fakeRoute struct {
	// meets is asked with the address and how many times it has been dialed, this time included:
	// "answers", "closes" (the far end of a proxy could not connect), "silent" (packets are
	// dropped), or anything else for a dial that fails here
	meets func(address string, attempt int) string

	mu    sync.Mutex
	dials map[string]int
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

func testIPv6Check(route *fakeRoute) *ipv6Check {
	return &ipv6Check{
		dial:       route.dial,
		services:   []ipv6Service{{tcp443(testV6a), tcp443(testV4a)}, {tcp443(testV6b), tcp443(testV4b)}},
		timeout:    100 * time.Millisecond,
		unknownFor: 30 * time.Second,
		deadFor:    10 * time.Minute,
		aliveFor:   30 * time.Minute,
		now:        time.Now,
		routes:     make(map[string]*ipv6Route),
	}
}

func isIPv6(address string) bool { return address == testV6a || address == testV6b }

// settled waits for the check of tag that is running to end, and returns its result
func settled(t *testing.T, c *ipv6Check, tag string) ipv6State {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		c.mu.Lock()
		r := c.routes[tag]
		if r != nil && !r.running && !r.checked.IsZero() {
			state := r.state
			c.mu.Unlock()
			return state
		}
		c.mu.Unlock()
	}
	t.Fatal("the check did not end")
	return ipv6Unknown
}

// Elarni: only a server that carries IPv4 to the same services and no IPv6 has "no IPv6". One
// service that is down, a tunnel that is down and a proxy that was still connecting when IPv6 was
// tried must not take IPv6 answers from a server that has IPv6.
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
		{"a server that cannot connect over IPv6", func(address string, _ int) string {
			if isIPv6(address) {
				return "closes"
			}
			return "answers"
		}, ipv6Dead, 2},
		{"a server that drops IPv6 without a word", func(address string, _ int) string {
			if isIPv6(address) {
				return "silent"
			}
			return "answers"
		}, ipv6Dead, 2},
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
		if got := testIPv6Check(route).check("apps"); got != c.want {
			t.Errorf("%s: IPv6 is %v, want %v", c.name, got, c.want)
		}
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
	check := testIPv6Check(route)
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
		if check.dead("apps") {
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
	if !check.dead("apps") || route.dialed(testV6a) != 2 {
		t.Fatal("a result that is not old was checked again, or forgotten")
	}

	mu.Lock()
	ipv6 = "answers"
	mu.Unlock()
	pass(time.Second)
	if !check.dead("apps") {
		t.Fatal("the old result did not stand while it was checked again")
	}
	if state := settled(t, check, "apps"); state != ipv6Alive || check.dead("apps") {
		t.Fatalf("after the server got IPv6 it is %v, want %v", state, ipv6Alive)
	}
	if dials := route.dialed(testV6a); dials != 3 {
		t.Fatalf("an IPv6 address was dialed %d times, want 3", dials)
	}

	// another inbound has its own route, and its own result
	if check.dead("other") {
		t.Fatal("an inbound that was not checked has no IPv6")
	}
	if state := settled(t, check, "other"); state != ipv6Alive || route.dialed(testV6a) != 4 {
		t.Fatalf("the other inbound: IPv6 is %v after %d dials, want %v after 4", state, route.dialed(testV6a), ipv6Alive)
	}
}

func aaaaAnswers(t *testing.T, h *Handler, qType dnsmessage.Type, tag string) []string {
	t.Helper()
	messages := make(capturedMessages, 1)
	timer := signal.CancelAfterInactivity(context.Background(), func() {}, time.Minute)
	h.handleIPQuery(7, qType, "example.test.", tag, messages, timer)
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

// Elarni: with a server that has no IPv6, apps were handed AAAA answers and sent it IPv6 it could
// only drop. The answers lose their public addresses, and nothing else changes: not the first
// answers, which do not wait for the check, not an address of the LAN or of FakeDNS, not A answers.
func TestAAAAAnswersHoldNoPublicAddressWhileTheServerHasNoIPv6(t *testing.T) {
	route := &fakeRoute{meets: func(address string, _ int) string {
		if isIPv6(address) {
			return "closes"
		}
		return "answers"
	}}
	public, lan, fake := net.ParseIP("2a00:1450:4001::7"), net.ParseIP("fd00::7"), net.ParseIP("fc00::1234")
	check := testIPv6Check(route)
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

// Elarni: a lookup that brings no public IPv6, every one under an IPv4 preference and every name
// of the LAN or of a test network, has nothing to lose and must not send a check to the Internet.
func TestAnswersWithoutPublicIPv6StartNoCheck(t *testing.T) {
	route := &fakeRoute{meets: func(string, int) string { return "answers" }}
	check := testIPv6Check(route)
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
