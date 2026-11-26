package dns_test

import (
	"context"
	"fmt"
	gonet "net"
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

// aaaaResolver is a UDP name server on loopback that answers AAAA queries for the names in
// records with their address, and every other query without records. It returns its port.
func aaaaResolver(t *testing.T, records map[string]string) int {
	t.Helper()
	conn, err := gonet.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFrom(b)
			if err != nil {
				return
			}
			var msg dnsmessage.Message
			if msg.Unpack(b[:n]) != nil || len(msg.Questions) != 1 {
				continue
			}
			q := msg.Questions[0]
			msg.Response, msg.RecursionAvailable, msg.Additionals = true, true, nil
			if address, ok := records[q.Name.String()]; ok && q.Type == dnsmessage.TypeAAAA {
				msg.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AAAAResource{AAAA: [16]byte(net.ParseIP(address).To16())},
				}}
			}
			if packed, err := msg.Pack(); err == nil {
				conn.WriteTo(packed, from)
			}
		}
	}()
	return conn.LocalAddr().(*gonet.UDPAddr).Port
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := gonet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*gonet.TCPAddr).Port
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

// Elarni: the whole path in a core built from JSON. The socks inbound "server-in" stands for a
// proxy server without IPv6: it ends its connections to the IPv6 service, and the IPv4 one
// answers. The inbound "no-ipv6" sends its traffic to that server, but a domain rule sends
// direct.example.test direct; "via-provider" gets there through a loopback whose inbound routes as
// "no-ipv6" does, as a provider profile's rules do in the compiled configs. Their apps get the
// AAAA record of a proxied name until the check has ended and an empty answer from then on, while
// the direct name and a name of the static hosts keep theirs. The inbound "with-ipv6" reaches both
// services directly and keeps the record, which also shows that the check is routed as the
// traffic of the inbound that asked, and that each inbound has its own result. The inbound
// "unasked" has no IPv6 either, but its DNS outbound was not told to check: its answers stay as
// they were.
func TestAAAAAnswersOfACoreFollowTheRouteOfEachName(t *testing.T) {
	v6, v4 := tlsService(t), tlsService(t)
	server := freePort(t)
	resolver := aaaaResolver(t, map[string]string{"dual.example.test.": "2a00:1450:4001::7", "direct.example.test.": "2a00:1450:4001::9"})

	config, err := serial.DecodeJSONConfig(strings.NewReader(fmt.Sprintf(`{
		"log": {"loglevel": "warning"},
		"dns": {"servers": [{"address": "127.0.0.1", "port": %d}], "hosts": {"pinned.example.test": ["2a00:1450:4001::8"]}},
		"inbounds": [{"tag": "server-in", "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"auth": "noauth"}}],
		"routing": {"rules": [
			{"type": "field", "inboundTag": ["no-ipv6", "with-ipv6", "via-provider"], "port": 53, "outboundTag": "dns-out"},
			{"type": "field", "inboundTag": ["unasked"], "port": 53, "outboundTag": "dns-unasked"},
			{"type": "field", "inboundTag": ["server-in"], "port": %d, "outboundTag": "block"},
			{"type": "field", "inboundTag": ["server-in"], "outboundTag": "direct"},
			{"type": "field", "inboundTag": ["no-ipv6", "provider-in"], "domain": ["full:direct.example.test"], "outboundTag": "direct"},
			{"type": "field", "inboundTag": ["no-ipv6", "provider-in", "unasked"], "outboundTag": "server"},
			{"type": "field", "inboundTag": ["with-ipv6"], "outboundTag": "direct"},
			{"type": "field", "inboundTag": ["via-provider"], "outboundTag": "provider"}
		]},
		"outbounds": [
			{"tag": "direct", "protocol": "freedom"},
			{"tag": "block", "protocol": "blackhole"},
			{"tag": "server", "protocol": "socks", "settings": {"servers": [{"address": "127.0.0.1", "port": %d}]}},
			{"tag": "provider", "protocol": "loopback", "settings": {"inboundTag": "provider-in"}},
			{"tag": "dns-out", "protocol": "dns", "settings": {"checkIPv6": true}},
			{"tag": "dns-unasked", "protocol": "dns"}
		]
	}`, resolver, server, v6.Port, server)))
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
	handler := instance.GetFeature(outbound.ManagerType()).(outbound.Manager).GetHandler("dns-out").(*proxyman_outbound.Handler).GetOutbound().(*dns_proxy.Handler)
	handler.SetIPv6Services(v6, v4, 2*time.Second)
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			instance.Close()
		}
	})

	for _, tag := range []string{"no-ipv6", "via-provider", "with-ipv6"} {
		if n := lookup(t, instance, tag, "dual.example.test."); n != 1 {
			t.Fatalf("%s: the first answer, before the check ended, has %d records, want 1", tag, n)
		}
	}
	for _, c := range []struct {
		tag, state string
		records    map[string]int
	}{
		{"no-ipv6", "not reachable", map[string]int{"dual.example.test.": 0, "direct.example.test.": 1, "pinned.example.test.": 1}},
		{"via-provider", "not reachable", map[string]int{"dual.example.test.": 0, "direct.example.test.": 1}},
		{"with-ipv6", "reachable", map[string]int{"dual.example.test.": 1}},
	} {
		for deadline := time.Now().Add(20 * time.Second); handler.IPv6State(c.tag) != c.state; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("public IPv6 for %s is %q, want %q", c.tag, handler.IPv6State(c.tag), c.state)
			}
		}
		for name, records := range c.records {
			if n := lookup(t, instance, c.tag, name); n != records {
				t.Errorf("after the check, the answer for %s to %s has %d records, want %d", name, c.tag, n, records)
			}
		}
	}
	if n := lookup(t, instance, "unasked", "dual.example.test."); n != 1 {
		t.Errorf("without checkIPv6 the answer has %d records, want 1", n)
	}

	closed = true
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	if !handler.IPv6CheckEnded() {
		t.Error("the IPv6 check outlives its core")
	}
}
