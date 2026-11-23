package dispatcher

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/geodata"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

// Elarni: in proxy mode the client names its target (CONNECT host, SOCKS domain). The name in the
// first bytes replaced it for routing, so a request to one site was routed by another site's rule.
func TestANamedTargetIsNotRoutedByTheSniffedName(t *testing.T) {
	instance, err := core.New(&core.Config{})
	common.Must(err)
	listed, err := geodata.ParseDomainRules([]string{"full:listed.test"}, geodata.Domain_Substr)
	common.Must(err)
	var picked string
	handlers := taggedHandlers{picked: &picked}
	r := new(router.Router)
	common.Must(r.Init(context.Background(), &router.Config{
		Rule: []*router.RoutingRule{
			{TargetTag: &router.RoutingRule_Tag{Tag: "direct"}, Domain: listed},
			{TargetTag: &router.RoutingRule_Tag{Tag: "proxy"}, Networks: []net.Network{net.Network_TCP}},
		},
	}, &countingDNS{}, handlers, nil))
	d := &DefaultDispatcher{ohm: handlers, router: r, fdns: fakePool{}}

	for _, c := range []struct {
		name string
		dest net.Destination
		host string
		want string
	}{
		{"named target, listed name inside", net.TCPDestination(net.DomainAddress("other.test"), 80), "listed.test", "proxy"},
		{"listed named target, other name inside", net.TCPDestination(net.DomainAddress("listed.test"), 80), "other.test", "direct"},
		{"address target, listed name inside", net.TCPDestination(net.ParseAddress("203.0.113.10"), 80), "listed.test", "direct"},
	} {
		picked = ""
		outbound := &session.Outbound{}
		ctx := context.WithValue(context.Background(), core.XrayKey(1), instance)
		ctx = session.ContextWithInbound(ctx, &session.Inbound{Tag: "http"})
		ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{outbound})
		ctx = session.ContextWithContent(ctx, &session.Content{SniffingRequest: session.SniffingRequest{
			Enabled: true, RouteOnly: true, OverrideDestinationForProtocol: []string{"http", "tls", "quic"},
		}})
		reader, writer := pipe.New(pipe.WithoutSizeLimit())
		common.Must(writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("GET / HTTP/1.1\r\nHost: "+c.host+"\r\n\r\n"))))
		_, downlink := pipe.New(pipe.WithoutSizeLimit())
		common.Must(d.DispatchLink(ctx, c.dest, &transport.Link{Reader: reader, Writer: downlink}))
		if picked != c.want || outbound.Target != c.dest {
			t.Errorf("%s: routed to %q with target %v, want %q with %v", c.name, picked, outbound.Target, c.want, c.dest)
		}
	}
}
