//go:build cgo && darwin && elarni_shadowquic_static

package shadowquic

import "testing"

// The client linked into the program itself, the way the Apple apps and network extensions
// carry it: go test -tags elarni_shadowquic_static with libelarni_shadowquic.a in CGO_LDFLAGS.
func TestStaticallyLinkedClientCarriesTraffic(t *testing.T) {
	c := testClient(StartTestServer(t))
	echoThrough(t, c, tcpEcho(t), "static")
	client, ok := c.current.native.(*linked)
	if !ok {
		t.Fatalf("the outbound runs %T, not the linked client", c.current.native)
	}
	c.Close()
	if client.Running() {
		t.Fatal("the linked client runs after Close")
	}
}
