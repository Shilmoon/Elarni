package quic

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagernet/quic-go/internal/monotime"
	"github.com/sagernet/quic-go/internal/protocol"
	"github.com/sagernet/quic-go/internal/utils"
	"github.com/sagernet/quic-go/internal/wire"
)

type blockedWriteSender struct {
	streamSender
	completed chan struct{}
}

func (s *blockedWriteSender) onHasStreamData(protocol.StreamID, *SendStream)                      {}
func (s *blockedWriteSender) onHasStreamControlFrame(protocol.StreamID, streamControlFrameGetter) {}
func (s *blockedWriteSender) onStreamCompleted(protocol.StreamID)                                 { close(s.completed) }

// Upstream quic-go#6136 (fix 254a5393): a Write blocked when the peer stopped the stream put
// its data into a new frame that is never sent, so the stream never completed and its
// MAX_STREAMS credit never came back; a Hysteria2 or TUIC connection then ran out of streams.
func TestSendStreamResetDuringBlockedWriteCompletes(t *testing.T) {
	const id protocol.StreamID = 4
	sender := &blockedWriteSender{completed: make(chan struct{})}
	rttStats := utils.NewRTTStats()
	connFC := newConnectionFlowController(protocol.MaxByteCount, protocol.MaxByteCount, nil, rttStats, utils.DefaultLogger)
	fc := newStreamFlowController(id, connFC, protocol.MaxByteCount, protocol.MaxByteCount, protocol.MaxByteCount, rttStats, utils.DefaultLogger)
	str := newSendStream(context.Background(), id, sender, fc, false)

	if _, err := str.Write(make([]byte, 1000)); err != nil { // buffered in the next frame
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := str.Write(make([]byte, 1000)); done <- err }() // does not fit next to it, blocks
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		str.mutex.Lock()
		blocked := len(str.dataForWriting) > 0
		str.mutex.Unlock()
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second Write did not block")
		}
	}

	str.handleStopSendingFrame(&wire.StopSendingFrame{StreamID: id})
	var streamErr *StreamError
	if err := <-done; !errors.As(err, &streamErr) {
		t.Fatalf("blocked Write returned %v, want the stream error", err)
	}
	f, ok, _ := str.getControlFrame(monotime.Now()) // RESET_STREAM
	if !ok {
		t.Fatal("no RESET_STREAM queued")
	}
	f.Handler.OnAcked(f.Frame)
	_ = str.Close()

	select {
	case <-sender.completed:
	default:
		t.Fatal("send stream never completed")
	}
}
