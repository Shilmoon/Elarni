package hysteria

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol/tls/cert"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/internet/tls"
)

// silentSocket takes datagrams and never answers, as an address without a route does.
type silentSocket struct {
	net.PacketConn
	received atomic.Int32
}

func listenSilent(t *testing.T, network, address string) (*silentSocket, error) {
	socket, err := net.ListenPacket(network, address)
	if err != nil {
		return nil, err
	}
	s := &silentSocket{PacketConn: socket}
	t.Cleanup(func() { socket.Close() })
	go func() {
		packet := make([]byte, 65535)
		for {
			if _, _, err := socket.ReadFrom(packet); err != nil {
				return
			}
			s.received.Add(1)
		}
	}()
	return s, nil
}

// settled returns how many datagrams arrived once none came for 100 ms.
func (s *silentSocket) settled() int32 {
	for {
		n := s.received.Load()
		time.Sleep(100 * time.Millisecond)
		if s.received.Load() == n {
			return n
		}
	}
}

// loopbackNames is the core's DNS: every name has ::1 and then 127.0.0.1.
type loopbackNames struct{}

func (loopbackNames) Type() interface{} { return dns.ClientType() }
func (loopbackNames) Start() error      { return nil }
func (loopbackNames) Close() error      { return nil }
func (loopbackNames) LookupIP(_ string, option dns.IPOption) ([]net.IP, uint32, error) {
	var ips []net.IP
	if option.IPv6Enable {
		ips = append(ips, net.IPv6loopback)
	}
	if option.IPv4Enable {
		ips = append(ips, net.IPv4(127, 0, 0, 1).To4())
	}
	return ips, 60, nil
}

func loopbackNamesContext() context.Context {
	return internet.ContextWithSystemDialerFeatures(context.Background(), func() (dns.Client, outbound.Manager) { return loopbackNames{}, nil })
}

// silentIPv6Server runs a Hysteria server on 127.0.0.1 and a silent socket on [::1] at the
// same port.
func silentIPv6Server(t *testing.T) (*silentSocket, xnet.Port) {
	for range 20 {
		silent, err := listenSilent(t, "udp6", "[::1]:0")
		if err != nil {
			t.Fatal(err)
		}
		port := xnet.Port(silent.LocalAddr().(*net.UDPAddr).Port)
		listener, err := Listen(context.Background(), xnet.LocalHostIP, port, &internet.MemoryStreamConfig{
			ProtocolName:     protocolName,
			ProtocolSettings: &Config{Auth: "p"},
			SecuritySettings: &tls.Config{Certificate: []*tls.Certificate{serverCertificate()}},
		}, func(conn stat.Connection) { conn.Close() })
		if err != nil {
			silent.Close()
			continue
		}
		t.Cleanup(func() { listener.Close() })
		return silent, port
	}
	t.Fatal("no port free on both loopback addresses")
	return nil, 0
}

func serverCertificate() *tls.Certificate {
	c, _ := cert.MustGenerate(nil, cert.DNSNames("localhost"), cert.CommonName("localhost"))
	parsed := tls.ParseCertificate(c)
	parsed.OneTimeLoading = true
	return parsed
}

// nameClient is a stream config of its own, so each dial opens a new connection, that has the
// core's DNS resolve the server name.
func nameClient(t *testing.T) *internet.MemoryStreamConfig {
	settings := &internet.MemoryStreamConfig{ProtocolName: protocolName, ProtocolSettings: &Config{Auth: "p"},
		SecuritySettings: &tls.Config{ServerName: "localhost", AllowInsecure: true},
		SocketSettings:   &internet.SocketConfig{DomainStrategy: internet.DomainStrategy_USE_IP}}
	t.Cleanup(func() { internet.ReleaseTransport(settings) })
	return settings
}

// ipv6First has settings try a name's IPv6 addresses first, as happyEyeballs' prioritizeIPv6 does.
func ipv6First(settings *internet.MemoryStreamConfig) *internet.MemoryStreamConfig {
	settings.SocketSettings.HappyEyeballs = &internet.HappyEyeballsConfig{PrioritizeIpv6: true, Interleave: 1}
	return settings
}

