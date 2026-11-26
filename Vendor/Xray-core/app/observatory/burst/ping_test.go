package burst

import (
	"context"
	gonet "net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport/internet/tagged"
)

// A probe to a server that takes the connection and never answers its TLS hello ends within the probe
// timeout, and so does the dial behind it: net/http keeps a dial going after the request gives up,
// and without a handshake timeout that one waited for the server, holding its socket and goroutines.
func TestProbeToAStalledTLSServerLeavesNothingBehind(t *testing.T) {
	ln, err := gonet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []gonet.Conn
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	}()
	defer func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			conn.Close()
		}
	}()

	// dialed orders the dial goroutine's read of tagged.Dialer before the restore below
	dialed := make(chan struct{}, 1)
	saved := tagged.Dialer
	tagged.Dialer = func(ctx context.Context, _ routing.Dispatcher, dest net.Destination, _ string) (net.Conn, error) {
		select {
		case dialed <- struct{}{}:
		default:
		}
		var d gonet.Dialer
		return d.DialContext(ctx, "tcp", dest.NetAddr())
	}
	defer func() { tagged.Dialer = saved }()

	// let the accept loop settle before counting
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const timeout = 200 * time.Millisecond
	client := newPingClient(context.Background(), nil, "https://"+ln.Addr().String()+"/", timeout, "proxy")
	start := time.Now()
	if _, err := client.MeasureDelay("HEAD"); err == nil {
		t.Fatal("a probe to a server that never answers succeeded")
	}
	if elapsed := time.Since(start); elapsed > 5*timeout {
		t.Fatalf("the probe took %v, its timeout is %v", elapsed, timeout)
	}
	for _, reached := range []chan struct{}{dialed, accepted} {
		select {
		case <-reached:
		case <-time.After(10 * timeout):
			t.Fatal("the probe never reached the server")
		}
	}

	deadline := time.Now().Add(10 * timeout)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("%d goroutines %v after the probe, %d before it:\n%s",
				runtime.NumGoroutine(), 10*timeout, baseline, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}
