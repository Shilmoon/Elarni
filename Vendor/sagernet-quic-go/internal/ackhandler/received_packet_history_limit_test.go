package ackhandler

import (
	"testing"

	"github.com/sagernet/quic-go/internal/protocol"
)

// Upstream quic-go PR #6308: past 64 ACK ranges the oldest range was dropped, but a late
// duplicate of a packet from it was taken as new and processed again, so a reordering path
// delivered a DATAGRAM (a Hysteria2 or TUIC UDP packet) to the application twice.
func TestReceivedPacketHistoryRejectsPacketsFromDiscardedRange(t *testing.T) {
	hist := newReceivedPacketHistory()
	for i := range protocol.MaxNumAckRanges {
		if !hist.ReceivedPacket(protocol.PacketNumber(2 * i)) {
			t.Fatalf("packet %d not new", 2*i)
		}
	}
	hist.ReceivedPacket(2*protocol.MaxNumAckRanges + 1000)
	if len(hist.ranges) != protocol.MaxNumAckRanges {
		t.Fatalf("tracking %d ranges, want %d", len(hist.ranges), protocol.MaxNumAckRanges)
	}
	if got := hist.ranges[0]; got != (interval{Start: 2, End: 2}) {
		t.Fatalf("oldest range %+v, want 2-2", got)
	}
	if !hist.IsPotentiallyDuplicate(0) {
		t.Error("packet 0 from the discarded range is not a potential duplicate")
	}
	if hist.ReceivedPacket(0) {
		t.Error("packet 0 from the discarded range was accepted again")
	}
	if !hist.ReceivedPacket(1) {
		t.Error("packet 1 from the gap after the discarded range was rejected")
	}
}
