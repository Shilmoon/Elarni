package xray

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/dns"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/proxy"
)

func unusedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// nameServer answers A and AAAA queries from records (name without the final dot to an address of
// either family) and everything else empty, on a loopback UDP port it returns.
func nameServer(t *testing.T, records map[string]string) int {
	t.Helper()
	socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	go func() {
		packet := make([]byte, 1500)
		for {
			n, from, err := socket.ReadFrom(packet)
			if err != nil {
				return
			}
			var parser dnsmessage.Parser
			header, err := parser.Start(packet[:n])
			if err != nil {
				continue
			}
			question, err := parser.Question()
			if err != nil {
				continue
			}
			answer := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, RecursionAvailable: true})
			answer.StartQuestions()
			answer.Question(question)
			answer.StartAnswers()
			name := question.Name.String()
			address := net.ParseIP(records[name[:len(name)-1]])
			if ipv4 := address.To4(); ipv4 != nil && question.Type == dnsmessage.TypeA {
				answer.AResource(dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					dnsmessage.AResource{A: [4]byte(ipv4)})
			} else if ipv4 == nil && address != nil && question.Type == dnsmessage.TypeAAAA {
				answer.AAAAResource(dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: 60},
					dnsmessage.AAAAResource{AAAA: [16]byte(address.To16())})
			}
			if reply, err := answer.Finish(); err == nil {
				socket.WriteTo(reply, from)
			}
		}
	}()
	return socket.LocalAddr().(*net.UDPAddr).Port
}

// appLooksUp asks instance's DNS for names, as an app does through the tunnel before it connects.
func appLooksUp(t *testing.T, instance *core.Instance, names ...string) {
	t.Helper()
	client := instance.GetFeature(dns.ClientType()).(dns.Client)
	for _, name := range names {
		if _, _, err := client.LookupIP(name, dns.IPOption{IPv4Enable: true, IPv6Enable: true}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The app dials an address the profile's DNS gave it for a name, and the name is only sniffed for
// routing. A server outbound must hand the server that name, since the server routes by name and
// its exit may have no IPv6, also after a loopback into a provider's routing (looped.test); a
// direct outbound must still dial the app's IP.
func TestServerGetsTheSniffedNameWhileDirectKeepsTheIP(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "site") }))
	defer site.Close()
	siteURL, _ := url.Parse(site.URL)
	clientPort, serverPort := unusedPort(t), unusedPort(t)
	names := nameServer(t, map[string]string{"named.test": "2001:db8::1", "looped.test": "2001:db8::1", "named4.test": "192.0.2.1"})
	// server-in stands in for the remote server: only a request that names the site reaches it.
	instance, err := StartXrayFromJSON(fmt.Sprintf(`{
		"dns": {"tag": "names", "servers": [{"address": "127.0.0.1", "port": %d}], "useSystemHosts": false},
		"inbounds": [
			{"tag": "client-in", "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"udp": false},
				"sniffing": {"enabled": true, "destOverride": ["http"], "routeOnly": true}},
			{"tag": "server-in", "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"udp": false}}],
		"outbounds": [
			{"tag": "server", "protocol": "socks", "settings": {"servers": [{"address": "127.0.0.1", "port": %d}]}},
			{"tag": "direct", "protocol": "freedom"},
			{"tag": "site", "protocol": "freedom", "settings": {"redirect": "%s"}},
			{"tag": "loop", "protocol": "loopback", "settings": {"inboundTag": "provider"}},
			{"tag": "block", "protocol": "blackhole"}],
		"routing": {"rules": [
			{"inboundTag": ["server-in"], "domain": ["full:named.test", "full:looped.test", "full:named4.test"], "outboundTag": "site"},
			{"inboundTag": ["server-in"], "network": "tcp,udp", "outboundTag": "block"},
			{"inboundTag": ["names"], "outboundTag": "direct"},
			{"inboundTag": ["provider"], "outboundTag": "server"},
			{"domain": ["full:named.test", "full:named4.test"], "outboundTag": "server"},
			{"domain": ["full:looped.test"], "outboundTag": "loop"},
			{"domain": ["full:direct.test"], "outboundTag": "direct"},
			{"network": "tcp,udp", "outboundTag": "block"}]}}`, names, clientPort, serverPort, serverPort, siteURL.Host))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", clientPort))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
	appLooksUp(t, instance, "named.test", "looped.test", "named4.test")
	// No route leads to these addresses: only the name can reach the site through the server.
	unreachable := fmt.Sprintf("http://[2001:db8::1]:%s", siteURL.Port())
	unreachable4 := fmt.Sprintf("http://192.0.2.1:%s", siteURL.Port())
	for host, dialed := range map[string]string{"named.test": unreachable, "looped.test": unreachable, "named4.test": unreachable4, "direct.test": site.URL} {
		request, _ := http.NewRequest("GET", dialed, nil) // dials the IP, names host
		request.Host = host
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != "site" {
			t.Fatalf("%s: got %q, %v", host, body, err)
		}
	}
}

