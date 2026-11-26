package quic

import (
	"errors"
	"net"
	"sync/atomic"
	"time"

	"github.com/apernet/quic-go/internal/monotime"
	"github.com/apernet/quic-go/internal/protocol"
)

type sender interface {
	Send(p *packetBuffer, gsoSize uint16, ecn protocol.ECN)
	SendProbe(*packetBuffer, net.Addr, packetInfo)
	Run() error
	WouldBlock() bool
	Available() <-chan struct{}
	Close()
}

type queueEntry struct {
	buf     *packetBuffer
	gsoSize uint16
	ecn     protocol.ECN
}

type sendQueue struct {
	queue       chan queueEntry
	closeCalled chan struct{} // runStopped when Close() is called
	runStopped  chan struct{} // runStopped when the run loop returns
	available   chan struct{}
	conn        sendConn
	// Elarni: the connection's, which its direct writes during the handshake share.
	sendErrors *sendErrors
}

var _ sender = &sendQueue{}

const sendQueueCapacity = 8

func newSendQueue(conn sendConn, errs *sendErrors) sender {
	return &sendQueue{
		conn:        conn,
		runStopped:  make(chan struct{}),
		closeCalled: make(chan struct{}),
		available:   make(chan struct{}, 1),
		queue:       make(chan queueEntry, sendQueueCapacity),
		sendErrors:  errs,
	}
}

// Elarni: networkChangeGrace is how long, from the first failure, sends that fail with a lost
// interface, route or local address after the handshake lose only their packet, while a phone or
// laptop moves between Wi-Fi and mobile data. A send that goes through ends the streak. A failure
// past it closes the connection, as before, so that a route or address gone for good fails over
// at once instead of at the idle timeout. A variable so that tests can shorten it.
var networkChangeGrace = 5 * time.Second

// Elarni: sendErrors tells a failed send that loses only its packet, which loss recovery resends,
// from one that closes the connection. A connection's send queues and its direct writes during the
// handshake share one.
type sendErrors struct {
	// handshakeComplete is set by the connection once its handshake is complete. Before that, a
	// lost route or address still fails the dial at once.
	handshakeComplete atomic.Bool
	// networkChangeSince is the monotonic time of the first failure of the current streak of
	// sends that failed with a lost interface, route or local address; 0 while sends go through.
	networkChangeSince atomic.Int64
	// grace is networkChangeGrace when the connection was set up.
	grace time.Duration
}

// transient reports whether a send that failed with err loses only its packet: a full socket or
// interface queue (ENOBUFS), or a lost interface, route or local address once the handshake is
// complete, until the streak of such failures is grace old.
func (s *sendErrors) transient(err error) bool {
	if isNoBufferSpaceErr(err) {
		return true
	}
	if !s.handshakeComplete.Load() || !isNetworkChangeErr(err) {
		return false
	}
	now := int64(monotime.Now())
	since := s.networkChangeSince.Load()
	if since == 0 {
		s.networkChangeSince.Store(now)
		return true
	}
	return time.Duration(now-since) < s.grace
}

// sent ends a streak of failed sends.
func (s *sendErrors) sent() {
	if s.networkChangeSince.Load() != 0 {
		s.networkChangeSince.Store(0)
	}
}

// Send sends out a packet. It's guaranteed to not block.
// Callers need to make sure that there's actually space in the send queue by calling WouldBlock.
// Otherwise Send will panic.
func (h *sendQueue) Send(p *packetBuffer, gsoSize uint16, ecn protocol.ECN) {
	select {
	case h.queue <- queueEntry{buf: p, gsoSize: gsoSize, ecn: ecn}:
		// clear available channel if we've reached capacity
		if len(h.queue) == sendQueueCapacity {
			select {
			case <-h.available:
			default:
			}
		}
	case <-h.runStopped:
	default:
		panic("sendQueue.Send would have blocked")
	}
}

func (h *sendQueue) SendProbe(p *packetBuffer, addr net.Addr, info packetInfo) {
	h.conn.WriteTo(p.Data, addr, info)
}

func (h *sendQueue) WouldBlock() bool {
	return len(h.queue) == sendQueueCapacity
}

func (h *sendQueue) Available() <-chan struct{} {
	return h.available
}

func (h *sendQueue) Run() error {
	defer close(h.runStopped)
	var shouldClose bool
	for {
		if shouldClose && len(h.queue) == 0 {
			return nil
		}
		select {
		case <-h.closeCalled:
			h.closeCalled = nil // prevent this case from being selected again
			// make sure that all queued packets are actually sent out
			shouldClose = true
		case e := <-h.queue:
			if err := h.conn.Write(e.buf.Data, e.gsoSize, e.ecn); err != nil {
				// This additional check enables:
				// 1. Checking for "datagram too large" message from the kernel, as such,
				// 2. Path MTU discovery,and
				// 3. Eventual detection of loss PingFrame.
				// Elarni: so do a full socket or interface queue (ENOBUFS), and a route or local
				// address that is gone during a network switch, for a while once the handshake is
				// complete (sendErrors.transient); loss recovery resends the packet.
				var tooLarge *DatagramTooLargeError
				if !isSendMsgSizeErr(err) && !h.sendErrors.transient(err) && !errors.As(err, &tooLarge) {
					return err
				}
			} else {
				h.sendErrors.sent()
			}
			e.buf.Release()
			select {
			case h.available <- struct{}{}:
			default:
			}
		}
	}
}

func (h *sendQueue) Close() {
	close(h.closeCalled)
	// wait until the run loop returned
	<-h.runStopped
}
