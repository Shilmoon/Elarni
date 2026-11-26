package tuic

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	quic "github.com/sagernet/sing-quic/tuic"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	ST "github.com/sagernet/sing/common/tls"
)

type serverTLS struct{ tlsAdapter }

func (c *serverTLS) Start() error                          { return nil }
func (c *serverTLS) Close() error                          { return nil }
func (c *serverTLS) Server(conn net.Conn) (ST.Conn, error) { return tls.Server(conn, c.config), nil }

// endless starts a download that never ends on every TCP request, and counts in sent, when set, the bytes that left.
type endless struct{ sent *atomic.Int64 }

func (e endless) NewConnectionEx(_ context.Context, conn net.Conn, _, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	defer conn.Close()
	if _, err := conn.Read(make([]byte, 1)); err != nil {
		return
	}
	chunk := make([]byte, 64<<10)
	for {
		n, err := conn.Write(chunk)
		if e.sent != nil {
			e.sent.Add(int64(n))
		}
		if err != nil {
			return
		}
	}
}

// firstStreamWindow is quic-go's first stream window: what a stream the application does not read holds.
const firstStreamWindow = 2 << 20

// waitSent waits until the server has sent want bytes, and ends the test when it sends nothing for ten seconds.
func waitSent(t *testing.T, sent *atomic.Int64, want int64) {
	t.Helper()
	last, since := sent.Load(), time.Now()
	for last < want {
		time.Sleep(10 * time.Millisecond)
		if now := sent.Load(); now != last {
			last, since = now, time.Now()
		} else if time.Since(since) > 10*time.Second {
			t.Fatalf("the server stopped at %d of the %d bytes the stalled downloads may hold", last, want)
		}
	}
}

func (endless) NewPacketConnectionEx(_ context.Context, conn N.PacketConn, _, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	conn.Close()
}

// Every TCP flow is a stream of one QUIC connection. Downloads the app stopped reading held the whole
// connection window with their unread bytes (8 at quic-go's first 2 MiB stream window fill 16 MiB),
// so every other flow froze. Past twice the window of unread bytes it still freezes, to bound memory.
func TestStalledDownloadsLeaveTheConnectionUsable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var sent atomic.Int64
	server := startService(t, endless{sent: &sent})
	client, err := quic.NewClient(quic.ClientOptions{Context: ctx, Dialer: N.SystemDialer, ServerAddress: M.SocksaddrFromNet(server),
		TLSConfig:   &tlsAdapter{config: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}, MinVersion: tls.VersionTLS13}, timeout: 10 * time.Second},
		QUICOptions: clientQUICOptions, UUID: userID, Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(nil)
	origin := M.ParseSocksaddr("127.0.0.1:80")
	started := time.Now()
	const stalledDownloads = 12
	for range stalledDownloads {
		stalled, err := client.DialConn(ctx, origin)
		if err != nil {
			t.Fatal(err)
		}
		defer stalled.Close()
		stalled.Write([]byte{1})
	}
	// The server fills what the stalled streams may hold. A fixed second was not enough on a slow machine: the
	// fresh download then shared the link with twelve that still filled, and its megabyte did not arrive in time.
	waitSent(t, &sent, stalledDownloads*firstStreamWindow)
	filled := time.Since(started).Round(time.Millisecond)
	fresh, err := client.DialConn(ctx, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	fresh.Write([]byte{1})
	fresh.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := io.ReadFull(fresh, make([]byte, 1<<20)); err != nil {
		t.Fatalf("a fresh download got %d bytes: %v (the stalled ones were full after %v)", n, err, filled)
	}
	t.Logf("the stalled downloads were full after %v", filled)
}