// A name the profile pins in its DNS hosts reaches the pinned IP; the server would look the name up
// itself and miss the pin, so it gets the IP although the profile's DNS gave it for the name.
func TestServerGetsTheIPOfAPinnedName(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "site") }))
	defer site.Close()
	siteURL, _ := url.Parse(site.URL)
	clientPort, serverPort := unusedPort(t), unusedPort(t)
	// server-in refuses the name, as a server that resolves it elsewhere would; only a request for
	// the pinned address reaches the site.
	instance, err := StartXrayFromJSON(fmt.Sprintf(`{
		"dns": {"hosts": {"pinned.test": "2001:db8::1"}},
		"inbounds": [
			{"tag": "client-in", "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"udp": false},
				"sniffing": {"enabled": true, "destOverride": ["http"], "routeOnly": true}},
			{"tag": "server-in", "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"udp": false}}],
		"outbounds": [
			{"tag": "server", "protocol": "socks", "settings": {"servers": [{"address": "127.0.0.1", "port": %d}]}},
			{"tag": "site", "protocol": "freedom", "settings": {"redirect": "%s"}},
			{"tag": "block", "protocol": "blackhole"}],
		"routing": {"rules": [
			{"inboundTag": ["server-in"], "domain": ["full:pinned.test"], "outboundTag": "block"},
			{"inboundTag": ["server-in"], "ip": ["2001:db8::1"], "outboundTag": "site"},
			{"inboundTag": ["server-in"], "network": "tcp,udp", "outboundTag": "block"},
			{"domain": ["full:pinned.test"], "outboundTag": "server"},
			{"network": "tcp,udp", "outboundTag": "block"}]}}`, clientPort, serverPort, serverPort, siteURL.Host))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", clientPort))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
	appLooksUp(t, instance, "pinned.test")
	request, _ := http.NewRequest("GET", fmt.Sprintf("http://[2001:db8::1]:%s", siteURL.Port()), nil) // dials the pinned IP, names pinned.test
	request.Host = "pinned.test"
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "site" {
		t.Fatalf("got %q, %v", body, err)
	}
}

// clientHello returns the first flight of a TLS handshake that names serverName.
func clientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	app, wire := net.Pipe()
	defer app.Close()
	defer wire.Close()
	go tls.Client(app, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}).Handshake()
	wire.SetReadDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 5)
	if _, err := io.ReadFull(wire, header); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint16(header[3:]))
	if _, err := io.ReadFull(wire, body); err != nil {
		t.Fatal(err)
	}
	return append(header, body...)
}

// Telegram disguises a connection to its own IPv4 address as TLS to an unrelated site. The name
// in such a hello says nothing about where the app connects, so the server must get the IP: with
// the name it would connect to that site and Telegram would never reach its data center.
func TestServerGetsTheIPv4AddressOfADisguisedConnection(t *testing.T) {
	dataCenter, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dataCenter.Close()
	go func() {
		for {
			connection, err := dataCenter.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				// The whole hello: a close with part of it unread resets the connection, which can lose the reply.
				header := make([]byte, 5)
				if _, err := io.ReadFull(connection, header); err == nil {
					io.CopyN(io.Discard, connection, int64(binary.BigEndian.Uint16(header[3:])))
					io.WriteString(connection, "data center")
				}
			}()
		}
	}()
	clientPort, serverPort := unusedPort(t), unusedPort(t)
	// The decoy is a real site with its own address, and somebody on the device has looked it up.
	dnsPort := nameServer(t, map[string]string{"decoy.test": "192.0.2.9"})
	// server-in refuses the name, as a server that looks it up reaches the site, and dials an IP.
	instance, err := StartXrayFromJSON(fmt.Sprintf(`{
		"dns": {"tag": "names", "servers": [{"address": "127.0.0.1", "port": %d}], "useSystemHosts": false},
		"inbounds": [
			{"tag": "client-in", "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"udp": false},
				"sniffing": {"enabled": true, "destOverride": ["tls"], "routeOnly": true}},
			{"tag": "server-in", "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"udp": false}}],
		"outbounds": [
			{"tag": "server", "protocol": "socks", "settings": {"servers": [{"address": "127.0.0.1", "port": %d}]}},
			{"tag": "direct", "protocol": "freedom"},
			{"tag": "block", "protocol": "blackhole"}],
		"routing": {"rules": [
			{"inboundTag": ["names"], "outboundTag": "direct"},
			{"inboundTag": ["server-in"], "domain": ["full:decoy.test"], "outboundTag": "block"},
			{"inboundTag": ["server-in"], "ip": ["127.0.0.1"], "outboundTag": "direct"},
			{"inboundTag": ["server-in"], "network": "tcp,udp", "outboundTag": "block"},
			{"domain": ["full:decoy.test"], "outboundTag": "server"},
			{"network": "tcp,udp", "outboundTag": "block"}]}}`, dnsPort, clientPort, serverPort, serverPort))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	appLooksUp(t, instance, "decoy.test")
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dialer.Dial("tcp", dataCenter.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := connection.Write(clientHello(t, "decoy.test")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len("data center"))
	if _, err = io.ReadFull(connection, reply); string(reply) != "data center" {
		t.Fatalf("got %q, %v", reply, err)
	}
}

