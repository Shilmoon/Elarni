package splithttp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/splithttp"
)

// A post size of 0 that reaches the dialer, where the config builder did not refuse it, fails the
// dial instead of ending the process with a panic.
func TestPacketUpDialRefusesPostSizeZero(t *testing.T) {
	addr, _, _ := countingXHTTP(t)
	settings := xhttpSettings(t, `{"path":"/x","mode":"packet-up"}`)
	settings.ScMaxEachPostBytes = &splithttp.RangeConfig{From: 0, To: 1000000}
	client := &internet.MemoryStreamConfig{ProtocolName: "splithttp", ProtocolSettings: settings}
	t.Cleanup(func() { internet.ReleaseTransport(client) })
	conn, err := splithttp.Dial(context.Background(), destination(t, addr), client)
	if err == nil {
		conn.Close()
		t.Fatal("the dial took a post size of 0")
	}
	if !strings.Contains(err.Error(), "scMaxEachPostBytes") {
		t.Fatalf("error %v, want one about scMaxEachPostBytes", err)
	}
}
