package quic

import (
	"testing"

	"github.com/apernet/quic-go/internal/ackhandler"
	"github.com/apernet/quic-go/internal/monotime"
	"github.com/apernet/quic-go/internal/protocol"
	"github.com/apernet/quic-go/internal/utils"
	"github.com/apernet/quic-go/internal/wire"
)

// Upstream quic-go#5970 (6dfb89eb): frames are packed in the order ACK, control frames,
// retransmissions, DATAGRAM, STREAM. Before, a steady flow of DATAGRAM frames (game and call
// traffic over Hysteria2 and TUIC) filled each packet ahead of MAX_DATA, MAX_STREAM_DATA and lost
// frames, so flow control credit and retransmissions waited behind application data.

const orderTestMaxPayload protocol.ByteCount = 1000

type onceAckSource struct{ ack *wire.AckFrame }

func (s *onceAckSource) GetAckFrame(protocol.EncryptionLevel, monotime.Time, bool) *wire.AckFrame {
	ack := s.ack
	s.ack = nil
	return ack
}

// orderTestStream has left bytes of STREAM data to send.
type orderTestStream struct{ left protocol.ByteCount }

func (s *orderTestStream) popStreamFrame(maxBytes protocol.ByteCount, v protocol.Version) (ackhandler.StreamFrame, *wire.StreamDataBlockedFrame, bool) {
	f := &wire.StreamFrame{StreamID: 4, DataLenPresent: true}
	n := min(f.MaxDataLen(maxBytes, v), s.left)
	if n == 0 {
		return ackhandler.StreamFrame{}, nil, s.left > 0
	}
	f.Data = make([]byte, n)
	s.left -= n
	return ackhandler.StreamFrame{Frame: f}, nil, s.left > 0
}

func newOrderTestPacker(ack *wire.AckFrame) (*packetPacker, *framer) {
	fr := newFramer(newConnectionFlowController(1<<20, 1<<20, nil, utils.NewRTTStats(), utils.DefaultLogger))
	return &packetPacker{
		framer:              fr,
		acks:                &onceAckSource{ack: ack},
		datagramQueue:       newDatagramQueue(func() {}, utils.DefaultLogger),
		retransmissionQueue: newRetransmissionQueue(),
	}, fr
}

// fullPacketDatagram returns a DATAGRAM frame that fills a whole packet payload.
func fullPacketDatagram() *wire.DatagramFrame {
	f := &wire.DatagramFrame{DataLenPresent: true}
	f.Data = make([]byte, f.MaxDataLen(orderTestMaxPayload, protocol.Version1))
	return f
}

func composeOrderTestPacket(p *packetPacker) payload {
	return p.composeNextPacket(orderTestMaxPayload, false, true, monotime.Now(), protocol.Version1)
}

// checkPayloadLength checks the length the packer accounted against the frames it packed.
func checkPayloadLength(t *testing.T, pl payload) {
	t.Helper()
	var l protocol.ByteCount
	if pl.ack != nil {
		l += pl.ack.Length(protocol.Version1)
	}
	for _, f := range pl.frames {
		l += f.Frame.Length(protocol.Version1)
	}
	for _, f := range pl.streamFrames {
		l += f.Frame.Length(protocol.Version1)
	}
	if l != pl.length {
		t.Fatalf("payload length %d, frames add up to %d", pl.length, l)
	}
	if pl.length > orderTestMaxPayload {
		t.Fatalf("payload length %d over the maximum %d", pl.length, orderTestMaxPayload)
	}
}

