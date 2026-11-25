package quic

import (
	"bytes"
	"errors"
	"testing"

	"github.com/sagernet/quic-go/internal/protocol"
	"github.com/sagernet/quic-go/internal/qerr"
	"github.com/sagernet/quic-go/internal/wire"
)

// Upstream quic-go PR #6265: the handshake failed with CRYPTO_BUFFER_EXCEEDED once a server's
// first flight passed 16 KiB, even when every byte before it had been read. Long certificate
// chains and post-quantum key shares make such flights.
func TestCryptoStreamAcceptsLargeFlightReadInOrder(t *testing.T) {
	str := newCryptoStream()
	chunk := bytes.Repeat([]byte{'a'}, 1200)
	for offset := protocol.ByteCount(0); offset < 3*protocol.MaxCryptoStreamBufferSize; offset += protocol.ByteCount(len(chunk)) {
		if err := str.HandleCryptoFrame(&wire.CryptoFrame{Offset: offset, Data: chunk}); err != nil {
			t.Fatalf("frame at offset %d: %v", offset, err)
		}
		if data := str.GetCryptoData(); !bytes.Equal(data, chunk) {
			t.Fatalf("read %d bytes at offset %d, want %d", len(data), offset, len(chunk))
		}
	}
}

// Out-of-order data past the buffer after the read position is still refused.
func TestCryptoStreamLimitsOutOfOrderData(t *testing.T) {
	str := newCryptoStream()
	if err := str.HandleCryptoFrame(&wire.CryptoFrame{Data: []byte("foobar")}); err != nil {
		t.Fatal(err)
	}
	if data := str.GetCryptoData(); string(data) != "foobar" {
		t.Fatalf("read %q", data)
	}
	if err := str.HandleCryptoFrame(&wire.CryptoFrame{Offset: 6 + protocol.MaxCryptoStreamBufferSize - 5, Data: []byte("foo")}); err != nil {
		t.Fatal(err)
	}
	err := str.HandleCryptoFrame(&wire.CryptoFrame{Offset: 6 + protocol.MaxCryptoStreamBufferSize - 2, Data: []byte("bar")})
	if !errors.Is(err, &qerr.TransportError{ErrorCode: qerr.CryptoBufferExceeded}) {
		t.Fatalf("got %v, want CRYPTO_BUFFER_EXCEEDED", err)
	}
}
