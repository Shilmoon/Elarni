package quic

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sagernet/quic-go/internal/ackhandler"
	"github.com/sagernet/quic-go/internal/handshake"
	"github.com/sagernet/quic-go/internal/monotime"
	"github.com/sagernet/quic-go/internal/protocol"
	"github.com/sagernet/quic-go/internal/utils"
	"github.com/sagernet/quic-go/internal/wire"
)

// freedQueueSender is a send queue that reports itself full right after the first packet and free
// again by the time the run loop looks: the queue wrote that packet in between.
type freedQueueSender struct {
	wouldBlock []bool // answers to WouldBlock, in order; false once they run out
	sent       int
}

func (s *freedQueueSender) Send(p *packetBuffer, _ uint16, _ protocol.ECN)      { s.sent++; p.Release() }
func (s *freedQueueSender) SendProbe(p *packetBuffer, _ net.Addr, _ packetInfo) { p.Release() }
func (s *freedQueueSender) Run() error                                          { return nil }
func (s *freedQueueSender) Available() <-chan struct{}                          { return nil }
func (s *freedQueueSender) Close()                                              {}

func (s *freedQueueSender) WouldBlock() bool {
	if len(s.wouldBlock) == 0 {
		return false
	}
	b := s.wouldBlock[0]
	s.wouldBlock = s.wouldBlock[1:]
	return b
}

// queuedPacker has packets left to pack (queued DATAGRAM frames, say), then nothing.
type queuedPacker struct {
	packer
	packets int
	calls   int
}

func (p *queuedPacker) AppendPacket(buf *packetBuffer, _ protocol.ByteCount, _ monotime.Time, _ protocol.Version) (shortHeaderPacket, error) {
	p.calls++
	if p.packets == 0 {
		return shortHeaderPacket{}, errNothingToPack
	}
	p.packets--
	buf.Data = append(buf.Data, make([]byte, 100)...)
	return shortHeaderPacket{
		PacketNumber: protocol.PacketNumber(p.calls),
		Frames:       []ackhandler.Frame{{Frame: &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 80)}}},
		Length:       100,
	}, nil
}

// sendAnySentPacketHandler lets everything be sent and keeps no record of it.
type sendAnySentPacketHandler struct {
	ackhandler.SentPacketHandler
}

func (sendAnySentPacketHandler) SentPacket(monotime.Time, protocol.PacketNumber, protocol.PacketNumber, []ackhandler.StreamFrame, []ackhandler.Frame, protocol.EncryptionLevel, protocol.ECN, protocol.ByteCount, bool, bool) {
}
func (sendAnySentPacketHandler) SendMode(monotime.Time) ackhandler.SendMode {
	return ackhandler.SendAny
}
func (sendAnySentPacketHandler) ECNMode(bool) protocol.ECN              { return protocol.ECNNon }
func (sendAnySentPacketHandler) GetLossDetectionTimeout() monotime.Time { return 0 }
func (sendAnySentPacketHandler) TimeUntilSend() monotime.Time           { return 0 }

type nopConnRunner struct{}

func (nopConnRunner) Add(protocol.ConnectionID, packetHandler) bool                    { return true }
func (nopConnRunner) Remove(protocol.ConnectionID)                                     {}
func (nopConnRunner) ReplaceWithClosed([]protocol.ConnectionID, []byte, time.Duration) {}
func (nopConnRunner) AddResetToken(protocol.StatelessResetToken, packetHandler)        {}
func (nopConnRunner) RemoveResetToken(protocol.StatelessResetToken)                    {}

// capabilitiesSendConn only answers what the connection asks it; the fake send queue does the writing.
type capabilitiesSendConn struct {
	sendConn
	gso bool
}

func (c capabilitiesSendConn) capabilities() connCapabilities { return connCapabilities{GSO: c.gso} }
func (capabilitiesSendConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4321}
}
func (capabilitiesSendConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
}

// Upstream quic-go#6189 (fix 5a6c94ec): when the send queue was full after a packet and had
// written it by the time the run loop checked, the run loop waited for the next received packet
// or timer, so queued DATAGRAM frames (game and call traffic over Hysteria2 and TUIC) were late.
func TestConnectionSendQueueFreedBeforeCheck(t *testing.T) {
	for _, gso := range []bool{false, true} {
		t.Run(map[bool]string{false: "without GSO", true: "with GSO"}[gso], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(context.Background())
				conn := newConnection(
					ctx,
					cancel,
					capabilitiesSendConn{gso: gso},
					nopConnRunner{},
					protocol.ParseConnectionID([]byte{1, 2, 3, 4, 5, 6}),
					nil,
					protocol.ConnectionID{},
					protocol.ConnectionID{},
					protocol.ParseConnectionID([]byte{7, 8, 9, 10, 11, 12}),
					&protocol.DefaultConnectionIDGenerator{},
					newStatelessResetter(nil),
					populateConfig(&Config{DisablePathMTUDiscovery: true}),
					&tls.Config{},
					handshake.NewTokenGenerator(handshake.TokenProtectorKey{}),
					false,
					time.Second,
					nil,
					utils.DefaultLogger,
					protocol.Version1,
				).Conn
				// The run loop checks before sending (free), the send loop stops after the first
				// packet (full), and from then on the queue is free.
				sender := &freedQueueSender{wouldBlock: []bool{false, true}}
				packer := &queuedPacker{packets: 1}
				conn.sendQueue = sender
				conn.packer = packer
				conn.sentPacketHandler = sendAnySentPacketHandler{conn.sentPacketHandler}
				conn.handshakeComplete = true
				conn.handshakeConfirmed = true

				errChan := make(chan error, 1)
				go func() { errChan <- conn.run() }()
				conn.scheduleSending()
				synctest.Wait()
				sent, calls := sender.sent, packer.calls

				conn.destroy(nil)
				synctest.Wait()
				select {
				case err := <-errChan:
					if err != nil {
						t.Fatalf("run returned %v", err)
					}
				default:
					t.Fatal("run did not return")
				}

				if sent != 1 {
					t.Fatalf("%d packets sent, want 1", sent)
				}
				// Sending goes on without another scheduleSending, a received packet or a timer:
				// the packer is asked again and finds nothing more.
				if calls != 2 {
					t.Fatalf("packer asked %d times, want 2: the run loop stopped sending while the queue was free", calls)
				}
			})
		})
	}
}