func TestPackControlFramesAndRetransmissionsBeforeDatagram(t *testing.T) {
	for _, tc := range []struct {
		name  string
		queue func(*packetPacker, *framer) wire.Frame
	}{
		{"control frame", func(_ *packetPacker, fr *framer) wire.Frame {
			f := &wire.MaxDataFrame{MaximumData: 0x1337}
			fr.QueueControlFrame(f)
			return f
		}},
		{"retransmission", func(p *packetPacker, _ *framer) wire.Frame {
			f := &wire.MaxStreamDataFrame{StreamID: 4, MaximumStreamData: 0x1337}
			p.retransmissionQueue.addAppData(f)
			return f
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, fr := newOrderTestPacker(nil)
			datagram := fullPacketDatagram()
			if err := p.datagramQueue.Add(datagram); err != nil {
				t.Fatal(err)
			}
			queued := tc.queue(p, fr)

			pl := composeOrderTestPacket(p)
			checkPayloadLength(t, pl)
			if len(pl.frames) != 1 || pl.frames[0].Frame != queued {
				t.Fatalf("first packet has frames %v, want only the queued %T ahead of the DATAGRAM", frameTypes(pl.frames), queued)
			}
			if pl.frames[0].Handler == nil {
				t.Fatal("the queued frame has no handler, so it would not be retransmitted if lost")
			}
			// The DATAGRAM no longer fits, and waits for the next packet instead of being dropped.
			if p.datagramQueue.Peek() != datagram {
				t.Fatal("the DATAGRAM frame was dropped instead of waiting for the next packet")
			}

			pl = composeOrderTestPacket(p)
			checkPayloadLength(t, pl)
			if len(pl.frames) != 1 || pl.frames[0].Frame != datagram {
				t.Fatalf("second packet has frames %v, want the DATAGRAM", frameTypes(pl.frames))
			}
			if pl.frames[0].Handler != nil {
				t.Fatal("the DATAGRAM frame has a handler, but DATAGRAM frames are never retransmitted")
			}
			if p.datagramQueue.Peek() != nil {
				t.Fatal("the DATAGRAM frame is still queued")
			}
			// The next DATAGRAM frame has waited for no packet yet.
			if p.peekTimes != 0 {
				t.Fatalf("peekTimes %d after the DATAGRAM frame was sent, want 0", p.peekTimes)
			}
		})
	}
}

// Elarni: during loss recovery every packet was filled with retransmissions, so a DATAGRAM frame
// of a whole packet never fit beside them, and after DatagramFrameMaxPeekTimes packets it was
// dropped: UDP traffic over Hysteria2 and TUIC was lost while a stream recovered. Now it goes
// first in the next packet, and the retransmissions carry on after it.
func TestPackDatagramFirstAfterItWaitedTheMaxPeekTimes(t *testing.T) {
	p, _ := newOrderTestPacker(nil)
	datagram := fullPacketDatagram()
	if err := p.datagramQueue.Add(datagram); err != nil {
		t.Fatal(err)
	}
	p.retransmissionQueue.addAppData(&wire.CryptoFrame{Data: make([]byte, 20*orderTestMaxPayload)})

	for i := range DatagramFrameMaxPeekTimes {
		pl := composeOrderTestPacket(p)
		checkPayloadLength(t, pl)
		if len(pl.frames) == 0 {
			t.Fatalf("packet %d is empty", i+1)
		}
		for _, f := range pl.frames {
			if f.Frame == datagram {
				t.Fatalf("packet %d has the DATAGRAM frame beside the retransmissions, which fill it", i+1)
			}
		}
		if p.datagramQueue.Peek() != datagram {
			t.Fatalf("the DATAGRAM frame was dropped after %d packets", i+1)
		}
	}
	pl := composeOrderTestPacket(p)
	checkPayloadLength(t, pl)
	if len(pl.frames) == 0 || pl.frames[0].Frame != datagram {
		t.Fatalf("packet %d has frames %v, want the DATAGRAM first", DatagramFrameMaxPeekTimes+1, frameTypes(pl.frames))
	}
	if p.datagramQueue.Peek() != nil {
		t.Fatal("the DATAGRAM frame is still queued")
	}
	if p.peekTimes != 0 {
		t.Fatalf("peekTimes %d after the DATAGRAM frame was sent, want 0", p.peekTimes)
	}
	if !p.retransmissionQueue.HasData(protocol.Encryption1RTT) {
		t.Fatal("the retransmissions are gone")
	}
	pl = composeOrderTestPacket(p)
	if len(pl.frames) == 0 {
		t.Fatal("the retransmissions did not carry on in the next packet")
	}
}

func TestPackDatagramBeforeStreamData(t *testing.T) {
	t.Run("shares the packet", func(t *testing.T) {
		p, fr := newOrderTestPacker(nil)
		datagram := &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 600)}
		if err := p.datagramQueue.Add(datagram); err != nil {
			t.Fatal(err)
		}
		fr.AddActiveStream(4, &orderTestStream{left: 10000})

		pl := composeOrderTestPacket(p)
		checkPayloadLength(t, pl)
		if len(pl.frames) != 1 || pl.frames[0].Frame != datagram {
			t.Fatalf("packet has frames %v, want the DATAGRAM", frameTypes(pl.frames))
		}
		if len(pl.streamFrames) != 1 {
			t.Fatalf("packet has %d STREAM frames, want 1 in the rest of the packet", len(pl.streamFrames))
		}
	})

	t.Run("fills the packet", func(t *testing.T) {
		p, fr := newOrderTestPacker(nil)
		datagram := fullPacketDatagram()
		if err := p.datagramQueue.Add(datagram); err != nil {
			t.Fatal(err)
		}
		fr.AddActiveStream(4, &orderTestStream{left: 10000})

		pl := composeOrderTestPacket(p)
		checkPayloadLength(t, pl)
		if len(pl.frames) != 1 || pl.frames[0].Frame != datagram || len(pl.streamFrames) != 0 {
			t.Fatalf("packet has frames %v and %d STREAM frames, want only the DATAGRAM", frameTypes(pl.frames), len(pl.streamFrames))
		}
		if !fr.HasData() {
			t.Fatal("the STREAM data is gone")
		}
		pl = composeOrderTestPacket(p)
		if len(pl.streamFrames) == 0 {
			t.Fatal("the STREAM data was not sent in the next packet")
		}
	})
}

