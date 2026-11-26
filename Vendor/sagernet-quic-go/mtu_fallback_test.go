//go:build darwin || linux || windows

package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/quic-go/congestion"
	"github.com/sagernet/quic-go/internal/ackhandler"
	"github.com/sagernet/quic-go/internal/protocol"
)

// mtu1280Payload is the largest UDP payload an interface with a 1280-byte MTU sends over IPv6.
const mtu1280Payload = 1232

// msgSizeLimitedConn refuses datagrams over limit bytes with the system's "message too long"
// error, as a socket on an interface with a small MTU does. It hides the socket's system-call
// interface, so quic-go sends every packet through WriteTo.
type msgSizeLimitedConn struct {
	net.PacketConn
	limit   int // 0: no limit
	refused atomic.Int32
}

func (c *msgSizeLimitedConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.limit > 0 && len(p) > c.limit {
		c.refused.Add(1)
		return 0, &net.OpError{Op: "write", Net: "udp", Addr: addr, Err: os.NewSyscallError("sendto", msgSizeErrno())}
	}
	return c.PacketConn.WriteTo(p, addr)
}

func msgSizeErrno() error {
	if runtime.GOOS == "windows" {
		return syscall.Errno(10040) // WSAEMSGSIZE
	}
	return syscall.EMSGSIZE
}

func listenLimited(t *testing.T, limit int) *msgSizeLimitedConn {
	t.Helper()
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	return &msgSizeLimitedConn{PacketConn: socket, limit: limit}
}

// dialLimited dials a loopback echo server whose socket refuses writes over serverLimit bytes,
// from a socket that refuses writes over clientLimit bytes, and returns both ends. The server
// takes DisablePathMTUDiscovery from conf.
func dialLimited(t *testing.T, ctx context.Context, conf *Config, clientLimit, serverLimit int) (client, server *Conn, clientConn, serverConn *msgSizeLimitedConn, err error) {
	t.Helper()
	serverConn = listenLimited(t, serverLimit)
	ln, err := Listen(serverConn, &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}, NextProtos: []string{"t"}}, &Config{HandshakeIdleTimeout: time.Minute, DisablePathMTUDiscovery: conf.DisablePathMTUDiscovery})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted := make(chan *Conn, 1)
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			accepted <- conn
			go func() {
				str, err := conn.AcceptStream(context.Background())
				if err == nil {
					io.Copy(str, str)
				}
			}()
		}
	}()
	clientConn = listenLimited(t, clientLimit)
	conf = conf.Clone()
	// A handshake timeout far past the context's deadline: a dial that waited for it fails the test.
	conf.HandshakeIdleTimeout = time.Minute
	client, err = Dial(ctx, clientConn, ln.Addr(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"t"}}, conf)
	if err != nil {
		return nil, nil, clientConn, serverConn, err
	}
	t.Cleanup(func() { client.CloseWithError(0, "") })
	select {
	case server = <-accepted:
	case <-ctx.Done():
		t.Fatal("the server did not accept the connection")
	}
	return client, server, clientConn, serverConn, nil
}

func checkEcho(t *testing.T, ctx context.Context, conn *Conn) {
	t.Helper()
	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	msg := make([]byte, 5000) // several full packets
	for i := range msg {
		msg[i] = byte(i)
	}
	if _, err := str.Write(msg); err != nil {
		t.Fatal(err)
	}
	str.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply := make([]byte, len(msg))
	if _, err := io.ReadFull(str, reply); err != nil {
		t.Fatalf("no echo: %v", err)
	}
}

