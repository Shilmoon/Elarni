package share

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	dnsapp "github.com/xtls/xray-core/app/dns"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/routing"
	routingsession "github.com/xtls/xray-core/features/routing/session"
	"github.com/xtls/xray-core/infra/conf"
	"golang.org/x/net/dns/dnsmessage"
)

// Configs written by native/elarni-domain/src/router_fixtures.rs. Their dns.hosts
// answer every lookup the router makes, so these tests never leave this host.
// Embedded because check-android runs the test binary on a device, away from testdata/.
//
//go:embed testdata/compiled_routing.json
var compiledRoutingJSON []byte

func compiledRoutingFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	var fixtures map[string]map[string]any
	if err := json.Unmarshal(compiledRoutingJSON, &fixtures); err != nil {
		t.Fatal(err)
	}
	config := fixtures[name]
	if config == nil {
		t.Fatalf("missing fixture %s", name)
	}
	// Routes are picked and dispatched without opening the app listeners.
	config["inbounds"] = []any{}
	return config
}

func pickCompiledRoute(t *testing.T, router routing.Router, inbound string, target xnet.Address) string {
	t.Helper()
	route, err := router.PickRoute(&routingsession.Context{
		Inbound:  &session.Inbound{Tag: inbound},
		Outbound: &session.Outbound{Target: xnet.TCPDestination(target, 443)},
		Content:  &session.Content{},
	})
	if err != nil {
		t.Fatalf("%s %s: %v", inbound, target, err)
	}
	return route.GetOutboundTag()
}

func TestCompiledRoutingMatchesIPRulesForHostnames(t *testing.T) {
	instance, stop := startTrafficCore(t, compiledRoutingFixture(t, "outbound"))
	defer stop()
	router := instance.GetFeature(routing.RouterType()).(routing.Router)
	for _, fixture := range []struct {
		target xnet.Address
		want   string
	}{
		{xnet.DomainAddress("blocked.test"), "elarni-block"},
		{xnet.ParseAddress("198.51.100.20"), "elarni-direct"},
		{xnet.DomainAddress("direct-range.test"), "elarni-direct"},
		// "Resolve if no rule matches": the name's own rule decides, though an IP rule ahead of it
		// holds the address it resolves to.
		{xnet.DomainAddress("named-in-range.test"), "elarni-proxy"},
		{xnet.DomainAddress("outside.test"), "elarni-proxy"},
		{xnet.ParseAddress("203.0.113.10"), "elarni-proxy"},
	} {
		if got := pickCompiledRoute(t, router, "elarni-socks", fixture.target); got != fixture.want {
			t.Errorf("%s: got %s, want %s", fixture.target, got, fixture.want)
		}
	}
}

// Xray's JSON reader skips keys it does not know, so a config that loads proves nothing
// about the compiler's DNS keys; the built DNS app must carry them.
func TestCompiledDNSKeysReachTheDNSApp(t *testing.T) {
	raw, err := json.Marshal(compiledRoutingFixture(t, "outbound")["dns"])
	if err != nil {
		t.Fatal(err)
	}
	var config conf.DNSConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	built, err := config.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !built.ServeStale || built.ServeExpiredTTL != 3600 || built.QueryStrategy != dnsapp.QueryStrategy_USE_IP4 {
		t.Errorf("serveStale %v, serveExpiredTTL %d, queryStrategy %v", built.ServeStale, built.ServeExpiredTTL, built.QueryStrategy)
	}
	// Only the remote resolver gives up early.
	if len(built.NameServer) != 2 || built.NameServer[0].TimeoutMs != 2000 || built.NameServer[1].TimeoutMs != 0 {
		t.Errorf("name servers %v", built.NameServer)
	}
}