func TestPackDatagramThatDoesNotFit(t *testing.T) {
	tooLarge := func() *wire.DatagramFrame {
		return &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, orderTestMaxPayload)}
	}

	t.Run("in an otherwise empty packet it is dropped", func(t *testing.T) {
		p, _ := newOrderTestPacker(nil)
		if err := p.datagramQueue.Add(tooLarge()); err != nil {
			t.Fatal(err)
		}
		pl := composeOrderTestPacket(p)
		if len(pl.frames) != 0 || pl.ack != nil {
			t.Fatalf("packet has frames %v, want none", frameTypes(pl.frames))
		}
		if p.datagramQueue.Peek() != nil {
			t.Fatal("a DATAGRAM frame larger than the packet was kept")
		}
		if p.peekTimes != 0 {
			t.Fatalf("peekTimes %d after the DATAGRAM frame was dropped, want 0", p.peekTimes)
		}
	})

	t.Run("next to an ACK it waits", func(t *testing.T) {
		p, _ := newOrderTestPacker(&wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 3}}})
		datagram := tooLarge()
		if err := p.datagramQueue.Add(datagram); err != nil {
			t.Fatal(err)
		}
		pl := composeOrderTestPacket(p)
		checkPayloadLength(t, pl)
		if pl.ack == nil || len(pl.frames) != 0 {
			t.Fatalf("packet has ACK %v and frames %v, want only the ACK", pl.ack != nil, frameTypes(pl.frames))
		}
		if p.datagramQueue.Peek() != datagram {
			t.Fatal("the DATAGRAM frame was dropped from a packet that carried an ACK")
		}
	})

	t.Run("next to control frames it waits", func(t *testing.T) {
		p, fr := newOrderTestPacker(nil)
		datagram := tooLarge()
		if err := p.datagramQueue.Add(datagram); err != nil {
			t.Fatal(err)
		}
		control := &wire.MaxDataFrame{MaximumData: 0x1337}
		fr.QueueControlFrame(control)

		pl := composeOrderTestPacket(p)
		checkPayloadLength(t, pl)
		if len(pl.frames) != 1 || pl.frames[0].Frame != control {
			t.Fatalf("packet has frames %v, want the control frame", frameTypes(pl.frames))
		}
		if p.datagramQueue.Peek() != datagram {
			t.Fatal("the DATAGRAM frame was dropped from a packet that carried other frames")
		}
		// Alone in the next packet it still does not fit, and is dropped there.
		pl = composeOrderTestPacket(p)
		if len(pl.frames) != 0 {
			t.Fatalf("packet has frames %v, want none", frameTypes(pl.frames))
		}
		if p.datagramQueue.Peek() != nil {
			t.Fatal("a DATAGRAM frame larger than the packet was kept")
		}
	})

	t.Run("an ACK-only packet leaves it queued", func(t *testing.T) {
		p, _ := newOrderTestPacker(&wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 3}}})
		datagram := &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 100)}
		if err := p.datagramQueue.Add(datagram); err != nil {
			t.Fatal(err)
		}
		pl := p.composeNextPacket(orderTestMaxPayload, true, true, monotime.Now(), protocol.Version1)
		if pl.ack == nil || len(pl.frames) != 0 {
			t.Fatalf("ACK-only packet has ACK %v and frames %v", pl.ack != nil, frameTypes(pl.frames))
		}
		if p.datagramQueue.Peek() != datagram {
			t.Fatal("an ACK-only packet consumed the DATAGRAM frame")
		}
	})
}

func frameTypes(frames []ackhandler.Frame) []string {
	types := make([]string, 0, len(frames))
	for _, f := range frames {
		switch f.Frame.(type) {
		case *wire.DatagramFrame:
			types = append(types, "DATAGRAM")
		case *wire.MaxDataFrame:
			types = append(types, "MAX_DATA")
		case *wire.MaxStreamDataFrame:
			types = append(types, "MAX_STREAM_DATA")
		default:
			types = append(types, "other")
		}
	}
	return types
}
