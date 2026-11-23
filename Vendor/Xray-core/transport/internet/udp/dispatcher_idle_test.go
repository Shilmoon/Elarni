package udp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol/udp"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

type silentPeer struct{ flows atomic.Int32 }

func (*silentPeer) Type() interface{} { return routing.DispatcherType() }
func (*silentPeer) Start() error      { return nil }
func (*silentPeer) Close() error      { return nil }
func (p *silentPeer) Dispatch(context.Context, net.Destination) (*transport.Link, error) {
	p.flows.Add(1)
	uplinkReader, uplinkWriter := pipe.New(pipe.WithoutSizeLimit())
	downlinkReader, downlinkWriter := pipe.New(pipe.WithoutSizeLimit())
	go func() {
		for {
			if _, err := uplinkReader.ReadMultiBuffer(); err != nil {
				downlinkWriter.Close()
				return
			}
		}
	}()
	return &transport.Link{Reader: downlinkReader, Writer: uplinkWriter}, nil
}
func (*silentPeer) DispatchLink(context.Context, net.Destination, *transport.Link) error { return nil }

// A flow that only sends stays one flow while it sends, and ends after the idle time without it.
// A caller whose datagrams each ask for an answer (the DNS client) keeps its flow by replies only:
// queries to a relay that went silent move to a new flow after the idle time.
func TestSendingKeepsAFlowWithoutReplies(t *testing.T) {
	defer func(previous time.Duration) { idleTimeout = previous }(idleTimeout)
	idleTimeout = 600 * time.Millisecond
	dest := net.UDPDestination(net.LocalHostIP, 9)
	sender, asker := &silentPeer{}, &silentPeer{}
	sending := NewDispatcher(sender, func(context.Context, *udp.Packet) {})
	asking := NewDispatcher(asker, func(context.Context, *udp.Packet) {}).KeptByReplies()
	send := func(dispatcher *Dispatcher) {
		payload := buf.New()
		payload.WriteString("x")
		dispatcher.Dispatch(context.Background(), dest, payload)
	}
	// Twice the idle time of sending, with most of the idle time to spare between two sends.
	for start := time.Now(); time.Since(start) < 2*idleTimeout; time.Sleep(40 * time.Millisecond) {
		send(sending)
		send(asking)
	}
	if flows := sender.flows.Load(); flows != 1 {
		t.Fatalf("sending for twice the idle time used %d flows, want 1", flows)
	}
	if flows := asker.flows.Load(); flows < 2 {
		t.Fatalf("asking without an answer for twice the idle time stayed on %d flow, want a new one", flows)
	}
	time.Sleep(2 * idleTimeout)
	send(sending)
	if flows := sender.flows.Load(); flows != 2 {
		t.Fatalf("a send after the idle time used flow %d, want a new one", flows)
	}
}