// FakeDNS answers every name except those whose address is the point: Windows compares the answer
// for dns.msftncsi.com and reports "No internet", and a LAN name with a fake address goes to the server.
func TestCompiledFakeDNSLeavesListedNamesToTheResolver(t *testing.T) {
	resolver, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	go func() {
		packet := make([]byte, 2048)
		for {
			n, peer, err := resolver.ReadFrom(packet)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if query.Unpack(packet[:n]) != nil || len(query.Questions) != 1 {
				continue
			}
			question := query.Questions[0]
			reply := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, RecursionAvailable: true}, Questions: query.Questions}
			if question.Name.String() == "dns.msftncsi.com." && question.Type == dnsmessage.TypeA {
				reply.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: question.Name, Type: question.Type, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: [4]byte{131, 107, 255, 255}},
				}}
			} else {
				reply.RCode = dnsmessage.RCodeNameError
			}
			if out, err := reply.Pack(); err == nil {
				resolver.WriteTo(out, peer)
			}
		}
	}()
	port := resolver.LocalAddr().(*net.UDPAddr).Port

	config := compiledRoutingFixture(t, "fake-dns")
	// The copy of the resolver that serves the listed names is the test's; the plain one stays
	// unreachable, so an answer can only come from FakeDNS or from the listed-names server.
	servers := config["dns"].(map[string]any)["servers"].([]any)
	listed := servers[len(servers)-1].(map[string]any)
	if listed["finalQuery"] != true || listed["address"] != servers[1] {
		t.Fatalf("the last server is not the resolver's copy for listed names: %v", listed)
	}
	listed["address"], listed["port"] = "127.0.0.1", port
	routing := config["routing"].(map[string]any)
	routing["rules"] = append([]any{map[string]any{"type": "field", "ip": []any{"127.0.0.1"}, "port": port, "outboundTag": "elarni-direct"}}, routing["rules"].([]any)...)

	instance, stop := startTrafficCore(t, config)
	defer stop()
	client := instance.GetFeature(dns.ClientType()).(dns.Client)
	option := dns.IPOption{IPv4Enable: true, FakeEnable: true}
	_, pool, _ := net.ParseCIDR("198.18.0.0/15")

	if ips, _, err := client.LookupIP("site.test", option); err != nil || len(ips) != 1 || !pool.Contains(ips[0]) {
		t.Errorf("site.test: %v, %v; want a FakeDNS address", ips, err)
	}
	if ips, _, err := client.LookupIP("dns.msftncsi.com", option); err != nil || len(ips) != 1 || !ips[0].Equal(net.IPv4(131, 107, 255, 255)) {
		t.Errorf("dns.msftncsi.com: %v, %v; want the resolver's 131.107.255.255", ips, err)
	}
	// A name the resolver does not know is not found; FakeDNS must not answer for it.
	for _, name := range []string{"printer.lan", "nas", "router.home.arpa"} {
		if ips, _, err := client.LookupIP(name, option); err == nil {
			t.Errorf("%s: %v, want no answer", name, ips)
		}
	}
}

func TestCompiledProviderReentryMatchesIPRulesForHostnames(t *testing.T) {
	instance, stop := startTrafficCore(t, compiledRoutingFixture(t, "profile"))
	defer stop()
	router := instance.GetFeature(routing.RouterType()).(routing.Router)
	for _, fixture := range []struct {
		inbound string
		target  xnet.Address
		want    string
	}{
		{"elarni-socks", xnet.DomainAddress("blocked.test"), "elarni-block"},
		{"elarni-socks", xnet.DomainAddress("loopback-range.test"), "elarni-proxy"},
		{"elarni-provider", xnet.DomainAddress("loopback-range.test"), "direct"},
		{"elarni-provider", xnet.DomainAddress("named-loopback.test"), "proxy"},
		{"elarni-provider", xnet.ParseAddress("192.0.2.10"), "proxy"},
	} {
		if got := pickCompiledRoute(t, router, fixture.inbound, fixture.target); got != fixture.want {
			t.Errorf("%s %s: got %s, want %s", fixture.inbound, fixture.target, got, fixture.want)
		}
	}
	// App proxy traffic passes the real loopback into the provider policy, whose
	// IP rule sends the hostname direct to the echo listener; the provider's
	// fallback node refuses connections.
	for _, network := range []string{"tcp", "udp"} {
		trafficRoundTripFromInbound(t, instance, network, "127.0.0.1", "elarni-socks", "loopback-range.test")
	}
}

