package dns

import (
	go_errors "errors"
	"slices"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/dns/fakedns"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/geodata"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/dns"
	"golang.org/x/net/dns/dnsmessage"
)

// reply answers query with rCode and those of addresses that fit the asked type.
func reply(query []byte, rCode dnsmessage.RCode, addresses ...string) []byte {
	var msg dnsmessage.Message
	if msg.Unpack(query) != nil || len(msg.Questions) != 1 {
		return nil
	}
	q := msg.Questions[0]
	msg.Response, msg.RCode = true, rCode
	for _, address := range addresses {
		ip := net.ParseIP(address)
		h := dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 60}
		if q.Type == dnsmessage.TypeA && ip.To4() != nil {
			msg.Answers = append(msg.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.AResource{A: [4]byte(ip.To4())}})
		} else if q.Type == dnsmessage.TypeAAAA && ip.To4() == nil {
			msg.Answers = append(msg.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.AAAAResource{AAAA: [16]byte(ip.To16())}})
		}
	}
	answer, err := msg.Pack()
	common.Must(err)
	return answer
}

// udpResolver is a UDP name server that answers each query with answer, or never when answer
// returns nil, and reports the type of every query that reaches it.
func udpResolver(timeout time.Duration, answer func(query []byte) []byte) (*Client, chan dnsmessage.Type) {
	asked := make(chan dnsmessage.Type, 16)
	flows := &udpFlows{ended: make(chan int, 16), answer: func(_ int, query []byte) []byte {
		var msg dnsmessage.Message
		if msg.Unpack(query) == nil && len(msg.Questions) == 1 {
			asked <- msg.Questions[0].Type
		}
		return answer(query)
	}}
	both := dns.IPOption{IPv4Enable: true, IPv6Enable: true}
	server := NewClassicNameServer(net.UDPDestination(net.ParseAddress("192.0.2.53"), 53), flows, true, false, 0, nil)
	return &Client{server: server, ipOption: &both, timeoutMs: timeout}, asked
}

func askedTypes(asked chan dnsmessage.Type) []dnsmessage.Type {
	var types []dnsmessage.Type
	for {
		select {
		case qType := <-asked:
			types = append(types, qType)
		default:
			return types
		}
	}
}

