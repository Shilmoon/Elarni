package tuic

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	qtls "github.com/sagernet/sing-quic"
	quic "github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/xtls/xray-core/common/protocol/tls/cert"
)

// transfer serves each TCP flow one 9-byte request: 'd' or 'u' and a big-endian byte count. For
// 'd' it sends that many bytes; for 'u' it reads that many and answers one byte once all arrived.
type transfer struct{}

func (transfer) NewConnectionEx(_ context.Context, conn net.Conn, _, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	defer conn.Close()
	var request [9]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil {
		return
	}
	n := int64(binary.BigEndian.Uint64(request[1:]))
	switch request[0] {
	case 'd':
		io.CopyN(conn, zeros{}, n)
	case 'u':
		if _, err := io.CopyN(io.Discard, conn, n); err == nil {
			conn.Write([]byte{1})
		}
	}
}

func (transfer) NewPacketConnectionEx(_ context.Context, conn N.PacketConn, _, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	conn.Close()
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// bbrQUICOptions holds the packet size constant, as the sing-box#4538 reproduction does.
var bbrQUICOptions = qtls.QUICOptions{InitialPacketSize: 1200, DisablePathMTUDiscovery: true}

// startBBRService is startService with BBR and bbrQUICOptions.
func startBBRService(tb testing.TB, handler quic.ServiceHandler) net.Addr {
	certificate, _ := cert.MustGenerate(nil, cert.DNSNames("localhost"))
	certPEM, keyPEM := certificate.ToPEM()
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		tb.Fatal(err)
	}
	service, err := quic.NewService[int](quic.ServiceOptions{Context: context.Background(), Logger: logger.NOP(), Handler: handler,
		TLSConfig:   &serverTLS{tlsAdapter{config: &tls.Config{Certificates: []tls.Certificate{pair}, NextProtos: []string{"h3"}}, timeout: 10 * time.Second}},
		QUICOptions: bbrQUICOptions, CongestionControl: "bbr"})
	if err != nil {
		tb.Fatal(err)
	}
	service.UpdateUsers([]int{1}, [][16]byte{userID}, []string{"p"})
	socket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	if err := service.Start(socket); err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { service.Close() })
	return socket.LocalAddr()
}

// newShapedRelay is newRelay with a link of rate bits per second and delay in each direction, as
// in the sing-box#4538 reproduction (100 Mbit/s, 5 ms each way, no loss). closeRelay stops it; the
// test's cleanup does too.
func newShapedRelay(tb testing.TB, server net.Addr, rate float64, delay time.Duration) (addr net.Addr, closeRelay func()) {
	socket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	done := make(chan struct{})
	closeRelay = sync.OnceFunc(func() { close(done); socket.Close() })
	tb.Cleanup(closeRelay)
	type packet struct {
		data []byte
		to   net.Addr
		due  time.Time
	}
	type link struct {
		queue chan packet
		free  time.Time
	}
	newLink := func() *link {
		l := &link{queue: make(chan packet, 1<<16)}
		go func() {
			for {
				select {
				case p := <-l.queue:
					if wait := time.Until(p.due); wait > 0 {
						time.Sleep(wait)
					}
					socket.WriteTo(p.data, p.to)
				case <-done:
					return
				}
			}
		}()
		return l
	}
	up, down := newLink(), newLink()
	go func() {
		var client net.Addr
		buf := make([]byte, 65535)
		for {
			n, from, err := socket.ReadFrom(buf)
			if err != nil {
				return
			}
			l, to := up, server
			if from.String() == server.String() {
				l, to = down, client
			} else {
				client = from
			}
			if to == nil {
				continue
			}
			now := time.Now()
			l.free = later(l.free, now).Add(time.Duration(float64(n*8) / rate * float64(time.Second)))
			select {
			case l.queue <- packet{data: append([]byte(nil), buf[:n]...), to: to, due: l.free.Add(delay)}:
			case <-done:
				return
			}
		}
	}()
	return socket.LocalAddr(), closeRelay
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// request downloads ('d') or uploads ('u') n bytes on a new flow of client.
func request(ctx context.Context, client *quic.Client, kind byte, n int64) error {
	conn, err := client.DialConn(ctx, M.ParseSocksaddr("127.0.0.1:80"))
	if err != nil {
		return err
	}
	defer conn.Close()
	var header [9]byte
	header[0] = kind
	binary.BigEndian.PutUint64(header[1:], uint64(n))
	if _, err := conn.Write(header[:]); err != nil {
		return err
	}
	if kind == 'd' {
		_, err = io.CopyN(io.Discard, conn, n)
		return err
	}
	if _, err := io.CopyN(conn, zeros{}, n); err != nil {
		return err
	}
	_, err = io.ReadFull(conn, make([]byte, 1))
	return err
}

// BenchmarkBBRUploadAfterDownload follows the sing-box#4538 reproduction: TUIC with BBR at both
// ends, the final upload (16 MiB over four flows) of a fresh connection against the same upload on
// a connection that first went through uploads and downloads. There, with SagerNet/quic-go mod.7,
// the second ran at about 60% of the first; mod.12 reworks how application-limited periods reach
// the congestion controller.
//
//	go test -run '^$' -bench BBRUploadAfterDownload -benchtime 3x -count 3 ./proxy/tuic/
func BenchmarkBBRUploadAfterDownload(b *testing.B) {
	const MiB = 1 << 20
	type phase struct {
		kind  byte
		bytes int64
	}
	server := startBBRService(b, transfer{})
	for _, tc := range []struct {
		name    string
		history []phase
	}{
		{"fresh", []phase{{'u', MiB}}},
		{"after-download", []phase{{'u', MiB}, {'u', 4 * MiB}, {'d', 4 * MiB}, {'u', 16 * MiB}, {'d', 16 * MiB}}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			var mbps float64
			for range b.N {
				b.StopTimer()
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				relay, closeRelay := newShapedRelay(b, server, 100e6, 5*time.Millisecond)
				options := bbrQUICOptions
				options.ConnectionReceiveWindow = clientQUICOptions.ConnectionReceiveWindow
				client, err := quic.NewClient(quic.ClientOptions{Context: ctx, Dialer: N.SystemDialer, ServerAddress: M.SocksaddrFromNet(relay),
					TLSConfig:   &tlsAdapter{config: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}, MinVersion: tls.VersionTLS13}, timeout: 10 * time.Second},
					QUICOptions: options, UUID: userID, Password: "p", CongestionControl: "bbr"})
				if err != nil {
					b.Fatal(err)
				}
				for _, p := range tc.history {
					if err := request(ctx, client, p.kind, p.bytes); err != nil {
						b.Fatal(err)
					}
				}
				const upload = 16 * MiB
				start := time.Now()
				b.StartTimer()
				var wg sync.WaitGroup
				errs := make(chan error, 4)
				for range 4 {
					wg.Go(func() { errs <- request(ctx, client, 'u', upload/4) })
				}
				wg.Wait()
				b.StopTimer()
				for range 4 {
					if err := <-errs; err != nil {
						b.Fatal(err)
					}
				}
				mbps += float64(upload*8) / time.Since(start).Seconds() / 1e6
				client.CloseWithError(nil)
				cancel()
				// Each iteration's relay (a socket, its goroutines and their queues) goes with it,
				// so relays do not pile up over a larger b.N and skew the figures.
				closeRelay()
			}
			b.ReportMetric(mbps/float64(b.N), "upload-Mbit/s")
		})
	}
}
