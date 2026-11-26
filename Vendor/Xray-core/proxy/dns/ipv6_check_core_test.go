package dns_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	proxyman_outbound "github.com/xtls/xray-core/app/proxyman/outbound"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
	dns_proxy "github.com/xtls/xray-core/proxy/dns"
	"golang.org/x/net/dns/dnsmessage"
)

// tlsService is a TLS server on loopback that stands for a public service
func tlsService(t *testing.T) net.Destination {
	t.Helper()
	server := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	dest, err := net.ParseDestination("tcp:" + server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

// lookup asks the core for the AAAA records of name as an app behind the inbound with this tag
func lookup(t *testing.T, instance *core.Instance, tag, name string) int {
	t.Helper()
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{Tag: tag})
	conn, err := core.Dial(ctx, instance, net.UDPDestination(net.ParseAddress("192.0.2.53"), 53))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	query := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 7, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName(name), Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET}},
	}
	packed, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(packed); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	b := make([]byte, 2048)
	n, err := conn.Read(b)
	if err != nil {
		t.Fatal("no answer to the AAAA query from ", tag, ": ", err)
	}
	var answer dnsmessage.Message
	if err := answer.Unpack(b[:n]); err != nil {
		t.Fatal(err)
	}
	if answer.RCode != dnsmessage.RCodeSuccess {
		t.Fatal("rcode ", answer.RCode)
	}
	return len(answer.Answers)
}

// Elarni: the whole path in a core built from JSON. The inbound "no-ipv6" stands for a tunnel to a
// server that has no IPv6: a rule ends its connections to the IPv6 service, the IPv4 one answers.
// Its apps get the AAAA record until the check has ended, and an empty answer from then on. The
// inbound "with-ipv6" reaches both services and keeps the record, which also shows that the check
// is routed as the traffic of the inbound that asked, and that each inbound has its own result.
// The inbound "unasked" has no IPv6 either, but its DNS outbound was not told to check: its
// answers stay as they were.
func TestAAAAAnswersOfACoreFollowTheRouteOfTheInboundThatAsks(t *testing.T) {
	v6, v4 := tlsService(t), tlsService(t)
	t.Cleanup(dns_proxy.SetIPv6Services(v6, v4))

	config, err := serial.DecodeJSONConfig(strings.NewReader(fmt.Sprintf(`{
		"log": {"loglevel": "warning"},
		"dns": {"hosts": {"dual.example.test": ["2a00:1450:4001::7", "192.0.2.7"]}},
		"routing": {"rules": [
			{"type": "field", "inboundTag": ["no-ipv6", "with-ipv6"], "port": 53, "outboundTag": "dns-out"},
			{"type": "field", "inboundTag": ["unasked"], "port": 53, "outboundTag": "dns-unasked"},
			{"type": "field", "inboundTag": ["no-ipv6", "unasked"], "port": %d, "outboundTag": "block"}
		]},
		"outbounds": [
			{"tag": "direct", "protocol": "freedom"},
			{"tag": "block", "protocol": "blackhole"},
			{"tag": "dns-out", "protocol": "dns", "settings": {"checkIPv6": true}},
			{"tag": "dns-unasked", "protocol": "dns"}
		]
	}`, v6.Port)))
	if err != nil {
		t.Fatal(err)
	}
	built, err := config.Build()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := core.New(built)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { instance.Close() })
	handler := instance.GetFeature(outbound.ManagerType()).(outbound.Manager).GetHandler("dns-out").(*proxyman_outbound.Handler).GetOutbound().(*dns_proxy.Handler)

	if n := lookup(t, instance, "no-ipv6", "dual.example.test."); n != 1 {
		t.Fatalf("the first answer, before the check ended, has %d records, want 1", n)
	}
	if n := lookup(t, instance, "with-ipv6", "dual.example.test."); n != 1 {
		t.Fatalf("the first answer with IPv6 has %d records, want 1", n)
	}
	for _, c := range []struct {
		tag, state string
		records    int
	}{{"no-ipv6", "not reachable", 0}, {"with-ipv6", "reachable", 1}} {
		for deadline := time.Now().Add(20 * time.Second); handler.IPv6State(c.tag) != c.state; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("public IPv6 for %s is %q, want %q", c.tag, handler.IPv6State(c.tag), c.state)
			}
		}
		if n := lookup(t, instance, c.tag, "dual.example.test."); n != c.records {
			t.Errorf("after the check, the answer for %s has %d records, want %d", c.tag, n, c.records)
		}
	}
	if n := lookup(t, instance, "unasked", "dual.example.test."); n != 1 {
		t.Errorf("without checkIPv6 the answer has %d records, want 1", n)
	}
}
