//go:build cgo && darwin && !elarni_shadowquic_static

package shadowquic

import "testing"

// The client as part of the process, the way the apps and the network extensions run it.
func TestLinkedClientCarriesTraffic(t *testing.T) {
	if _, err := startLinked(nil); err != errNotLinked {
		t.Fatalf("a client is linked before one was loaded: %v", err)
	}
	if !LoadLibrary(Artifact(t, "libelarni_shadowquic.dylib")) {
		t.Fatal("the client library did not load")
	}
	c := testClient(StartTestServer(t))
	echo := tcpEcho(t)
	echoThrough(t, c, echo, "linked")
	client, ok := c.current.native.(*linked)
	if !ok {
		t.Fatalf("the outbound runs %T, not the linked client", c.current.native)
	}
	if !client.Running() {
		t.Fatal("the linked client is not running")
	}
	c.Close()
	if client.Running() {
		t.Fatal("the linked client runs after Close")
	}
	if _, err := startLinked([]byte(`{}`)); err == nil || err == errNotLinked {
		t.Fatalf("a refused description: %v", err)
	}
}
