package ackhandler

import (
	"fmt"
	"testing"

	"github.com/sagernet/quic-go/congestion"
	"github.com/sagernet/quic-go/internal/monotime"
	"github.com/sagernet/quic-go/internal/protocol"
	"github.com/sagernet/quic-go/internal/utils"
	"github.com/sagernet/quic-go/internal/wire"
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

func newSizeTestHandler(initial protocol.ByteCount, pers protocol.Perspective) *sentPacketHandler {
	return NewSentPacketHandler(0, initial, utils.NewRTTStats(), &utils.ConnectionStats{}, false, false, nil, pers, false, nil, utils.DefaultLogger).(*sentPacketHandler)
}

// A connection that falls back to 1200 bytes on "message too long" (quic-go/quic-go#5911) must not
// tell a controller installed with SetCongestionControl a smaller size than it was seeded with: an
// early connection reaches the application, which installs one seeded with InitialPacketSize, before
// its first packet is written and refused.
func TestInstalledCongestionControlNeverLearnsASmallerSize(t *testing.T) {
	const initial protocol.ByteCount = 1280
	h := newSizeTestHandler(initial, protocol.PerspectiveClient)
	cc := &shrinkPanickingCC{size: congestion.ByteCount(initial)}
	h.SetCongestionControl(cc, initial)

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

// Elarni: a controller installed after the fallback was seeded with 1200 bytes, so it must learn
// every size path MTU discovery confirms above that, including those up to the configured 1280.
// It was told only sizes above the configured size, and kept 1200 while 1252-byte packets left.
func TestInstalledCongestionControlLearnsEverySizeAboveItsSeed(t *testing.T) {
	h := newSizeTestHandler(1280, protocol.PerspectiveClient)
	h.SetMaxDatagramSize(protocol.MinInitialPacketSize) // the fallback, before the controller is installed
	cc := &shrinkPanickingCC{size: congestion.ByteCount(protocol.MinInitialPacketSize)}
	h.SetCongestionControl(cc, protocol.MinInitialPacketSize)

	h.SetMaxDatagramSize(1252)
	if cc.size != 1252 {
		t.Fatalf("installed controller told %d, want 1252", cc.size)
	}
}

// Elarni: the connection's own cubic sender takes the one decrease a connection makes, its
// fallback to 1200 bytes before the handshake is confirmed, and still panics on any other, as
// upstream's does, so that a later bug that shrinks the packets does not pass unnoticed.
func TestOwnCongestionControlTakesOnlyTheFallback(t *testing.T) {
	h := newSizeTestHandler(1280, protocol.PerspectiveClient)
	h.SetMaxDatagramSize(protocol.MinInitialPacketSize) // must not panic
	h.SetMaxDatagramSize(1252)                          // path MTU discovery, started again from 1200

	for _, tc := range []struct {
		name      string
		confirmed bool
		size      protocol.ByteCount
	}{
		{"to 1200 bytes after the handshake is confirmed", true, protocol.MinInitialPacketSize},
		{"to another size before the handshake is confirmed", false, 1250},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSizeTestHandler(1280, protocol.PerspectiveClient)
			if tc.confirmed {
				now := monotime.Now()
				h.DropPackets(protocol.EncryptionInitial, now)
				h.DropPackets(protocol.EncryptionHandshake, now)
			}
			defer func() {
				if recover() == nil {
					t.Fatalf("decrease from 1280 to %d did not panic", tc.size)
				}
			}()
			h.SetMaxDatagramSize(tc.size)
		})
	}
}

// Elarni: a packet the socket refused ("message too long") never left, so it must not count
// against the amplification limit; otherwise a server that falls back to 1200 bytes holds back
// its resend and the rest of its flight until the client sends again.
func TestRefusedPacketTakesNoAmplificationBudget(t *testing.T) {
	h := newSizeTestHandler(1280, protocol.PerspectiveServer)
	now := monotime.Now()
	h.ReceivedBytes(1280, now) // the client's Initial: three times that may be sent
	for range 3 {
		pn := h.PopPacketNumber(protocol.EncryptionInitial)
		h.SentPacket(now, pn, protocol.InvalidPacketNumber, nil, []Frame{{Frame: &wire.PingFrame{}}}, protocol.EncryptionInitial, protocol.ECNNon, 1280, false, false)
	}
	if mode := h.SendMode(now); mode != SendNone {
		t.Fatalf("send mode %s with the budget used up, want %s", mode, SendNone)
	}
	h.PacketRefused(1280, now)
	if mode := h.SendMode(now); mode == SendNone {
		t.Fatal("a refused packet still counts against the amplification limit")
	}
	h.PacketRefused(1<<20, now) // more than was sent: no underflow
	if h.bytesSent != 0 {
		t.Fatalf("%d bytes sent after refusing more than was sent, want 0", h.bytesSent)
	}
}
