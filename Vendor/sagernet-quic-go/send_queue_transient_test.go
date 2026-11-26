//go:build unix || windows

package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/quic-go/internal/monotime"
	"github.com/sagernet/quic-go/internal/protocol"
)

// failingSendConn fails every Write with err; the send queue calls nothing else on it.
type failingSendConn struct {
	sendConn
	err    error
	writes chan struct{}
}

func (c *failingSendConn) Write([]byte, uint16, protocol.ECN) error {
	c.writes <- struct{}{}
	return c.err
}

func sendErr(errno error) error {
	return &net.OpError{Op: "write", Net: "udp", Err: os.NewSyscallError("sendmsg", errno)}
}

// runSendQueue sends two packets through a send queue whose every write fails with errno, and
// that drops network-change errors for grace. It returns the error that stopped the queue, or
// nil when the queue dropped both errors and closed.
func runSendQueue(t *testing.T, errno error, handshakeComplete bool, grace time.Duration) error {
	t.Helper()
	c := &failingSendConn{err: sendErr(errno), writes: make(chan struct{}, 2)}
	errs := &sendErrors{grace: grace}
	errs.handshakeComplete.Store(handshakeComplete)
	q := newSendQueue(c, errs)
	done := make(chan error, 1)
	go func() { done <- q.Run() }()
	// The second write happens only once the first error was dropped.
	for range 2 {
		q.Send(getPacketBuffer(), 0, protocol.ECNNon)
		select {
		case <-c.writes:
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("packet not written")
		}
	}
	q.Close()
	return <-done
}

// ENOBUFS is upstream's (e86a8a79): it drops the packet with or without a handshake.
func TestSendQueueDropsNoBufferSpaceErrors(t *testing.T) {
	for _, handshakeComplete := range []bool{false, true} {
		if err := runSendQueue(t, noBufferSpaceErrno, handshakeComplete, 0); err != nil {
			t.Fatalf("handshake complete %t: send queue stopped: %v", handshakeComplete, err)
		}
	}
}

// Elarni: an interface, route or local address that is gone during a switch between Wi-Fi and
// cellular failed one send, and the send queue returned the error, which closed the connection.
func TestSendQueueDropsNetworkChangeErrorsAfterHandshake(t *testing.T) {
	for _, errno := range networkChangeErrnos {
		t.Run(errno.Error(), func(t *testing.T) {
			if err := runSendQueue(t, errno, true, networkChangeGrace); err != nil {
				t.Fatalf("send queue stopped: %v", err)
			}
		})
	}
}

// Elarni: a route that stays gone failed every send, and each was dropped, so the connection
// only ended at the idle timeout, 30 s or more later; a failover waited that long. Once a streak
// of failed sends outlasts the grace, the next failure stops the send queue as before.
func TestSendQueueStopsWhenNetworkChangeErrorsOutlastTheGrace(t *testing.T) {
	for _, errno := range networkChangeErrnos {
		t.Run(errno.Error(), func(t *testing.T) {
			if err := runSendQueue(t, errno, true, 0); !errors.Is(err, errno) {
				t.Fatalf("send queue stopped with %v, want %v", err, errno)
			}
		})
	}
}

func TestSendErrorsStreakEndsAfterTheGrace(t *testing.T) {
	err := sendErr(networkChangeErrnos[0])
	errs := &sendErrors{} // no grace: only the first failure of a streak is dropped
	errs.handshakeComplete.Store(true)
	if !errs.transient(err) {
		t.Fatal("first failure of a streak not dropped")
	}
	if errs.transient(err) {
		t.Fatal("failure past the grace dropped")
	}
	if !errs.transient(sendErr(noBufferSpaceErrno)) {
		t.Fatal("ENOBUFS past the grace not dropped")
	}
	errs.sent()
	if !errs.transient(err) {
		t.Fatal("first failure after a successful send not dropped")
	}
	errs.grace = time.Hour
	for range 10 {
		if !errs.transient(err) {
			t.Fatal("failure within the grace not dropped")
		}
	}
}

// Before the handshake completes, the same errors mean the server cannot be reached from this
// host at all (an IPv6 server address without an IPv6 route), so the dial fails at once.
func TestSendQueueStopsOnNetworkChangeErrorsBeforeHandshake(t *testing.T) {
	for _, errno := range networkChangeErrnos {
		t.Run(errno.Error(), func(t *testing.T) {
			if err := runSendQueue(t, errno, false, networkChangeGrace); !errors.Is(err, errno) {
				t.Fatalf("send queue stopped with %v, want %v", err, errno)
			}
		})
	}
}

func TestSendQueueStopsOnOtherSendErrors(t *testing.T) {
	if err := runSendQueue(t, net.ErrClosed, true, networkChangeGrace); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("send queue stopped with %v, want %v", err, net.ErrClosed)
	}
}

// flakyPacketConn fails as many writes as fail holds with err. It hides the socket's system-call
// interface, so quic-go sends every packet through WriteTo.
type flakyPacketConn struct {
	net.PacketConn
	err    error
	fail   atomic.Int32
	failed atomic.Int32
}