// Xray refuses two routing rules with one tag, so per-app routing with several targets, each
// with a rule of its own, must still start; so must a provider's rule that came with one of their names.
func TestCompiledPerAppRoutingWithSeveralTargetsStarts(t *testing.T) {
	config := compiledRoutingFixture(t, "per-app")
	listed := 0
	for _, rule := range config["routing"].(map[string]any)["rules"].([]any) {
		if tag, _ := rule.(map[string]any)["ruleTag"].(string); strings.HasPrefix(tag, "elarni-per-app-listed") {
			listed++
		}
	}
	if listed != 4 {
		t.Fatalf("%d per-app rules for three targets and a children rule", listed)
	}
	_, stop := startTrafficCore(t, config)
	stop()
}

// Per-app routing sends a listed app's TUN traffic through a loopback back into
// the profile's rules (compiler.rs apply_per_app_routing). A provider rule that
// matches the sniffed protocol must still match on that second pass.
func TestPerAppReentryKeepsSniffedProtocol(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(conn, conn); conn.Close() }()
		}
	}()
	port := trafficPort(t)
	var config map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{
		"inbounds": [{"tag": "elarni-tun", "listen": "127.0.0.1", "port": %d, "protocol": "dokodemo-door",
			"settings": {"address": "127.0.0.1", "port": %d, "network": "tcp"},
			"sniffing": {"enabled": true, "destOverride": ["http", "tls", "quic"], "routeOnly": true}}],
		"outbounds": [{"tag": "direct", "protocol": "freedom"}, {"tag": "block", "protocol": "blackhole"},
			{"tag": "elarni-apps-0", "protocol": "loopback", "settings": {"inboundTag": "elarni-apps-0"}}],
		"routing": {"rules": [
			{"type": "field", "inboundTag": ["elarni-tun"], "network": "tcp,udp", "outboundTag": "elarni-apps-0"},
			{"type": "field", "inboundTag": ["elarni-apps-0"], "protocol": ["http"], "outboundTag": "block"},
			{"type": "field", "inboundTag": ["elarni-apps-0"], "network": "tcp,udp", "outboundTag": "direct"}]}
	}`, port, echo.Addr().(*net.TCPAddr).Port)), &config); err != nil {
		t.Fatal(err)
	}
	_, stop := startTrafficCore(t, config)
	defer stop()
	// Traffic that no rule blocks still re-enters and reaches the echo server.
	control, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	payload := []byte("ping\n")
	control.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := control.Write(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(control, make([]byte, len(payload))); err != nil {
		t.Fatalf("re-entered traffic did not reach the echo server: %v", err)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := []byte("GET / HTTP/1.1\r\nHost: app.test\r\n\r\n")
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	if n, err := io.ReadFull(conn, make([]byte, len(request))); err == nil {
		t.Fatalf("the http rule did not block re-entered traffic; %d bytes echoed", n)
	}
}

// In proxy mode with automatic IP preference the direct outbound dials names
// AsIs. The fixture's pinned name exists only in its DnsHosts, and the proxy
// fallback refuses connections, so only a direct dial to the pin echoes.
func TestCompiledDirectRouteDialsDnsHostsPin(t *testing.T) {
	instance, stop := startTrafficCore(t, compiledRoutingFixture(t, "automatic-ip"))
	defer stop()
	for _, network := range []string{"tcp", "udp"} {
		trafficRoundTripFromInbound(t, instance, network, "127.0.0.1", "elarni-socks", "pinned-direct.test")
	}
}

// The TUN's own subnets (Android Platform.kt, desktop TunGateway4/6) hold only its DNS
// resolver. A Private DNS probe to it on 853 or a subnet broadcast there must end at
// once instead of leaving by a profile rule for 198.51.100.0/24, the fixtures' direct
// range, while DNS on 53 and the rest of that range keep their routes.
func TestCompiledTunBlocksItsOwnSubnets(t *testing.T) {
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(compiledRoutingJSON, &fixtures); err != nil {
		t.Fatal(err)
	}
	desktop, err := WindowsTunnelConfig(fixtures["tun"], "Elarni")
	if err != nil {
		t.Fatal(err)
	}
	var desktopConfig map[string]any
	if err := json.Unmarshal(desktop, &desktopConfig); err != nil {
		t.Fatal(err)
	}
	desktopConfig["inbounds"] = []any{}
	tcp := func(address string, port uint32) xnet.Destination {
		return xnet.TCPDestination(xnet.ParseAddress(address), xnet.Port(port))
	}
	udp := func(address string, port uint32) xnet.Destination {
		return xnet.UDPDestination(xnet.ParseAddress(address), xnet.Port(port))
	}
	for _, fixture := range []struct {
		name          string
		config        map[string]any
		dns           xnet.Destination
		dnsOutbound   string
		rangeOutbound string
		otherOutbound string
	}{
		{"tun", compiledRoutingFixture(t, "tun"), udp("198.51.100.254", 53), "elarni-dns", "elarni-direct", "elarni-proxy"},
		{"tun-per-app", compiledRoutingFixture(t, "tun-per-app"), udp("198.51.100.254", 53), "elarni-apps-0", "elarni-direct", "elarni-direct"},
		{"tun-profile", compiledRoutingFixture(t, "tun-profile"), udp("198.51.100.254", 53), "elarni-dns", "direct", "proxy"},
		{"desktop", desktopConfig, udp(TunDNS4, 53), "elarni-windows-dns", "elarni-direct", "elarni-proxy"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			instance, stop := startTrafficCore(t, fixture.config)
			defer stop()
			router := instance.GetFeature(routing.RouterType()).(routing.Router)
			for _, route := range []struct {
				inbound string
				target  xnet.Destination
				want    string
			}{
				{"elarni-tun", tcp("198.51.100.254", 853), "elarni-block"},
				{"elarni-tun", tcp(TunDNS4, 853), "elarni-block"},
				{"elarni-tun", udp("198.51.100.255", 137), "elarni-block"},
				{"elarni-tun", udp("198.51.100.255", 57621), "elarni-block"},
				{"elarni-tun", tcp("fdfe:dcba:9876::2", 853), "elarni-block"},
				{"elarni-tun", tcp(TunDNS6, 853), "elarni-block"},
				{"elarni-tun", fixture.dns, fixture.dnsOutbound},
				{"elarni-tun", tcp("198.51.100.20", 443), fixture.rangeOutbound},
				{"elarni-tun", tcp("fdfe:dcba:9876::10", 443), fixture.otherOutbound},
			} {
				picked, err := router.PickRoute(&routingsession.Context{
					Inbound:  &session.Inbound{Tag: route.inbound},
					Outbound: &session.Outbound{Target: route.target},
					Content:  &session.Content{},
				})
				if err != nil {
					t.Fatalf("%s: %v", route.target, err)
				}
				if got := picked.GetOutboundTag(); got != route.want {
					t.Errorf("%s: got %s, want %s", route.target, got, route.want)
				}
			}
		})
	}
}

// A blocked connection to the TUN's resolver on 853 closes at once, so Android's Private
// DNS check falls back to plain DNS instead of waiting out its 16 s timeout.
func TestCompiledTunClosesPrivateDNSProbeAtOnce(t *testing.T) {
	config := compiledRoutingFixture(t, "tun")
	port := trafficPort(t)
	config["inbounds"] = []any{map[string]any{"tag": "elarni-tun", "listen": "127.0.0.1", "port": port,
		"protocol": "dokodemo-door", "settings": map[string]any{"address": "198.51.100.254", "port": 853, "network": "tcp"}}}
	_, stop := startTrafficCore(t, config)
	defer stop()
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write([]byte("\x16\x03\x01\x00\x05hello"))
	start := time.Now()
	if n, err := conn.Read(make([]byte, 64)); err == nil || isTimeout(err) {
		t.Fatalf("probe got %d bytes, err %v after %s; want the connection closed", n, err, time.Since(start))
	}
}

func isTimeout(err error) bool {
	timeout, ok := err.(net.Error)
	return ok && timeout.Timeout()
}
