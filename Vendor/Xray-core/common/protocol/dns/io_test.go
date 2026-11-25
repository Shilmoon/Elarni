package dns_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/protocol/dns"
)

// The DNS outbound answers each hijacked query in its own goroutine over one TCP writer: every
// message has to reach the stream whole, right after its own length.
func TestTCPWriterKeepsConcurrentMessagesWhole(t *testing.T) {
	var out bytes.Buffer
	w := &dns.TCPWriter{Writer: buf.NewWriter(&out)}
	const writers, each = 8, 200
	want := map[string]int{}
	var wg sync.WaitGroup
	for i := range writers {
		for j := range each {
			want[fmt.Sprintf("answer %d of writer %d %s", j, i, bytes.Repeat([]byte{'x'}, i*37))]++
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range each {
				message := fmt.Sprintf("answer %d of writer %d %s", j, i, bytes.Repeat([]byte{'x'}, i*37))
				if err := w.WriteMessage(buf.FromBytes([]byte(message))); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()

	stream := out.Bytes()
	for len(stream) > 0 {
		if len(stream) < 2 {
			t.Fatalf("%d bytes left after the last message", len(stream))
		}
		size := int(binary.BigEndian.Uint16(stream))
		if len(stream) < 2+size {
			t.Fatalf("a message of %d bytes with %d left", size, len(stream)-2)
		}
		message := string(stream[2 : 2+size])
		if want[message] == 0 {
			t.Fatalf("a message that was not written, or not whole: %q", message)
		}
		want[message]--
		stream = stream[2+size:]
	}
	for message, left := range want {
		if left != 0 {
			t.Fatalf("%q missing %d times", message, left)
		}
	}
}
