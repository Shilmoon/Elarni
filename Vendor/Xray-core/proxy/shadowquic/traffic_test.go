package shadowquic_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
	"github.com/xtls/xray-core/proxy/shadowquic"
)

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func tcpEcho(t *testing.T) int {
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
	return listener.Addr().(*net.TCPAddr).Port
}

func udpEcho(t *testing.T) int {
	t.Helper()
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	go func() {
		packet := make([]byte, 4096)
		for {
			n, from, err := socket.ReadFromUDP(packet)
			if err != nil {
				return
			}
			socket.WriteToUDP(packet[:n], from)
		}
	}()
	return socket.LocalAddr().(*net.UDPAddr).Port
}

// startCore runs a core with a SOCKS5 inbound and the ShadowQUIC outbound and returns
// the inbound's port.
func startCore(t *testing.T, settings string) int {
	t.Helper()
	inbound := freePort(t)
	config, err := serial.DecodeJSONConfig(strings.NewReader(fmt.Sprintf(`{
		"log": {"loglevel": "warning"},
		"inbounds": [{"listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"udp": true}}],
		"outbounds": [{"tag": "proxy", "protocol": "shadowquic", "settings": %s}]
	}`, inbound, settings)))
	if err != nil {
		t.Fatal(err)
	}
	built, err := config.Build()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := core.New(built)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { instance.Close() })
	return inbound
}

// socks opens a SOCKS5 session without a login and sends the command for 127.0.0.1:port.
func socks(t *testing.T, inbound int, command byte, port int) (net.Conn, int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	conn.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
		t.Fatalf("socks greeting: %v %v", reply, err)
	}
	conn.Write([]byte{5, command, 0, 1, 127, 0, 0, 1, byte(port >> 8), byte(port)})
	answer := make([]byte, 10)
	if _, err := io.ReadFull(conn, answer); err != nil || answer[1] != 0 {
		t.Fatalf("socks request: %v %v", answer, err)
	}
	return conn, int(answer[8])<<8 | int(answer[9])
}

func tcpRoundTrip(t *testing.T, inbound, echo int, size int) {
	t.Helper()
	conn, _ := socks(t, inbound, 1, echo)
	payload := make([]byte, size)
	rand.Read(payload)
	go conn.Write(payload)
	echoed := make([]byte, size)
	if _, err := io.ReadFull(conn, echoed); err != nil {
		t.Fatalf("TCP through ShadowQUIC: %v", err)
	}
	if !bytes.Equal(echoed, payload) {
		t.Fatal("the echoed bytes differ")
	}
}

func udpRoundTrip(t *testing.T, inbound, echo int, size int) {
	t.Helper()
	_, relay := socks(t, inbound, 3, 0)
	socket, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: relay})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	payload := make([]byte, size)
	rand.Read(payload)
	packet := append([]byte{0, 0, 0, 1, 127, 0, 0, 1, byte(echo >> 8), byte(echo)}, payload...)
	answer := make([]byte, 4096)
	// The first datagrams may leave before the QUIC connection exists.
	for attempt := 0; attempt < 5; attempt++ {
		socket.Write(packet)
		socket.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := socket.Read(answer)
		if err != nil {
			continue
		}
		if !bytes.Equal(answer[:n], packet) {
			t.Fatal("the echoed datagram differs")
		}
		return
	}
	t.Fatal("no UDP reply through ShadowQUIC")
}

// TCP and UDP of a core reach their targets through a ShadowQUIC server, with UDP in
// datagrams and in a stream.
func TestTrafficThroughTheOutbound(t *testing.T) {
	shadowquic.UseClientProgram(t)
	server := shadowquic.StartTestServer(t)
	tcp, udp := tcpEcho(t), udpEcho(t)
	for name, options := range map[string]string{
		"datagram": `"alpn": ["h3"], "zeroRtt": true, "udpOverStream": false`,
		"stream":   `"zeroRtt": false, "udpOverStream": true, "congestionControl": "cubic", "initialMtu": 1280, "minMtu": 1280`,
	} {
		t.Run(name, func(t *testing.T) {
			inbound := startCore(t, fmt.Sprintf(`{"address": "localhost", "port": %d, "username": "user", "password": "pass", "serverName": "localhost", %s}`, server, options))
			tcpRoundTrip(t, inbound, tcp, 16)
			tcpRoundTrip(t, inbound, tcp, 4<<20)
			udpRoundTrip(t, inbound, udp, 32)
			udpRoundTrip(t, inbound, udp, 1100)
		})
	}
}

// A wrong password carries nothing: the flow ends instead of reaching the target.
func TestWrongPasswordCarriesNothing(t *testing.T) {
	shadowquic.UseClientProgram(t)
	server := shadowquic.StartTestServer(t)
	inbound := startCore(t, fmt.Sprintf(`{"address": "127.0.0.1", "port": %d, "username": "user", "password": "wrong", "serverName": "localhost"}`, server))
	conn, _ := socks(t, inbound, 1, tcpEcho(t))
	conn.Write([]byte("hello"))
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if n, err := io.ReadFull(conn, make([]byte, 5)); err == nil {
		t.Fatalf("%d bytes came back with a wrong password", n)
	}
}
