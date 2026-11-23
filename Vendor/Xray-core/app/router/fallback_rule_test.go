package router

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/geodata"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/dns"
	routingsession "github.com/xtls/xray-core/features/routing/session"
)

type countingDNS struct {
	answers map[string]string
	lookups int
}

func (*countingDNS) Type() interface{} { return dns.ClientType() }
func (*countingDNS) Start() error      { return nil }
func (*countingDNS) Close() error      { return nil }
func (d *countingDNS) LookupIP(domain string, _ dns.IPOption) ([]net.IP, uint32, error) {
	d.lookups++
	if answer, found := d.answers[domain]; found {
		return []net.IP{net.ParseIP(answer)}, 60, nil
	}
	return nil, 0, dns.ErrEmptyResponse
}

// A fallback rule stands for "no rule matched": with IPIfNonMatch the rules ahead of it take their second
// pass, with resolved addresses, before it applies.
func TestFallbackRuleWaitsForTheSecondPass(t *testing.T) {
	inside, err := geodata.ParseIPRules([]string{"10.0.0.0/8"})
	common.Must(err)
	exact, err := geodata.ParseDomainRules([]string{"full:app.inside.test"}, geodata.Domain_Substr)
	common.Must(err)
	all := []net.Network{net.Network_TCP, net.Network_UDP}
	rules := func(fallback bool, after ...*RoutingRule) []*RoutingRule {
		return append([]*RoutingRule{
			{TargetTag: &RoutingRule_Tag{Tag: "direct"}, Ip: inside},
			{TargetTag: &RoutingRule_Tag{Tag: "proxy"}, Domain: exact},
			{TargetTag: &RoutingRule_Tag{Tag: "block"}, Networks: all, Fallback: fallback},
		}, after...)
	}
	later := &RoutingRule{TargetTag: &RoutingRule_Tag{Tag: "later"}, Networks: all}
	domain := func(name string) *session.Outbound {
		return &session.Outbound{Target: net.TCPDestination(net.DomainAddress(name), 443)}
	}
	address := func(ip string) *session.Outbound {
		return &session.Outbound{Target: net.TCPDestination(net.ParseAddress(ip), 443)}
	}
	for _, test := range []struct {
		name     string
		strategy Config_DomainStrategy
		rules    []*RoutingRule
		outbound *session.Outbound
		skipDNS  bool
		tag      string
		lookups  int
	}{
		{"a domain rule after an IP rule wins for a name that resolves into the range", Config_IpIfNonMatch, rules(true), domain("app.inside.test"), false, "proxy", 0},
		{"a name no domain rule matches reaches the IP rule", Config_IpIfNonMatch, rules(true), domain("other.inside.test"), false, "direct", 1},
		{"a name no rule matches takes the fallback", Config_IpIfNonMatch, rules(true), domain("outside.test"), false, "block", 1},
		{"an address is routed in one pass", Config_IpIfNonMatch, rules(true), address("10.1.2.3"), false, "direct", 0},
		{"an address outside the range takes the fallback without a lookup", Config_IpIfNonMatch, rules(true), address("203.0.113.9"), false, "block", 0},
		{"rules after a fallback that matched are not read", Config_IpIfNonMatch, rules(true, later), domain("outside.test"), false, "block", 1},
		{"a lookup the DNS module forbids leaves the fallback", Config_IpIfNonMatch, rules(true), domain("other.inside.test"), true, "block", 0},
		{"a catch-all that is not a fallback ends the first pass, as upstream", Config_IpIfNonMatch, rules(false), domain("other.inside.test"), false, "block", 0},
		{"AsIs never resolves", Config_AsIs, rules(true), domain("other.inside.test"), false, "block", 0},
		{"IPOnDemand resolves at the first IP rule", Config_IpOnDemand, rules(true), domain("app.inside.test"), false, "direct", 1},
	} {
		resolver := &countingDNS{answers: map[string]string{"app.inside.test": "10.1.2.3", "other.inside.test": "10.4.5.6", "outside.test": "203.0.113.9"}}
		r := new(Router)
		common.Must(r.Init(context.Background(), &Config{DomainStrategy: test.strategy, Rule: test.rules}, resolver, nil, nil))
		route, err := r.PickRoute(&routingsession.Context{Outbound: test.outbound, Content: &session.Content{SkipDNSResolve: test.skipDNS}})
		if err != nil {
			t.Errorf("%s: %v", test.name, err)
			continue
		}
		if tag := route.GetOutboundTag(); tag != test.tag || resolver.lookups != test.lookups {
			t.Errorf("%s: outbound %s after %d lookups, want %s after %d", test.name, tag, resolver.lookups, test.tag, test.lookups)
		}
	}
}

