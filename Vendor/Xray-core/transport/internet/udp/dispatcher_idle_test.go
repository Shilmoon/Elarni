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
func TestSendingKeepsAFlowWithoutReplies(t *testing.T) {
	defer func(previous time.Duration) { idleTimeout = previous }(idleTimeout)
	idleTimeout = 200 * time.Millisecond
	peer := &silentPeer{}
	dispatcher := NewDispatcher(peer, func(context.Context, *udp.Packet) {})
	dest := net.UDPDestination(net.LocalHostIP, 9)
	send := func() {
		payload := buf.New()
		payload.WriteString("x")
		dispatcher.Dispatch(context.Background(), dest, payload)
	}
	for sent := 0; sent < 12; sent++ {
		send()
		time.Sleep(50 * time.Millisecond)
	}
	if flows := peer.flows.Load(); flows != 1 {
		t.Fatalf("600 ms of sending with a 200 ms idle time used %d flows, want 1", flows)
	}
	time.Sleep(500 * time.Millisecond)
	send()
	if flows := peer.flows.Load(); flows != 2 {
		t.Fatalf("a send after the idle time used flow %d, want a new one", flows)
	}
}