// Upstream quic-go#5911 (99c74363), ported: on an interface whose MTU is below the Initial packet
// size (1280 bytes, or Chrome's 1250; some tunnels, mobile networks and an IPv6 path with the
// minimum 1280-byte MTU), every handshake packet failed with "message too long", the send queue
// dropped the error, and the dial only ended at the handshake timeout. Now the connection falls
// back to 1200 bytes and resends the refused packet at that size.
func TestHandshakeFallsBackToMinPacketSizeOnMsgSizeError(t *testing.T) {
	if !isSendMsgSizeErr(&net.OpError{Err: os.NewSyscallError("sendto", msgSizeErrno())}) {
		t.Fatalf("%v is not this system's \"message too long\" error", msgSizeErrno())
	}
	for _, tc := range []struct {
		name                     string
		conf                     *Config
		clientLimit, serverLimit int
		wantClient, wantServer   protocol.ByteCount
	}{
		{"client at the default size", &Config{}, mtu1280Payload, 0, protocol.MinInitialPacketSize, protocol.InitialPacketSize},
		{"client at Chrome's size", &Config{ChromeParrot: true}, mtu1280Payload, 0, protocol.MinInitialPacketSize, protocol.InitialPacketSize},
		{"server at the default size", &Config{}, 0, mtu1280Payload, protocol.InitialPacketSize, protocol.MinInitialPacketSize},
		{"both ends", &Config{ChromeParrot: true}, mtu1280Payload, mtu1280Payload, protocol.MinInitialPacketSize, protocol.MinInitialPacketSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Without path MTU discovery on either end: its first probe goes out five RTTs after the
			// handshake is confirmed, sometimes while the echo below still runs, and is above the
			// size in effect by design (1326 bytes here), so the socket refuses it. The check after
			// the echo is for every other packet, still sized from the MTU discoverer.
			conf := tc.conf.Clone()
			conf.DisablePathMTUDiscovery = true
			start := time.Now()
			client, server, clientConn, serverConn, err := dialLimited(t, ctx, conf, tc.clientLimit, tc.serverLimit)
			if err != nil {
				t.Fatalf("dial failed: %v", err)
			}
			t.Logf("handshake took %s; writes refused: client %d, server %d", time.Since(start), clientConn.refused.Load(), serverConn.refused.Load())
			if tc.clientLimit > 0 && clientConn.refused.Load() == 0 {
				t.Fatal("no client write was refused, so the test proves nothing")
			}
			if tc.serverLimit > 0 && serverConn.refused.Load() == 0 {
				t.Fatal("no server write was refused, so the test proves nothing")
			}
			if got := protocol.ByteCount(client.InitialPacketSize()); got != tc.wantClient {
				t.Fatalf("client InitialPacketSize %d, want %d", got, tc.wantClient)
			}
			if got := protocol.ByteCount(server.InitialPacketSize()); got != tc.wantServer {
				t.Fatalf("server InitialPacketSize %d, want %d", got, tc.wantServer)
			}
			// Path MTU discovery starts from the size in effect.
			if got := client.mtuDiscoverer.CurrentSize(); got != tc.wantClient {
				t.Fatalf("client MTU discovery starts at %d, want %d", got, tc.wantClient)
			}
			if got := protocol.ByteCount(client.maxPayloadSizeEstimate.Load()); got != estimateMaxPayloadSize(tc.wantClient) {
				t.Fatalf("client datagram payload estimate %d, want %d", got, estimateMaxPayloadSize(tc.wantClient))
			}
			refused := clientConn.refused.Load() + serverConn.refused.Load()
			checkEcho(t, ctx, client)
			if after := clientConn.refused.Load() + serverConn.refused.Load(); after != refused {
				t.Fatalf("%d more writes refused after the handshake: packets above the size in effect", after-refused)
			}
		})
	}
}

// Where the interface takes the configured size, nothing changes.
func TestHandshakeKeepsConfiguredPacketSize(t *testing.T) {
	for _, tc := range []struct {
		name  string
		conf  *Config
		limit int
		want  protocol.ByteCount
	}{
		{"default size", &Config{}, 0, protocol.InitialPacketSize},
		{"Chrome's size", &Config{ChromeParrot: true}, 0, chromeInitialPacketSize},
		{"Chrome's size on an interface that takes it", &Config{ChromeParrot: true}, chromeInitialPacketSize, chromeInitialPacketSize},
		{"configured size", &Config{InitialPacketSize: 1350}, 0, 1350},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, clientConn, _, err := dialLimited(t, ctx, tc.conf, tc.limit, 0)
			if err != nil {
				t.Fatalf("dial failed: %v", err)
			}
			if n := clientConn.refused.Load(); n != 0 {
				t.Fatalf("%d client writes refused", n)
			}
			if got := protocol.ByteCount(client.InitialPacketSize()); got != tc.want {
				t.Fatalf("client InitialPacketSize %d, want %d", got, tc.want)
			}
			checkEcho(t, ctx, client)
		})
	}
}