// A connection from the TUN has the dialed address and a sniffed name for routing only. With
// IPIfNonMatch the name is read first and the dialed address second; the name is never looked up, so
// the addresses of a decoy name cannot pick the route.
func TestSniffedNameIsRoutedBeforeTheDialedAddress(t *testing.T) {
	listed, err := geodata.ParseIPRules([]string{"198.51.100.0/24", "192.168.0.0/16"})
	common.Must(err)
	home, err := geodata.ParseIPRules([]string{"203.0.113.0/24"})
	common.Must(err)
	site, err := geodata.ParseDomainRules([]string{"domain:site.test"}, geodata.Domain_Substr)
	common.Must(err)
	all := []net.Network{net.Network_TCP, net.Network_UDP}
	rules := []*RoutingRule{
		{TargetTag: &RoutingRule_Tag{Tag: "proxy"}, Ip: listed},
		{TargetTag: &RoutingRule_Tag{Tag: "direct"}, Domain: site},
		{TargetTag: &RoutingRule_Tag{Tag: "direct"}, Ip: home},
		{TargetTag: &RoutingRule_Tag{Tag: "block"}, Networks: all, Fallback: true},
	}
	// A rule set's own catch-all is no fallback rule, and a rule may read neither a name nor an
	// address: neither hides the IP rules ahead of it from a named connection.
	provider := []*RoutingRule{
		{TargetTag: &RoutingRule_Tag{Tag: "proxy"}, Ip: listed},
		{TargetTag: &RoutingRule_Tag{Tag: "udp"}, Networks: []net.Network{net.Network_UDP}},
		{TargetTag: &RoutingRule_Tag{Tag: "direct"}, Domain: site},
		{TargetTag: &RoutingRule_Tag{Tag: "block"}, Networks: all},
		{TargetTag: &RoutingRule_Tag{Tag: "direct"}, Ip: home},
	}
	quic := func(ip, name string) *session.Outbound {
		return &session.Outbound{Target: net.UDPDestination(net.ParseAddress(ip), 443), RouteTarget: net.UDPDestination(net.DomainAddress(name), 443)}
	}
	sniffed := func(ip, name string) *session.Outbound {
		outbound := &session.Outbound{Target: net.TCPDestination(net.ParseAddress(ip), 443)}
		if name != "" {
			outbound.RouteTarget = net.TCPDestination(net.DomainAddress(name), 443)
		}
		return outbound
	}
	for _, test := range []struct {
		name     string
		strategy Config_DomainStrategy
		rules    []*RoutingRule
		outbound *session.Outbound
		tag      string
	}{
		{"a later domain rule wins over an earlier IP rule", Config_IpIfNonMatch, rules, sniffed("198.51.100.7", "www.site.test"), "direct"},
		{"an unlisted name is routed by the dialed address", Config_IpIfNonMatch, rules, sniffed("198.51.100.7", "other.test"), "proxy"},
		{"the addresses of a decoy name are not read", Config_IpIfNonMatch, rules, sniffed("192.0.2.9", "decoy.test"), "block"},
		{"no name: one pass by address", Config_IpIfNonMatch, rules, sniffed("198.51.100.7", ""), "proxy"},
		{"a private address keeps the written order", Config_IpIfNonMatch, rules, sniffed("192.168.1.5", "www.site.test"), "proxy"},
		{"AsIs keeps the written order", Config_AsIs, rules, sniffed("198.51.100.7", "www.site.test"), "proxy"},
		{"IPOnDemand keeps the written order", Config_IpOnDemand, rules, sniffed("198.51.100.7", "www.site.test"), "proxy"},
		{"a catch-all that is no fallback leaves the IP rule ahead of it", Config_IpIfNonMatch, provider, sniffed("198.51.100.7", "other.test"), "proxy"},
		{"a catch-all that is no fallback hides the IP rule after it", Config_IpIfNonMatch, provider, sniffed("203.0.113.7", "other.test"), "block"},
		{"a later domain rule still wins over an earlier IP rule", Config_IpIfNonMatch, provider, sniffed("198.51.100.7", "www.site.test"), "direct"},
		{"a rule that reads no name keeps its place ahead of a domain rule", Config_IpIfNonMatch, provider, quic("192.0.2.9", "www.site.test"), "udp"},
		{"an IP rule ahead of a rule that reads no name is asked first", Config_IpIfNonMatch, provider, quic("198.51.100.7", "www.site.test"), "proxy"},
	} {
		// decoy.test resolves into the direct range: a lookup of it would send the flow direct.
		resolver := &countingDNS{answers: map[string]string{"decoy.test": "203.0.113.5", "other.test": "203.0.113.6"}}
		r := new(Router)
		common.Must(r.Init(context.Background(), &Config{DomainStrategy: test.strategy, Rule: test.rules}, resolver, nil, nil))
		route, err := r.PickRoute(&routingsession.Context{Outbound: test.outbound, Content: &session.Content{}})
		if err != nil {
			t.Errorf("%s: %v", test.name, err)
			continue
		}
		if tag := route.GetOutboundTag(); tag != test.tag || resolver.lookups != 0 {
			t.Errorf("%s: outbound %s after %d lookups, want %s after none", test.name, tag, resolver.lookups, test.tag)
		}
		if ips := route.GetTargetIPs(); len(ips) != 1 {
			t.Errorf("%s: the route lost the dialed address: %v", test.name, ips)
		}
	}
}
