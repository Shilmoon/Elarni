package xdns

import (
	"errors"
	gonet "net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet/finalmask"
	"github.com/xtls/xray-core/transport/internet/tls"
)

// refusingPacketConn is the packet conn of a dialerProxy whose tunnel takes nothing: every write fails, and a
// read waits until Close, whatever its deadline says.
type refusingPacketConn struct {
	once   sync.Once
	closed chan struct{}
}

func (c *refusingPacketConn) ReadFrom([]byte) (int, gonet.Addr, error) {
	<-c.closed
	return 0, nil, gonet.ErrClosed
}

func (c *refusingPacketConn) WriteTo([]byte, gonet.Addr) (int, error) {
	return 0, errors.New("refused")
}

func (c *refusingPacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *refusingPacketConn) LocalAddr() gonet.Addr {
	return &gonet.UDPAddr{IP: gonet.IPv4(127, 0, 0, 1), Port: 1}
}

func (c *refusingPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *refusingPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *refusingPacketConn) SetWriteDeadline(time.Time) error { return nil }

// A DoH resolver over HTTP/3 whose QUIC dial failed closed neither the packet conn it had dialed nor the QUIC
// transport over it. They are closed now, the packet conn first: the transport waits for its read loop,
// which this packet conn ends only by closing.
func TestDOHResolverClosesThePacketConnOfAFailedHTTP3Dial(t *testing.T) {
	conn := &refusingPacketConn{closed: make(chan struct{})}
	dialer := &finalmask.Dialer{DialUDP: func(net.Destination) (net.Conn, error) {
		return &net.PacketConnWrapper{PacketConn: conn, Dest: &gonet.UDPAddr{IP: gonet.IPv4(127, 0, 0, 1), Port: 8443}}, nil
	}}
	resolver, err := NewDOHResolver(&ResolverProto{
		Type:      "https",
		Addr:      "https://127.0.0.1:8443/dns-query",
		Worker:    1,
		TlsConfig: &tls.Config{ServerName: "fixture.test", NextProtocol: []string{"h3"}},
	}, dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()

	resolver.Send([]byte("query"))
	select {
	case <-conn.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the packet conn of the failed dial is still open")
	}
}