// A server name whose first address stays silent (an IPv6 address on a network without IPv6)
// was dialed at that address alone until the dial timed out. The next address starts beside it
// after the stagger and wins within the budget; later dials start with the address that
// answered until the network changes.
func TestDialReachesTheNextAddressOfTheServerName(t *testing.T) {
	defer func(d time.Duration) { dialTimeout = d }(dialTimeout)
	dialTimeout = 2 * time.Second
	t.Cleanup(internet.SetUDPAttemptStagger(time.Second))
	t.Cleanup(internet.SocketPathChanged)
	silent, port := silentIPv6Server(t)
	dest := xnet.UDPDestination(xnet.DomainAddress("hysteria-next.test"), port)
	dial := func() time.Duration {
		t.Helper()
		started := time.Now()
		conn, err := Dial(loopbackNamesContext(), dest, ipv6First(nameClient(t)))
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		return time.Since(started)
	}

	if took := dial(); took > dialTimeout {
		t.Fatalf("connected after %v, budget %v", took, dialTimeout)
	}
	tried := silent.settled()
	if tried == 0 {
		t.Fatal("the dial did not start with the IPv6 address")
	}
	if took := dial(); took >= dialTimeout/2 {
		t.Fatalf("the next dial took %v: it waited for the silent address", took)
	}
	if silent.settled() != tried {
		t.Fatal("the next dial tried the silent address before the one that answered")
	}
	internet.SocketPathChanged()
	dial()
	if silent.settled() == tried {
		t.Fatal("after a network change the dial did not start with the IPv6 address")
	}
}

// A server that refuses the password has answered: the dial ends there with the error a dial
// of that address alone gives, and the name's other address (IPv6, which IPv4 goes before) is
// not contacted.
func TestDialStopsAtAServerThatRefusesThePassword(t *testing.T) {
	defer func(d time.Duration) { dialTimeout = d }(dialTimeout)
	dialTimeout = 2 * time.Second
	silent, port := silentIPv6Server(t)
	wrongPassword := func() *internet.MemoryStreamConfig {
		settings := nameClient(t)
		settings.ProtocolSettings = &Config{Auth: "wrong"}
		return settings
	}
	_, alone := Dial(context.Background(), xnet.UDPDestination(xnet.LocalHostIP, port), wrongPassword())
	if alone == nil || !strings.Contains(alone.Error(), "auth failed") {
		t.Fatalf("a dial of the server's address with a wrong password gave %v", alone)
	}
	_, err := Dial(loopbackNamesContext(), xnet.UDPDestination(xnet.DomainAddress("hysteria-refused.test"), port), wrongPassword())
	if err == nil || err.Error() != alone.Error() {
		t.Fatalf("the name's dial gave %v, the address's %v", err, alone)
	}
	if tried := silent.settled(); tried != 0 {
		t.Fatalf("the dial went on to the name's other address (%d datagrams)", tried)
	}
}

// When no address of the name answers, the dial ends within its budget and its error names
// each address.
func TestDialNamesEachSilentAddressOfTheServerName(t *testing.T) {
	defer func(d time.Duration) { dialTimeout = d }(dialTimeout)
	dialTimeout = time.Second
	t.Cleanup(internet.SetUDPAttemptStagger(200 * time.Millisecond))
	var v4, v6 *silentSocket
	for v4 == nil {
		var err error
		if v6, err = listenSilent(t, "udp6", "[::1]:0"); err != nil {
			t.Fatal(err)
		}
		if v4, err = listenSilent(t, "udp4", strings.Replace(v6.LocalAddr().String(), "[::1]", "127.0.0.1", 1)); err != nil {
			v6.Close()
		}
	}
	port := xnet.Port(v6.LocalAddr().(*net.UDPAddr).Port)
	started := time.Now()
	_, err := Dial(loopbackNamesContext(), xnet.UDPDestination(xnet.DomainAddress("hysteria-silent.test"), port), nameClient(t))
	if took := time.Since(started); took > dialTimeout+500*time.Millisecond {
		t.Fatalf("failed after %v, budget %v", took, dialTimeout)
	}
	if err == nil {
		t.Fatal("dialed a server that never answers")
	}
	t.Log(err)
	for _, address := range []string{"[::1]:" + port.String(), "127.0.0.1:" + port.String()} {
		if !strings.Contains(err.Error(), address) {
			t.Errorf("the error does not name %s: %v", address, err)
		}
	}
	if v6.settled() == 0 || v4.settled() == 0 {
		t.Fatal("an address was not tried")
	}
}
