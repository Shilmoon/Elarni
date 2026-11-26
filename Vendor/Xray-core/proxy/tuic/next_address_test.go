package tuic

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport/internet"
	xTLS "github.com/xtls/xray-core/transport/internet/tls"
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

// nameClient is an outbound of its own, so each flow opens a new connection, whose server is
// a name the core's DNS resolves.
func nameClient(t *testing.T, name string, port int) *Client {
	c := &Client{config: &ClientConfig{Address: xnet.NewIPOrDomain(xnet.DomainAddress(name)), Port: uint32(port), Uuid: userID[:], Password: "p", Heartbeat: int64(10 * time.Second)},
		stream: &internet.MemoryStreamConfig{SecuritySettings: &xTLS.Config{ServerName: "localhost", AllowInsecure: true},
			SocketSettings: &internet.SocketConfig{DomainStrategy: internet.DomainStrategy_USE_IP}}}
	t.Cleanup(func() { c.Close() })
	return c
}

// nameFlow opens a TCP flow over c as Process does.
func nameFlow(c *Client) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(loopbackNamesContext(), 2*handshakeTimeout)
	defer cancel()
	client, err := c.initialize(ctx, noGateway{})
	if err != nil {
		return nil, err
	}
	return client.DialConn(ctx, M.ParseSocksaddr("127.0.0.1:80"))
}

// A server name whose first address stays silent (an IPv6 address on a network without IPv6)
// was dialed at that address alone until the handshake timed out. The handshake moves to the
// next address within its timeout; later connections start with the address that answered
// until the network changes.
func TestHandshakeReachesTheNextAddressOfTheServerName(t *testing.T) {
	defer func(d time.Duration) { handshakeTimeout = d }(handshakeTimeout)
	handshakeTimeout = 2 * time.Second
	t.Cleanup(internet.SetUDPAttemptMinimum(100 * time.Millisecond))
	t.Cleanup(internet.SocketPathChanged)
	var silent *silentSocket
	var port int
	for silent == nil {
		port = startService(t, echo{}).(*net.UDPAddr).Port
		silent, _ = listenSilent(t, "udp6", net.JoinHostPort("::1", xnet.Port(port).String()))
	}
	flow := func() time.Duration {
		t.Helper()
		started := time.Now()
		conn, err := nameFlow(nameClient(t, "tuic-next.test", port))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := echoes(conn); err != nil {
			t.Fatal(err)
		}
		return time.Since(started)
	}

	if took := flow(); took > handshakeTimeout {
		t.Fatalf("connected after %v, budget %v", took, handshakeTimeout)
	}
	tried := silent.settled()
	if tried == 0 {
		t.Fatal("the handshake did not start with the resolver's first address")
	}
	if took := flow(); took >= handshakeTimeout/2 {
		t.Fatalf("the next connection took %v: it waited for the silent address", took)
	}
	if silent.settled() != tried {
		t.Fatal("the next connection tried the silent address before the one that answered")
	}
	internet.SocketPathChanged()
	flow()
	if silent.settled() == tried {
		t.Fatal("after a network change the handshake did not start with the resolver's first address")
	}
}

// When no address of the name answers, the flow fails within the handshake timeout and the
// error names each address; no socket stays open.
func TestHandshakeNamesEachSilentAddressOfTheServerName(t *testing.T) {
	defer func(d time.Duration) { handshakeTimeout = d }(handshakeTimeout)
	handshakeTimeout = time.Second
	t.Cleanup(internet.SetUDPAttemptMinimum(100 * time.Millisecond))
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
	port := v6.LocalAddr().(*net.UDPAddr).Port
	c := nameClient(t, "tuic-silent.test", port)
	started := time.Now()
	conn, err := nameFlow(c)
	if took := time.Since(started); took > handshakeTimeout+500*time.Millisecond {
		t.Fatalf("failed after %v, budget %v", took, handshakeTimeout)
	}
	if err == nil {
		conn.Close()
		t.Fatal("connected to a server that never answers")
	}
	t.Log(err)
	for _, address := range []string{"[::1]:" + xnet.Port(port).String(), "127.0.0.1:" + xnet.Port(port).String()} {
		if !strings.Contains(err.Error(), address) {
			t.Errorf("the error does not name %s: %v", address, err)
		}
	}
	if v6.settled() == 0 || v4.settled() == 0 {
		t.Fatal("an address was not tried")
	}
	c.mu.Lock()
	sockets := c.current.sockets.open()
	c.mu.Unlock()
	if len(sockets) != 0 {
		t.Fatalf("%d sockets of the failed handshake stayed open", len(sockets))
	}
}
