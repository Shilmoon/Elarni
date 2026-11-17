package shadowquic

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy/socks"
)

func tcpEcho(t testing.TB) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}

func testClient(server uint16) *Client {
	return &Client{config: &ClientConfig{Address: xnet.NewIPOrDomain(xnet.LocalHostIP), Port: uint32(server),
		Username: "user", Password: "pass", ServerName: "localhost", Alpn: []string{"h3"}, ZeroRtt: true}}
}

// echoThrough sends a message to the echo server through the running client.
func echoThrough(t testing.TB, c *Client, echo uint16, message string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, login, err := c.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	request := &protocol.RequestHeader{Version: 5, Command: protocol.RequestCommandTCP, Address: xnet.LocalHostIP, Port: xnet.Port(echo), User: login}
	if _, err := socks.ClientHandshake(request, conn, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	answer := make([]byte, len(message))
	if _, err := io.ReadFull(conn, answer); err != nil || string(answer) != message {
		t.Fatalf("echo through the client: %q %v", answer, err)
	}
}

// A client program that ended is replaced on the next flow.
func TestClientProgramIsReplacedAfterItEnds(t *testing.T) {
	onlyProgram(t)
	c := testClient(StartTestServer(t))
	defer c.Close()
	echo := tcpEcho(t)
	echoThrough(t, c, echo, "first")
	first := c.current
	child := first.native.(*program)
	if err := child.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-child.ended
	if first.native.Running() {
		t.Fatal("an ended program is reported as running")
	}
	echoThrough(t, c, echo, "second")
	if c.current == first || !c.current.native.Running() {
		t.Fatal("the ended program was not replaced")
	}
}

// Closing the outbound ends its client program, and later flows are refused.
func TestCloseEndsTheClientProgram(t *testing.T) {
	onlyProgram(t)
	c := testClient(StartTestServer(t))
	echoThrough(t, c, tcpEcho(t), "hello")
	child := c.current.native.(*program)
	address := c.current.address
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the client program still runs after Close")
	}
	if conn, err := net.DialTimeout("tcp4", address, time.Second); err == nil {
		conn.Close()
		t.Fatal("the listener still accepts after Close")
	}
	if _, _, err := c.connect(context.Background()); err == nil {
		t.Fatal("a closed outbound started a client")
	}
}

// A client that cannot start is reported, and not started again for every flow.
func TestFailedStartIsReportedAndNotRetriedAtOnce(t *testing.T) {
	onlyProgram(t)
	SetProgramPath(Artifact(t, executable("elarni-shadowquic")) + "-missing")
	defer UseClientProgram(t)
	c := testClient(1)
	defer c.Close()
	if _, _, err := c.connect(context.Background()); err == nil {
		t.Fatal("a missing client program was not reported")
	}
	first := c.failedAt
	if _, _, err := c.connect(context.Background()); err == nil || c.failedAt != first {
		t.Fatalf("the failed start was retried at once: %v", err)
	}
}

// The program refuses a description it does not understand and says why.
func TestProgramReportsARefusedDescription(t *testing.T) {
	UseClientProgram(t)
	_, err := startProgram([]byte(`{"server":"example.com:443"}`))
	if err == nil || !strings.Contains(err.Error(), "invalid configuration") {
		t.Fatalf("refused description: %v", err)
	}
}

// What the outbound sends is what the client reads: every field of the description.
func TestDescriptionCarriesEverySetting(t *testing.T) {
	description, err := json.Marshal(settings{Server: "192.0.2.1:443", ServerName: "example.com", Username: "u", Password: "p",
		ALPN: []string{"h3"}, CongestionControl: "cubic", ZeroRTT: true, UDPOverStream: true, InitialMTU: 1400, MinMTU: 1280,
		FwMark: 7, BindInterfaceIndex: 9, SocksUsername: "a", SocksPassword: "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"server":"192.0.2.1:443","serverName":"example.com","username":"u","password":"p","alpn":["h3"],"congestionControl":"cubic","zeroRtt":true,"udpOverStream":true,"initialMtu":1400,"minMtu":1280,"fwMark":7,"bindInterfaceIndex":9,"socksUsername":"a","socksPassword":"b"}`
	if string(description) != want {
		t.Fatalf("description:\n%s\nwant:\n%s", description, want)
	}
	// Unset options are left out, so the client keeps its own values.
	description, _ = json.Marshal(settings{Server: "192.0.2.1:443", ServerName: "example.com", Username: "u", Password: "p", SocksUsername: "a", SocksPassword: "b"})
	want = `{"server":"192.0.2.1:443","serverName":"example.com","username":"u","password":"p","zeroRtt":false,"udpOverStream":false,"socksUsername":"a","socksPassword":"b"}`
	if string(description) != want {
		t.Fatalf("description:\n%s\nwant:\n%s", description, want)
	}
}

// The server address is resolved by the core; a loopback socket has no binding.
func TestLocateResolvesTheServerWithoutABinding(t *testing.T) {
	c := &Client{config: &ClientConfig{Address: xnet.NewIPOrDomain(xnet.DomainAddress("localhost")), Port: 4433}}
	server, bound, err := c.locate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !server.Addr().IsLoopback() || server.Port() != 4433 || bound != (binding{}) {
		t.Fatalf("located %v with binding %+v", server, bound)
	}
}
