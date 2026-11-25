package buf_test

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
)

// takesNothing reports every write as done without an error, yet takes no byte.
type takesNothing struct{ calls int }

func (w *takesNothing) Write([]byte) (int, error) {
	w.calls++
	return 0, nil
}

// A writer that takes nothing and reports no error must end the write with an error,
// not be asked again forever: one buffer goes through WriteAllBytes, more through net.Buffers.
func TestBufferToBytesWriterStopsOnZeroByteWrites(t *testing.T) {
	for _, buffers := range []int{1, 2} {
		target := &takesNothing{}
		w := &buf.BufferToBytesWriter{Writer: target}
		mb := make(buf.MultiBuffer, 0, buffers)
		for range buffers {
			mb = append(mb, buf.FromBytes([]byte("payload")))
		}
		done := make(chan error, 1)
		go func() { done <- w.WriteMultiBuffer(mb) }()
		select {
		case err := <-done:
			if err != io.ErrShortWrite {
				t.Fatalf("%d buffers: error %v, want %v", buffers, err, io.ErrShortWrite)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%d buffers: the write is still asking a writer that takes nothing", buffers)
		}
		if target.calls > buffers {
			t.Fatalf("%d buffers: %d writes before giving up", buffers, target.calls)
		}
	}
}

// A write larger than the free buffer space flushes and continues. The Trojan
// client writes its request header and then the first payload this way.
func TestBufferedWriterSpillsLargeWrite(t *testing.T) {
	var out bytes.Buffer
	w := buf.NewBufferedWriter(buf.NewWriter(&out))
	header := []byte("header")
	payload := bytes.Repeat([]byte{7}, buf.Size)
	if _, err := w.Write(header); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("Write(%d bytes) = %d, %v", len(payload), n, err)
	}
	if err := w.SetBuffered(false); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), append(header, payload...)) {
		t.Fatalf("wrote %d bytes, want %d", out.Len(), len(header)+len(payload))
	}
}
