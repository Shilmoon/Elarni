package splithttp_test

import (
	"context"
	gonet "net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/splithttp"
)

// silentSocket takes datagrams and never answers, as an address without a route does.
type silentSocket struct {
	gonet.PacketConn
	received atomic.Int32
}

func listenSilent(t *testing.T, network, address string) (*silentSocket, error) {
	socket, err := gonet.ListenPacket(network, address)
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
		ips = append(ips, gonet.IPv6loopback)
	}
	if option.IPv4Enable {
		ips = append(ips, gonet.IPv4(127, 0, 0, 1).To4())
	}
	return ips, 60, nil
}

func loopbackNamesContext() context.Context {
	return internet.ContextWithSystemDialerFeatures(context.Background(), func() (dns.Client, outbound.Manager) { return loopbackNames{}, nil })
}

// nameClient is an XHTTP/3 client of its own, so each dial opens a new connection, that has the
// core's DNS resolve the server name.
func nameClient(t *testing.T) *internet.MemoryStreamConfig {
	client := xhttpClient(t, true, `{"path":"/x","mode":"stream-one"}`)
	client.SocketSettings = &internet.SocketConfig{DomainStrategy: internet.DomainStrategy_USE_IP}
	return client
}

// ipv6First has settings try a name's IPv6 addresses first, as happyEyeballs' prioritizeIPv6 does.
func ipv6First(settings *internet.MemoryStreamConfig) *internet.MemoryStreamConfig {
	settings.SocketSettings.HappyEyeballs = &internet.HappyEyeballsConfig{PrioritizeIpv6: true, Interleave: 1}
	return settings
}

// A server name whose first address stays silent (an IPv6 address on a network without IPv6)
// was dialed at that address alone until the setup bound. The next address starts beside it
// after the stagger and wins within the bound; later dials start with the address that answered
// until the network changes.
func TestXHTTP3ReachesTheNextAddressOfTheServerName(t *testing.T) {
	const budget = 2 * time.Second
	t.Cleanup(splithttp.SetSetupTimeout(budget))
	t.Cleanup(internet.SetUDPAttemptStagger(time.Second))
	t.Cleanup(internet.SocketPathChanged)
	var silent *silentSocket
	var port net.Port
	for silent == nil {
		port = destination(t, echoServer(t, true, nil)).Port
		silent, _ = listenSilent(t, "udp6", "[::1]:"+port.String())
	}
	dest := net.TCPDestination(net.DomainAddress("xhttp-next.test"), port)
	dial := func() time.Duration {
		t.Helper()
		started := time.Now()
		conn := dialXHTTP(t, loopbackNamesContext(), dest, ipv6First(nameClient(t)))
		if err := echo(conn, 1); err != nil {
			t.Fatal(err)
		}
		return time.Since(started)
	}

	if took := dial(); took > budget {
		t.Fatalf("connected after %v, budget %v", took, budget)
	}
	tried := silent.settled()
	if tried == 0 {
		t.Fatal("the dial did not start with the IPv6 address")
	}
	if took := dial(); took >= budget/2 {
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

// When no address of the name answers, the session fails within the setup bound after both
// addresses were tried, and the QUIC dial's error names each address. The session itself fails
// with its request's own setup bound, which starts first.
func TestXHTTP3NamesEachSilentAddressOfTheServerName(t *testing.T) {
	const budget = time.Second
	t.Cleanup(splithttp.SetSetupTimeout(budget))
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
	port := net.Port(v6.LocalAddr().(*gonet.UDPAddr).Port)
	dest := net.TCPDestination(net.DomainAddress("xhttp-silent.test"), port)
	started := time.Now()
	conn, err := splithttp.Dial(loopbackNamesContext(), dest, nameClient(t))
	if took := time.Since(started); took > budget+500*time.Millisecond {
		t.Fatalf("the session failed after %v, budget %v", took, budget)
	}
	if err == nil {
		conn.Close()
		t.Fatal("dialed a server that never answers")
	}
	if v6.settled() == 0 || v4.settled() == 0 {
		t.Fatal("an address was not tried")
	}

	started = time.Now()
	err = splithttp.DialQUIC(loopbackNamesContext(), udp(dest), nameClient(t))
	if took := time.Since(started); took > budget+500*time.Millisecond {
		t.Fatalf("the dial failed after %v, budget %v", took, budget)
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
}
