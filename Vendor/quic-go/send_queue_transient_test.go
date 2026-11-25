//go:build unix || windows

package quic

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/quic-go/internal/protocol"
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

// runSendQueue sends two packets through a send queue whose every write fails with errno. It
// returns the error that stopped the queue, or nil when the queue dropped both errors and closed.
func runSendQueue(t *testing.T, errno error, handshakeComplete bool) error {
	t.Helper()
	c := &failingSendConn{err: sendErr(errno), writes: make(chan struct{}, 2)}
	var complete atomic.Bool
	complete.Store(handshakeComplete)
	q := newSendQueue(c, &complete)
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

// Elarni: a full socket buffer (ENOBUFS on macOS and iOS) failed one send, and the send queue
// returned the error, which closed the whole connection.
func TestSendQueueDropsNoBufferSpaceErrors(t *testing.T) {
	for _, handshakeComplete := range []bool{false, true} {
		if err := runSendQueue(t, noBufferSpaceErrno, handshakeComplete); err != nil {
			t.Fatalf("handshake complete %t: send queue stopped: %v", handshakeComplete, err)
		}
	}
}

// Elarni: an interface, route or local address that is gone during a switch between Wi-Fi and
// cellular failed one send, and the send queue returned the error, which closed the connection.
func TestSendQueueDropsNetworkChangeErrorsAfterHandshake(t *testing.T) {
	for _, errno := range networkChangeErrnos {
		t.Run(errno.Error(), func(t *testing.T) {
			if err := runSendQueue(t, errno, true); err != nil {
				t.Fatalf("send queue stopped: %v", err)
			}
		})
	}
}

// Before the handshake completes, the same errors mean the server cannot be reached from this
// host at all (an IPv6 server address without an IPv6 route), so the dial fails at once.
func TestSendQueueStopsOnNetworkChangeErrorsBeforeHandshake(t *testing.T) {
	for _, errno := range networkChangeErrnos {
		t.Run(errno.Error(), func(t *testing.T) {
			if err := runSendQueue(t, errno, false); !errors.Is(err, errno) {
				t.Fatalf("send queue stopped with %v, want %v", err, errno)
			}
		})
	}
}

func TestSendQueueStopsOnOtherSendErrors(t *testing.T) {
	if err := runSendQueue(t, net.ErrClosed, true); !errors.Is(err, net.ErrClosed) {
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

func selfSigned(t *testing.T) tls.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), NotAfter: time.Now().Add(time.Hour)}, &x509.Certificate{}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
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
	pc.fail.Store(3)
	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := str.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	str.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply := make([]byte, 4)
	if _, err := io.ReadFull(str, reply); err != nil {
		t.Fatalf("no echo after the failed sends: %v", err)
	}
	if string(reply) != "ping" {
		t.Fatalf("echo %q, want %q", reply, "ping")
	}
	if failed := pc.failed.Load(); failed != 3 {
		t.Fatalf("%d sends failed, want 3", failed)
	}
	if err := context.Cause(conn.Context()); err != nil {
		t.Fatalf("connection closed: %v", err)
	}
}
