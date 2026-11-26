package xdns

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet/finalmask"
)

// tcpAnswerTimeout bounds the wait of a worker for the answer to its query. Elarni: a resolver that drops a
// query (on an upstream timeout, say) took the worker out for good, and with every worker taken Send waited
// for ever. The xdns server holds a query for up to maxResponseDelay
const tcpAnswerTimeout = 5 * time.Second

// TCPResolver sends each query on a connection of one of its workers, over DNS's 2-byte length framing,
// and reads its answer there before the worker takes the next one.
type TCPResolver struct {
	dest   net.Destination
	dialer *finalmask.Dialer
	// timeout is how long a worker waits for an answer: then its connection is closed (Elarni)
	timeout time.Duration

	udpAddr *net.UDPAddr
	readCh  chan []byte
	sendCh  chan []byte
	closeCh chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	// conns are the connections of the workers, which Close closes to end a wait for an answer (Elarni)
	conns map[net.Conn]struct{}
}

func NewTCPResolver(config *ResolverProto, dialer *finalmask.Dialer) (Resolver, error) {
	return newTCPResolver(config, dialer, tcpAnswerTimeout)
}

func newTCPResolver(config *ResolverProto, dialer *finalmask.Dialer, timeout time.Duration) (*TCPResolver, error) {
	// Elarni: without a worker no query would be taken, and Send would wait until Close
	if config.Worker < 1 {
		return nil, errors.New("invalid worker")
	}
	dest, err := net.ParseDestination("tcp:" + config.Addr)
	if err != nil {
		return nil, err
	}
	r := &TCPResolver{
		dest:    dest,
		dialer:  dialer,
		timeout: timeout,
		readCh:  make(chan []byte),
		sendCh:  make(chan []byte),
		closeCh: make(chan struct{}),
		conns:   make(map[net.Conn]struct{}),
	}
	// Elarni: the workers are counted before NewTCPResolver returns, so that Close waits for every one
	for range config.Worker {
		r.wg.Add(1)
		go r.worker()
	}
	return r, nil
}

func (r *TCPResolver) closed() bool {
	select {
	case <-r.closeCh:
		return true
	default:
		return false
	}
}

func (r *TCPResolver) setaddr(addr net.Addr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch v := addr.(type) {
	case *net.TCPAddr:
		r.udpAddr = &net.UDPAddr{IP: v.IP, Port: v.Port}
	case *net.UDPAddr:
		r.udpAddr = v
	default:
		panic(v)
	}
}

// track adds conn to the connections that Close closes, false if Close has come first
func (r *TCPResolver) track(conn net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed() {
		return false
	}
	r.conns[conn] = struct{}{}
	return true
}

// drop closes the connection of a worker and forgets it
func (r *TCPResolver) drop(conn *net.Conn) {
	r.mu.Lock()
	delete(r.conns, *conn)
	r.mu.Unlock()
	_ = (*conn).Close()
	*conn = nil
}

func (r *TCPResolver) read(reader io.Reader, p []byte) (l int, err error) {
	if _, err := io.ReadFull(reader, p[:2]); err != nil {
		return 0, err
	}
	l = int(p[0])<<8 | int(p[1])
	if l == 0 || l > 4096 {
		return 0, errors.New("err resp len ", l)
	}
	if _, err = io.ReadFull(reader, p[:l]); err != nil {
		return 0, err
	}
	return l, nil
}

func (r *TCPResolver) do(conn *net.Conn, buf *[4096 + 2]byte, p []byte) error {
	if *conn == nil {
		c, err := r.dialer.DialTCP(r.dest)
		if err != nil {
			return err
		}
		if !r.track(c) {
			_ = c.Close()
			return errors.New("closed")
		}
		*conn = c
		r.setaddr(c.RemoteAddr())
	}

	c := *conn
	// Elarni: closing the connection ends the wait as a read deadline would, also for the connection of a
	// dialerProxy, which may not keep one
	timer := time.AfterFunc(r.timeout, func() { _ = c.Close() })
	l := len(p)
	buf[0] = byte(l >> 8)
	buf[1] = byte(l)
	copy(buf[2:], p)
	_, err := c.Write(buf[:2+len(p)])
	if err != nil {
		timer.Stop()
		r.drop(conn)
		return err
	}

	b := pool4K.Get().([]byte)
	l, err = r.read(c, b)
	if !timer.Stop() {
		// the connection is closed, even if the answer came just in time
		r.drop(conn)
		if err != nil {
			err = errors.New("no answer within ", r.timeout).Base(err)
		}
	}
	if err != nil {
		if *conn != nil {
			r.drop(conn)
		}
		pool4K.Put(b[:cap(b)])
		return err
	}
	select {
	case <-r.closeCh:
		pool4K.Put(b[:cap(b)])
	case r.readCh <- b[:l]:
	}
	return nil
}

func (r *TCPResolver) worker() {
	defer r.wg.Done()

	var conn net.Conn
	defer func() {
		if conn != nil {
			r.drop(&conn)
		}
	}()
	var buf [4096 + 2]byte
	var p []byte
	for {
		select {
		case <-r.closeCh:
			return
		default:
			select {
			case <-r.closeCh:
				return
			case p = <-r.sendCh:
			}
		}
		if err := r.do(&conn, &buf, p); err != nil && !r.closed() {
			errors.LogErrorInner(context.Background(), err, "do err")
		}
	}
}

func (r *TCPResolver) Addr() *net.UDPAddr {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.udpAddr
}

func (r *TCPResolver) Read(p []byte) (n int, err error) {
	packet, ok := <-r.readCh
	if ok {
		n = copy(p, packet)
		pool4K.Put(packet[:cap(packet)])
		return n, nil
	}
	return 0, io.ErrClosedPipe
}

func (r *TCPResolver) Send(p []byte) {
	select {
	case <-r.closeCh:
		return
	default:
		b := make([]byte, len(p))
		copy(b, p)
		select {
		case <-r.closeCh:
			return
		case r.sendCh <- b:
		}
	}
}

// Close ends the workers and Read. Elarni: it closes the workers' connections and waits for the workers, as
// the resolver before them did with its one connection: a worker that waited for an answer never saw Close,
// so Read, and the xdns client above it, went on waiting after it
func (r *TCPResolver) Close() {
	r.mu.Lock()
	if r.closed() {
		r.mu.Unlock()
		return
	}
	close(r.closeCh)
	conns := r.conns
	r.conns = nil
	r.mu.Unlock()
	for conn := range conns {
		_ = conn.Close()
	}
	r.wg.Wait()
	close(r.readCh)
	// close(r.sendCh)
}