// As upstream: an InitialPacketSize the application set is the smallest size it accepts (a
// MASQUE tunnel needs room for 1280-byte inner packets), so "message too long" at that size fails
// the dial at once instead of at the handshake timeout.
func TestHandshakeKeepsExplicitPacketSizeAsFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, _, _, err := dialLimited(t, ctx, &Config{InitialPacketSize: protocol.InitialPacketSize}, mtu1280Payload, 0)
	if err == nil {
		client.CloseWithError(0, "")
		t.Fatal("dial succeeded below the configured packet size")
	}
	if !errors.Is(err, msgSizeErrno()) {
		t.Fatalf("dial failed with %v, want %v", err, msgSizeErrno())
	}
}

// Elarni: Conn.Config().InitialPacketSize read 0 when the application set none, where it used to
// read 1280, so a library that sized its congestion controller from the Config got no size. It
// reads the default again; only a size the application set is the floor.
func TestPopulatedConfigReadsTheDefaultInitialPacketSize(t *testing.T) {
	conf := populateConfig(&Config{})
	if conf.InitialPacketSize != protocol.InitialPacketSize {
		t.Fatalf("InitialPacketSize %d, want %d", conf.InitialPacketSize, protocol.InitialPacketSize)
	}
	if conf.initialPacketSizeIsFloor() {
		t.Fatal("the default size is a floor")
	}
	// The server populates the Config that GetConfigForClient returns once more.
	if populateConfig(conf).initialPacketSizeIsFloor() {
		t.Fatal("the default size is a floor once the Config is populated again")
	}
	if !populateConfig(&Config{InitialPacketSize: protocol.InitialPacketSize}).initialPacketSizeIsFloor() {
		t.Fatal("a size the application set is no floor")
	}
	changed := conf.Clone()
	changed.InitialPacketSize = 1350
	if !populateConfig(changed).initialPacketSizeIsFloor() {
		t.Fatal("a size the application set on a populated Config is no floor")
	}
	if conf := populateConfig(&Config{ChromeParrot: true}); conf.InitialPacketSize != chromeInitialPacketSize || conf.initialPacketSizeIsFloor() {
		t.Fatalf("with ChromeParrot: InitialPacketSize %d, floor %t; want %d, no floor", conf.InitialPacketSize, conf.initialPacketSizeIsFloor(), chromeInitialPacketSize)
	}
}

// seedRecordingHandler records the seed SetCongestionControl passes on.
type seedRecordingHandler struct {
	ackhandler.SentPacketHandler
	seed protocol.ByteCount
}

func (h *seedRecordingHandler) SetCongestionControl(_ congestion.CongestionControl, seed protocol.ByteCount) {
	h.seed = seed
}

// Elarni: a controller is seeded with a size InitialPacketSize returned, before or after the
// fallback to 1200 bytes; SetCongestionControl passes on the largest one, so that the controller
// is told every larger size and no smaller one.
func TestSetCongestionControlSeedIsTheLargestReportedSize(t *testing.T) {
	for _, tc := range []struct {
		name                string
		askBefore, askAfter bool
		want                protocol.ByteCount
	}{
		{"asked before the fallback", true, false, protocol.InitialPacketSize},
		{"asked before and after the fallback", true, true, protocol.InitialPacketSize},
		{"asked after the fallback", false, true, protocol.MinInitialPacketSize},
		{"not asked", false, false, protocol.MinInitialPacketSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &seedRecordingHandler{}
			c := &Conn{sentPacketHandler: h}
			c.currentInitialPacketSize.Store(uint32(protocol.InitialPacketSize))
			if tc.askBefore {
				c.InitialPacketSize()
			}
			c.currentInitialPacketSize.Store(uint32(protocol.MinInitialPacketSize))
			if tc.askAfter {
				c.InitialPacketSize()
			}
			c.SetCongestionControl(nil)
			if h.seed != tc.want {
				t.Fatalf("seed %d, want %d", h.seed, tc.want)
			}
		})
	}
}
