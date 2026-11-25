package libXray

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/xtls/libxray/xray"
	"github.com/xtls/xray-core/core"
)

// freezingRelay forwards TCP to a server and lets a budget of server-to-client bytes through on each
// connection; past it the bytes vanish and the connection stays open (no FIN, no RST), as a path that
// freezes connections to some hosting networks does. A zero budget lets everything through.
type freezingRelay struct {
	budget   atomic.Int64
	listener net.Listener
}

func startFreezingRelay(t *testing.T, server string) *freezingRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relay := &freezingRelay{listener: listener}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go relay.forward(client, server)
		}
	}()
	return relay
}

func (r *freezingRelay) port() int { return r.listener.Addr().(*net.TCPAddr).Port }

func (r *freezingRelay) forward(client net.Conn, address string) {
	server, err := net.Dial("tcp", address)
	if err != nil {
		_ = client.Close()
		return
	}
	go func() {
		_, _ = io.Copy(server, client)
		_ = server.Close()
		_ = client.Close()
	}()
	budget := r.budget.Load()
	buffer := make([]byte, 1400)
	var passed int64
	frozen := false
	for {
		n, err := server.Read(buffer)
		if n > 0 && !frozen {
			if budget == 0 || passed+int64(n) <= budget {
				_, _ = client.Write(buffer[:n])
				passed += int64(n)
			} else {
				frozen = true
			}
		}
		if err != nil {
			if !frozen {
				_ = client.Close()
			}
			return
		}
	}
}

func startTestCore(t *testing.T, config string) {
	t.Helper()
	loaded, err := core.LoadConfig("json", bytes.NewReader([]byte(config)))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := core.New(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

type pingReply struct {
	Success bool         `json:"success"`
	Error   string       `json:"error"`
	Data    PingResponse `json:"data"`
}

func invokePingJSON(t *testing.T, request map[string]any) pingReply {
	t.Helper()
	payload, _ := json.Marshal(request)
	var reply pingReply
	if err := json.Unmarshal([]byte(invokePing(payload)), &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

// N9-01: a server whose path freezes each connection after about 16 KB (11 KB on mobile) passes a latency
// probe, which needs a few hundred bytes, while every page through it hangs. The transfer check tells them
// apart on each probe path: a temporary core, the running core, and the device's packet path.
func TestTransferCheckSeesAServerWhosePathFreezes(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), 65536) // 1 MiB
	mux := http.NewServeMux()
	mux.HandleFunc("/generate_204", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			http.Error(w, "identity expected", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(big)))
		_, _ = w.Write(big)
	})
	mux.HandleFunc("/small", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(big[:4096]) })
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	go server.Serve(target)
	defer server.Close()
	local := "http://" + target.Addr().String()
	refused := fmt.Sprintf("http://127.0.0.1:%d/big", freeLoopbackPort(t))

	const id = "11111111-2222-4333-8444-555555555555"
	serverPort := freeLoopbackPort(t)
	startTestCore(t, fmt.Sprintf(`{"log":{"loglevel":"none"},
	  "inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"vless","settings":{"clients":[{"id":%q}],"decryption":"none"}}],
	  "outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.0/8"]}]}}]}`, serverPort, id))
	relay := startFreezingRelay(t, fmt.Sprintf("127.0.0.1:%d", serverPort))
	vless := fmt.Sprintf(`{"protocol":"vless","settings":{"address":"127.0.0.1","port":%d,"id":%q,"encryption":"none"}}`, relay.port(), id)
	// The device's own path, frozen in front of the target itself.
	direct := startFreezingRelay(t, target.Addr().String())
	directURL := fmt.Sprintf("http://127.0.0.1:%d", direct.port())

	socks := freeLoopbackPort(t)
	temporary := map[string]any{
		"configJSON": fmt.Sprintf(`{"log":{"loglevel":"none"},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"socks"}],"outbounds":[%s]}`, socks, vless),
		"proxy":      fmt.Sprintf("socks5://127.0.0.1:%d", socks),
	}
	latency := func(path map[string]any, url string) pingReply {
		request := map[string]any{"url": url, "method": "GET", "mode": "default", "timeout": 3, "expectedStatus": 204, "expectedBody": ""}
		for key, value := range path {
			request[key] = value
		}
		return invokePingJSON(t, request)
	}
	transfer := func(path map[string]any, url string) pingReply {
		// Tests do not wait out the 4 s idle limit.
		request := map[string]any{"transfer": true, "url": url, "method": "GET", "timeout": 3, "transferIdleMs": 300}
		for key, value := range path {
			request[key] = value
		}
		return invokePingJSON(t, request)
	}
	expectVerdict := func(name string, reply pingReply, verdict string) {
		t.Helper()
		if !reply.Success || reply.Data.Verdict != verdict {
			t.Fatalf("%s: want %s, got success=%v error=%q verdict=%q after %d bytes", name, verdict, reply.Success, reply.Error, reply.Data.Verdict, reply.Data.TransferBytes)
		}
		passed := reply.Data.TransferBytes >= 96_000
		if passed != (verdict == "ok") {
			t.Fatalf("%s: %s after %d bytes", name, verdict, reply.Data.TransferBytes)
		}
		t.Logf("%s: %s, %d bytes in %d ms", name, verdict, reply.Data.TransferBytes, reply.Data.Delay)
	}

	runningCore := map[string]any{"runningCore": true}
	packetPath := map[string]any{"packetPath": true}
	if err := xray.RunXrayFromJSON(`{"log":{"loglevel":"none"},"outbounds":[` + vless + `]}`); err != nil {
		t.Fatal(err)
	}
	defer xray.StopXray()
	for _, budget := range []int64{16_384, 11_200, 0} {
		relay.budget.Store(budget)
		direct.budget.Store(budget)
		verdict := "stalled"
		if budget == 0 {
			verdict = "ok"
		}
		name := fmt.Sprintf("budget %d", budget)
		// The latency probe passes whatever the budget: that is the finding.
		if reply := latency(temporary, local+"/generate_204"); !reply.Success || reply.Data.Delay < 0 {
			t.Fatalf("%s: the latency probe failed: %+v", name, reply)
		}
		if reply := latency(runningCore, local+"/generate_204"); !reply.Success {
			t.Fatalf("%s: the running core's latency probe failed: %+v", name, reply)
		}
		expectVerdict(name+", temporary core", transfer(temporary, local+"/big"), verdict)
		expectVerdict(name+", running core", transfer(runningCore, local+"/big"), verdict)
		expectVerdict(name+", packet path", transfer(packetPath, directURL+"/big"), verdict)
	}

	// A target that refuses, fails or answers too little says nothing about a freeze.
	relay.budget.Store(16_384)
	for _, url := range []string{refused, local + "/missing", local + "/small"} {
		expectVerdict(url+", temporary core", transfer(temporary, url), "inconclusive")
		expectVerdict(url+", running core", transfer(runningCore, url), "inconclusive")
	}
	expectVerdict("refused, packet path", transfer(packetPath, refused), "inconclusive")

	// A transfer check is a GET with a bounded idle limit.
	for _, bad := range []map[string]any{{"method": "HEAD"}, {"trace": true}, {"transferIdleMs": 50}, {"transferIdleMs": 3000}} {
		request := map[string]any{"transfer": true, "url": local + "/big", "method": "GET", "timeout": 3, "packetPath": true}
		for key, value := range bad {
			request[key] = value
		}
		if reply := invokePingJSON(t, request); reply.Success || reply.Error != "invalid_transfer_check" {
			t.Fatalf("%v was accepted: %+v", bad, reply)
		}
	}
}