// Elarni: a resolver that answered an AAAA query without records was taken as a failure, so the
// lookup went on to the next resolver and, when that one did not answer (it often has no IPv6 route
// either), waited out its timeout before the app got the empty answer it already had (N2-47).
func TestAnEmptyAAAAAnswerEndsTheLookup(t *testing.T) {
	previous := udpResendAfter
	udpResendAfter = time.Hour
	t.Cleanup(func() { udpResendAfter = previous })
	ipv6Only := dns.IPOption{IPv6Enable: true, FakeEnable: true}
	ipv4Only := dns.IPOption{IPv4Enable: true, FakeEnable: true}
	both := dns.IPOption{IPv4Enable: true, IPv6Enable: true, FakeEnable: true}
	engine, err := fakedns.NewFakeDNSHolder() // an IPv4 pool only
	common.Must(err)
	notIn := common.Must2(geodata.IPReg.BuildIPMatcher(common.Must2(geodata.ParseIPRules([]string{"2001:db8:ffff::/48"}))))
	empty := func(query []byte) []byte { return reply(query, dnsmessage.RCodeSuccess) }
	for _, c := range []struct {
		name   string
		first  func() *Client
		second func(query []byte) []byte // nil: never answers
		option dns.IPOption
		want   []string // nil: an empty answer
		asked  []dnsmessage.Type
	}{
		{"AAAA answered empty", func() *Client { client, _ := udpResolver(time.Minute, empty); return client },
			nil, ipv6Only, nil, nil},
		{"A answered empty", func() *Client { client, _ := udpResolver(time.Minute, empty); return client },
			func(query []byte) []byte { return reply(query, dnsmessage.RCodeSuccess, "192.0.2.7") }, ipv4Only, []string{"192.0.2.7"}, []dnsmessage.Type{dnsmessage.TypeA}},
		{"both families answered empty", func() *Client { client, _ := udpResolver(time.Minute, empty); return client },
			func(query []byte) []byte { return reply(query, dnsmessage.RCodeSuccess, "192.0.2.7") }, both, []string{"192.0.2.7"}, []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA}},
		{"AAAA refused", func() *Client {
			client, _ := udpResolver(time.Minute, func(query []byte) []byte { return reply(query, dnsmessage.RCodeRefused) })
			return client
		}, func(query []byte) []byte { return reply(query, dnsmessage.RCodeSuccess, "2001:db8::7") }, ipv6Only, []string{"2001:db8::7"}, []dnsmessage.Type{dnsmessage.TypeAAAA}},
		{"AAAA timed out", func() *Client {
			client, _ := udpResolver(100*time.Millisecond, func([]byte) []byte { return nil })
			return client
		}, func(query []byte) []byte { return reply(query, dnsmessage.RCodeSuccess, "2001:db8::7") }, ipv6Only, []string{"2001:db8::7"}, []dnsmessage.Type{dnsmessage.TypeAAAA}},
		{"AAAA emptied by the expected addresses", func() *Client {
			client, _ := udpResolver(time.Minute, func(query []byte) []byte { return reply(query, dnsmessage.RCodeSuccess, "2001:db8::1") })
			client.expectedIPs = notIn
			return client
		}, func(query []byte) []byte { return reply(query, dnsmessage.RCodeSuccess, "2001:db8::7") }, ipv6Only, []string{"2001:db8::7"}, []dnsmessage.Type{dnsmessage.TypeAAAA}},
		// One half of a domestic/remote split: its empty answer is no answer for the other half.
		{"AAAA answered empty where expected addresses apply", func() *Client {
			client, _ := udpResolver(time.Minute, empty)
			client.expectedIPs = notIn
			return client
		}, func(query []byte) []byte { return reply(query, dnsmessage.RCodeSuccess, "2001:db8::7") }, ipv6Only, []string{"2001:db8::7"}, []dnsmessage.Type{dnsmessage.TypeAAAA}},
		{"AAAA answered empty where unexpected addresses apply", func() *Client {
			client, _ := udpResolver(time.Minute, empty)
			client.unexpectedIPs = notIn
			return client
		}, func(query []byte) []byte { return reply(query, dnsmessage.RCodeSuccess, "2001:db8::7") }, ipv6Only, []string{"2001:db8::7"}, []dnsmessage.Type{dnsmessage.TypeAAAA}},
		{"AAAA from FakeDNS without an IPv6 pool", func() *Client {
			both := dns.IPOption{IPv4Enable: true, IPv6Enable: true}
			return &Client{server: NewFakeDNSServer(engine), ipOption: &both, timeoutMs: time.Minute}
		}, func(query []byte) []byte { return reply(query, dnsmessage.RCodeSuccess, "2001:db8::7") }, ipv6Only, []string{"2001:db8::7"}, []dnsmessage.Type{dnsmessage.TypeAAAA}},
	} {
		t.Run(c.name, func(t *testing.T) {
			second, asked := udpResolver(time.Minute, func(query []byte) []byte {
				if c.second == nil {
					return nil
				}
				return c.second(query)
			})
			allowed := dns.IPOption{IPv4Enable: true, IPv6Enable: true}
			d := &DNS{ctx: udpTestContext(t), clients: []*Client{c.first(), second}, ipOption: &allowed, hosts: &StaticHosts{}}
			type result struct {
				ips []net.IP
				err error
			}
			done := make(chan result, 1)
			go func() {
				ips, _, err := d.LookupIP("example.test", c.option)
				done <- result{ips, err}
			}()
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("no answer within 5 s: the lookup waited for the next resolver")
			}
			var addresses []string
			for _, ip := range got.ips {
				addresses = append(addresses, ip.String())
			}
			if c.want == nil && !go_errors.Is(got.err, dns.ErrEmptyResponse) || c.want != nil && got.err != nil || !slices.Equal(addresses, c.want) {
				t.Fatalf("got %v, %v; want %v", addresses, got.err, c.want)
			}
			types := askedTypes(asked)
			slices.Sort(types)
			if !slices.Equal(types, c.asked) {
				t.Fatalf("the next resolver was asked %v, want %v", types, c.asked)
			}
		})
	}
}