// A name the app sends need not be the name of the address it dialed: a TLS hello that names a
// decoy site (Telegram, REALITY and ShadowTLS clients inside the tunnel), a request to the app's
// own HTTP proxy, which names the site behind it, or a name the profile's DNS hosts map to another
// name. The server gets a name only when the profile's DNS gave the app the dialed address for
// it (real.test); otherwise it must connect to the address.
func TestServerGetsTheAddressANameDoesNotDescribe(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "by address") }))
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)
	named := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "by name") }))
	defer named.Close()
	namedURL, _ := url.Parse(named.URL)
	clientPort, serverPort := unusedPort(t), unusedPort(t)
	names := nameServer(t, map[string]string{"elsewhere.test": "2001:db8::99", "real.test": "2001:db8::2"})
	// server-in stands in for the remote server: a request for the dialed address reaches the
	// origin, one that names real.test the host a lookup of that name leads to, any other name
	// nothing, as a server that looks one up reaches another host.
	instance, err := StartXrayFromJSON(fmt.Sprintf(`{
		"dns": {"tag": "names", "hosts": {"alias.test": "real.test"}, "servers": [{"address": "127.0.0.1", "port": %d}], "useSystemHosts": false},
		"inbounds": [
			{"tag": "client-in", "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"udp": false},
				"sniffing": {"enabled": true, "destOverride": ["http"], "routeOnly": true}},
			{"tag": "server-in", "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"udp": false}}],
		"outbounds": [
			{"tag": "server", "protocol": "socks", "settings": {"servers": [{"address": "127.0.0.1", "port": %d}]}},
			{"tag": "direct", "protocol": "freedom"},
			{"tag": "origin", "protocol": "freedom", "settings": {"redirect": "%s"}},
			{"tag": "named", "protocol": "freedom", "settings": {"redirect": "%s"}},
			{"tag": "block", "protocol": "blackhole"}],
		"routing": {"rules": [
			{"inboundTag": ["server-in"], "domain": ["full:real.test"], "outboundTag": "named"},
			{"inboundTag": ["server-in"], "ip": ["2001:db8::2"], "outboundTag": "origin"},
			{"inboundTag": ["server-in"], "network": "tcp,udp", "outboundTag": "block"},
			{"inboundTag": ["names"], "outboundTag": "direct"},
			{"network": "tcp,udp", "outboundTag": "server"}]}}`, names, clientPort, serverPort, serverPort, originURL.Host, namedURL.Host))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	// The app has looked every one of these names up but unknown.test: elsewhere.test has another
	// address, and alias.test got the dialed one, as the hosts make it real.test.
	appLooksUp(t, instance, "real.test", "elsewhere.test", "alias.test")
	proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", clientPort))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
	for host, want := range map[string]string{"real.test": "by name", "elsewhere.test": "by address", "unknown.test": "by address", "alias.test": "by address"} {
		request, _ := http.NewRequest("GET", fmt.Sprintf("http://[2001:db8::2]:%s", originURL.Port()), nil)
		request.Host = host
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != want {
			t.Fatalf("%s: got %q, %v, want %q", host, body, err, want)
		}
	}
}
