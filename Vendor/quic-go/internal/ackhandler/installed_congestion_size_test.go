package ackhandler

import (
	"fmt"
	"testing"

	"github.com/apernet/quic-go/congestion"
	"github.com/apernet/quic-go/internal/protocol"
	"github.com/apernet/quic-go/internal/utils"
)

// shrinkPanickingCC panics when told a smaller datagram size, as Hysteria's BBR and sing-quic's
// controllers do. Only the methods below are called.
type shrinkPanickingCC struct {
	congestion.CongestionControl
	size congestion.ByteCount
}

func (c *shrinkPanickingCC) SetRTTStatsProvider(congestion.RTTStatsProvider) {}

func (c *shrinkPanickingCC) SetMaxDatagramSize(s congestion.ByteCount) {
	if s < c.size {
		panic(fmt.Sprintf("decreased max datagram size from %d to %d", c.size, s))
	}
	c.size = s
}

// A connection that falls back to 1200 bytes on "message too long" (quic-go/quic-go#5911) must not
// tell a controller installed with SetCongestionControl a smaller size than it was seeded with: an
// early connection reaches the application, which installs one seeded with InitialPacketSize, before
// its first packet is written and refused.
func TestInstalledCongestionControlNeverLearnsASmallerSize(t *testing.T) {
	const initial protocol.ByteCount = 1280
	h := NewSentPacketHandler(0, initial, utils.NewRTTStats(), &utils.ConnectionStats{}, false, false, nil, protocol.PerspectiveClient, false, nil, utils.DefaultLogger).(*sentPacketHandler)
	cc := &shrinkPanickingCC{size: congestion.ByteCount(initial)}
	h.SetCongestionControl(cc)

	h.SetMaxDatagramSize(protocol.MinInitialPacketSize) // the fallback
	if h.maxDatagramSize != protocol.MinInitialPacketSize {
		t.Fatalf("connection's datagram size %d, want %d", h.maxDatagramSize, protocol.MinInitialPacketSize)
	}
	h.SetMaxDatagramSize(1252) // path MTU discovery, started again from 1200
	if cc.size != congestion.ByteCount(initial) {
		t.Fatalf("installed controller told %d, want it to keep %d", cc.size, initial)
	}
	h.SetMaxDatagramSize(1400)
	if cc.size != 1400 {
		t.Fatalf("installed controller told %d, want 1400 once discovery passes the initial size", cc.size)
	}
}