func (c *flakyPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.fail.Add(-1) >= 0 {
		c.failed.Add(1)
		return 0, &net.OpError{Op: "write", Net: "udp", Addr: addr, Err: os.NewSyscallError("sendto", c.err)}
	}
	return c.PacketConn.WriteTo(p, addr)
}

// dialFlaky dials a loopback echo server through a flakyPacketConn whose writes fail with errno.
func dialFlaky(t *testing.T, ctx context.Context, errno error, failFirst int32) (*Conn, *flakyPacketConn, error) {
	t.Helper()
	ln, err := ListenAddr("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}, NextProtos: []string{"t"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go func() {
				str, err := conn.AcceptStream(context.Background())
				if err == nil {
					io.Copy(str, str)
				}
			}()
		}
	}()
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	pc := &flakyPacketConn{PacketConn: socket, err: errno}
	pc.fail.Store(failFirst)
	// A handshake timeout far past the context's deadline: a dial that waited for it fails the test.
	conn, err := Dial(ctx, pc, ln.Addr(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"t"}}, &Config{HandshakeIdleTimeout: time.Minute})
	return conn, pc, err
}

// Elarni: a dial whose packets cannot leave the host fails with the system's error at once,
// not at the handshake timeout, so a failover or "Test all" moves on to the next server.
func TestDialWithoutARouteFailsAtOnce(t *testing.T) {
	errno := networkChangeErrnos[0]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := dialFlaky(t, ctx, errno, 1<<30)
	if err == nil {
		conn.CloseWithError(0, "")
		t.Fatal("dial succeeded without a route")
	}
	if !errors.Is(err, errno) {
		t.Fatalf("dial failed with %v, want %v", err, errno)
	}
}

// echoPing sends "ping" on str and reads the echo.
func echoPing(t *testing.T, str *Stream) {
	t.Helper()
	if _, err := str.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	str.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply := make([]byte, 4)
	if _, err := io.ReadFull(str, reply); err != nil {
		t.Fatalf("no echo: %v", err)
	}
	if string(reply) != "ping" {
		t.Fatalf("echo %q, want %q", reply, "ping")
	}
}

// Elarni: once the handshake is complete, a few sends that fail while the route is gone only
// lose those packets; the connection resends them and carries on.
func TestConnSurvivesNetworkChangeAfterHandshake(t *testing.T) {
	errno := networkChangeErrnos[0]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, pc, err := dialFlaky(t, ctx, errno, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "")
	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// One round trip first: Dial can return before the client's Finished left, and failing it
	// would leave the test to the Handshake-space probe timers.
	echoPing(t, str)
	pc.fail.Store(3)
	echoPing(t, str)
	if failed := pc.failed.Load(); failed != 3 {
		t.Fatalf("%d sends failed, want 3", failed)
	}
	if err := context.Cause(conn.Context()); err != nil {
		t.Fatalf("connection closed: %v", err)
	}
}

// Elarni: a connection whose sends keep failing (the route stays gone) closes with the system's
// error once the streak of failed sends outlasts the grace; it used to wait for the idle timeout.
func TestConnClosesWhenNetworkChangeOutlastsTheGrace(t *testing.T) {
	grace := networkChangeGrace
	networkChangeGrace = 0
	t.Cleanup(func() { networkChangeGrace = grace })
	errno := networkChangeErrnos[0]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, pc, err := dialFlaky(t, ctx, errno, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "")
	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	echoPing(t, str)
	pc.fail.Store(1 << 30)
	if _, err := str.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.Context().Done():
	case <-ctx.Done():
		t.Fatalf("connection still open after %d failed sends", pc.failed.Load())
	}
	if err := context.Cause(conn.Context()); !errors.Is(err, errno) {
		t.Fatalf("connection closed with %v, want %v", err, errno)
	}
}

// Elarni: until a packet of the initial size was tried, the run loop writes every packet itself
// (quic-go/quic-go#5911). When that try failed for another reason than "message too long" (a full
// socket buffer), the run loop kept writing, so receiving and timers waited behind each write.
// Any try of the initial size hands the writes back to the send queue.
func TestDirectWritesEndAfterTheFirstPacketOfTheInitialSize(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		size      int
		wantTried bool
	}{
		{"written", nil, int(protocol.InitialPacketSize), true},
		{"refused by a full socket buffer", sendErr(noBufferSpaceErrno), int(protocol.InitialPacketSize), true},
		{"smaller packet written", nil, int(protocol.InitialPacketSize) - 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Conn{conn: &failingSendConn{err: tc.err, writes: make(chan struct{}, 1)}}
			c.currentInitialPacketSize.Store(uint32(protocol.InitialPacketSize))
			buf := getPacketBuffer()
			buf.Data = buf.Data[:tc.size]
			c.writeUntilInitialPacketSizeTried(buf, protocol.ECNNon, monotime.Now())
			if c.initialPacketSizeTried != tc.wantTried {
				t.Fatalf("initial packet size tried %t, want %t", c.initialPacketSizeTried, tc.wantTried)
			}
		})
	}
}
