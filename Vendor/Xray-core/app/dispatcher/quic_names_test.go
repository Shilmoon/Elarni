package dispatcher

import (
	"context"
	"crypto/tls"
	gonet "net"
	"testing"
	"time"

	"github.com/apernet/quic-go"
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

// quicInitial returns the first datagram a QUIC client sends for the server name.
func quicInitial(t *testing.T, name string) []byte {
	server, err := gonet.ListenPacket("udp", "127.0.0.1:0")
	common.Must(err)
	defer server.Close()
	client, err := gonet.ListenPacket("udp", "127.0.0.1:0")
	common.Must(err)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go quic.Dial(ctx, client, server.LocalAddr(), &tls.Config{ServerName: name, InsecureSkipVerify: true, NextProtos: []string{"h3"}, CurvePreferences: []tls.CurveID{tls.X25519}}, nil)
	packet := make([]byte, 2048)
	server.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := server.ReadFrom(packet)
	if err != nil {
		t.Fatal(err)
	}
	return packet[:n]
}

// Elarni: a QUIC connection whose UDP flow idled out goes on with a short-header packet. The new
// flow was routed by address alone, so a connection proxied by its name resumed on another outbound.
func TestAResumedQUICFlowKeepsItsName(t *testing.T) {
	instance, err := core.New(&core.Config{})
	common.Must(err)
	listed, err := geodata.ParseDomainRules([]string{"full:video.test"}, geodata.Domain_Substr)
	common.Must(err)
	var picked string
	handlers := taggedHandlers{picked: &picked}
	r := new(router.Router)
	common.Must(r.Init(context.Background(), &router.Config{
		Rule: []*router.RoutingRule{
			{TargetTag: &router.RoutingRule_Tag{Tag: "proxy"}, Domain: listed},
			{TargetTag: &router.RoutingRule_Tag{Tag: "direct"}, Networks: []net.Network{net.Network_UDP}},
		},
	}, &countingDNS{}, handlers, nil))
	d := &DefaultDispatcher{ohm: handlers, router: r, fdns: fakePool{}}

	initial := quicInitial(t, "video.test")
	short := []byte{0x40, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23}
	app := net.UDPDestination(net.ParseAddress("172.19.0.1"), 50000)
	other := net.UDPDestination(net.ParseAddress("172.19.0.1"), 50001)
	site := net.UDPDestination(net.ParseAddress("203.0.113.10"), 443)
	for _, c := range []struct {
		name    string
		source  net.Destination
		dest    net.Destination
		payload []byte
		sniff   []string
		want    string
	}{
		{"short header before any Initial", app, site, short, []string{"http", "tls", "quic"}, "direct"},
		{"Initial", app, site, initial, []string{"http", "tls", "quic"}, "proxy"},
		{"the same sockets resume", app, site, short, []string{"http", "tls", "quic"}, "proxy"},
		{"another socket of the app", other, site, short, []string{"http", "tls", "quic"}, "direct"},
		{"another server", app, net.UDPDestination(net.ParseAddress("203.0.113.11"), 443), short, []string{"http", "tls", "quic"}, "direct"},
		{"QUIC names not asked for", app, site, short, []string{"http", "tls"}, "direct"},
	} {
		picked = ""
		outbound := &session.Outbound{}
		ctx := context.WithValue(context.Background(), core.XrayKey(1), instance)
		ctx = session.ContextWithInbound(ctx, &session.Inbound{Tag: "tun", Source: c.source})
		ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{outbound})
		ctx = session.ContextWithContent(ctx, &session.Content{SniffingRequest: session.SniffingRequest{
			Enabled: true, RouteOnly: true, OverrideDestinationForProtocol: c.sniff,
		}})
		reader, writer := pipe.New(pipe.WithoutSizeLimit())
		common.Must(writer.WriteMultiBuffer(buf.MergeBytes(nil, c.payload)))
		_, downlink := pipe.New(pipe.WithoutSizeLimit())
		common.Must(d.DispatchLink(ctx, c.dest, &transport.Link{Reader: reader, Writer: downlink}))
		if picked != c.want || outbound.Target != c.dest {
			t.Errorf("%s: routed to %q with target %v, want %q with %v", c.name, picked, outbound.Target, c.want, c.dest)
		}
	}

	d.quic.names[quicNameKey{app, site}] = quicName{"video.test", time.Now().Add(-time.Second)}
	if name := d.quic.recall(app, site); name != "" || len(d.quic.names) != 0 {
		t.Errorf("an expired name was recalled: %q, %d kept", name, len(d.quic.names))
	}
	for port := 0; port < quicNameLimit+10; port++ {
		d.quic.remember(net.UDPDestination(net.ParseAddress("172.19.0.1"), net.Port(port+1)), site, "video.test")
	}
	if len(d.quic.names) > quicNameLimit {
		t.Errorf("%d names kept, limit %d", len(d.quic.names), quicNameLimit)
	}
}
