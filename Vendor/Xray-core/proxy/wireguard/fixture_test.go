package wireguard

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/netip"
	"testing"

	awg "github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/transport/internet"
	"golang.zx2c4.com/wireguard/device"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// tunnelFixture is a client Handler and a server engine that talk over a loopback socket, each with
// its own gVisor stack: 10.29.0.2 and fd29::2 for the client, 10.29.0.1 and fd29::1 for the server.
// Keys are fresh; no OS VPN, production peer, persistent identity or root privileges are involved.
type tunnelFixture struct {
	handler     *Handler // not started: the test calls init
	clientNet   *Net
	server      tunnelDevice
	serverStack *stack.Stack
	clientKey   string // public, hex, as the server's IpcSet takes it
}

type fixtureOptions struct {
	amnezia  bool
	settings map[string]string // AmneziaWG settings of both ends
	mtu      int               // 1280 when 0
	dns      dns.Client        // the handler's, for its endpoint
	// wrapServer wraps the server's socket, to watch what reaches it.
	wrapServer func(net.PacketConn) net.PacketConn
}

// newTunnelFixture starts the server and leaves the handler to the test. Cleanup closes both, and
// the handler owns the client stack from the moment the stack exists.
func newTunnelFixture(tb testing.TB, o fixtureOptions) *tunnelFixture {
	tb.Helper()
	if o.mtu == 0 {
		o.mtu = 1280
	}
	clientKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	serverSocket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { serverSocket.Close() })
	clientTun, clientNet, _, err := CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.29.0.2"), netip.MustParseAddr("fd29::2")}, []netip.Addr{netip.MustParseAddr("10.29.0.1")}, o.mtu, true)
	if err != nil {
		tb.Fatal(err)
	}
	handler := &Handler{conf: &DeviceConfig{SecretKey: hex.EncodeToString(clientKey.Bytes()), Amnezia: o.amnezia, AmneziaSettings: o.settings,
		Peers: []*PeerConfig{{PublicKey: hex.EncodeToString(serverKey.PublicKey().Bytes()), Endpoint: serverSocket.LocalAddr().String(), AllowedIps: []string{"0.0.0.0/0", "::/0"}}}},
		dns: o.dns, tun: clientTun, tnet: clientNet, streamSettings: &internet.MemoryStreamConfig{}}
	tb.Cleanup(func() { handler.Close() })
	serverTun, _, serverStack, err := CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.29.0.1"), netip.MustParseAddr("fd29::1")}, nil, o.mtu, true)
	if err != nil {
		tb.Fatal(err)
	}
	var socket net.PacketConn = serverSocket
	if o.wrapServer != nil {
		socket = o.wrapServer(serverSocket)
	}
	serverBind := &bind{preserveHeader: o.amnezia, listenFunc: func() (net.PacketConn, error) { return socket, nil }}
	var server tunnelDevice
	if o.amnezia {
		server = amneziaDevice{awg.NewDevice(adaptAmneziaTUN(serverTun), &amneziaBind{serverBind}, &awg.Logger{Verbosef: func(string, ...any) {}, Errorf: tb.Logf})}
	} else {
		server = wireguardDevice{device.NewDevice(serverTun, serverBind, &device.Logger{Verbosef: func(string, ...any) {}, Errorf: tb.Logf})}
	}
	tb.Cleanup(server.Close)
	f := &tunnelFixture{handler: handler, clientNet: clientNet, server: server, serverStack: serverStack, clientKey: hex.EncodeToString(clientKey.PublicKey().Bytes())}
	config := "private_key=" + hex.EncodeToString(serverKey.Bytes()) + "\n"
	for key, value := range o.settings {
		config += key + "=" + value + "\n"
	}
	if err := server.IpcSet(config); err != nil {
		tb.Fatal(err)
	}
	f.addClient(tb)
	if err := server.Up(); err != nil {
		tb.Fatal(err)
	}
	return f
}

// addClient lets the client in on the server, anew after removeClient.
func (f *tunnelFixture) addClient(tb testing.TB) {
	tb.Helper()
	if err := f.server.IpcSet("public_key=" + f.clientKey + "\nallowed_ip=10.29.0.2/32\nallowed_ip=fd29::2/128\n"); err != nil {
		tb.Fatal(err)
	}
}

// removeClient makes the server forget the client and its keys, as a server does with keys older
// than RejectAfterTime.
func (f *tunnelFixture) removeClient(tb testing.TB) {
	tb.Helper()
	if err := f.server.IpcSet("public_key=" + f.clientKey + "\nremove=true\n"); err != nil {
		tb.Fatal(err)
	}
}
