package wireguard

import (
	"bytes"
	goerrors "errors"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

type testCounter struct{ atomic.Int64 }

func (c *testCounter) Value() int64      { return c.Load() }
func (c *testCounter) Set(v int64) int64 { return c.Swap(v) }
func (c *testCounter) Add(v int64) int64 { return c.Int64.Add(v) - v }

// Linux reads and writes the queued datagrams of a batch in one system call; the
// counters, reserved bytes and endpoints stay as they were per datagram. The socket is
// dual-stack and the peer IPv4, as DialSystem makes them.
func TestBindMovesDatagramBatchesWithReservedBytesAndCounters(t *testing.T) {
	socket, err := net.ListenPacket("udp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	read, written := &testCounter{}, &testCounter{}
	b := &bind{reserved: []byte{7, 8, 9}, listenFunc: func() (net.PacketConn, error) {
		return &PacketCounterConnection{PacketConn: socket, ReadCounter: read, WriteCounter: written}, nil
	}}
	fns, _, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	wantBatch := 1
	if runtime.GOOS == "linux" {
		wantBatch = conn.IdealBatchSize
	}
	if b.BatchSize() != wantBatch {
		t.Fatalf("batch size %d, want %d", b.BatchSize(), wantBatch)
	}

	target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: socket.LocalAddr().(*net.UDPAddr).Port}
	for i := range 5 {
		if _, err := peer.WriteTo([]byte{4, 1, 1, 1, byte(i)}, target); err != nil {
			t.Fatal(err)
		}
	}
	bufs, sizes, eps := make([][]byte, 8), make([]int, 8), make([]conn.Endpoint, 8)
	for i := range bufs {
		bufs[i] = make([]byte, 2048)
	}
	var got [][]byte
	for len(got) < 5 {
		n, err := fns[0](bufs, sizes, eps)
		if err != nil {
			t.Fatal(err)
		}
		if wantBatch > 1 && n != 5 {
			t.Fatalf("one receive returned %d of 5 queued datagrams", n)
		}
		for i := range n {
			if from := eps[i].(*conn.StdNetEndpoint).AddrPort; netip.AddrPortFrom(from.Addr().Unmap(), from.Port()).String() != peer.LocalAddr().String() {
				t.Fatalf("endpoint %s, want %s", from, peer.LocalAddr())
			}
			got = append(got, bytes.Clone(bufs[i][:sizes[i]]))
		}
	}
	for i, datagram := range got {
		if !bytes.Equal(datagram, []byte{4, 0, 0, 0, byte(i)}) {
			t.Fatalf("datagram %d is %v; reserved bytes must be cleared", i, datagram)
		}
	}
	if read.Value() != 25 {
		t.Fatalf("read counter %d, want 25", read.Value())
	}

	ep, err := b.ParseEndpoint(peer.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	out := make([][]byte, 5)
	for i := range out {
		out[i] = []byte{4, 0, 0, 0, byte(i), 0}
	}
	if err := b.Send(out, ep); err != nil {
		t.Fatal(err)
	}
	peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	for i := range 5 {
		n, _, err := peer.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf[:n], []byte{4, 7, 8, 9, byte(i), 0}) {
			t.Fatalf("sent datagram %d arrived as %v", i, buf[:n])
		}
	}
	if written.Value() != 30 {
		t.Fatalf("write counter %d, want 30", written.Value())
	}
}

// AmneziaWG keeps 64 KiB message buffers, so batches would hold megabytes per device.
func TestAmneziaEngineKeepsSingleDatagramBatches(t *testing.T) {
	if got := (&amneziaBind{&bind{}}).BatchSize(); got != 1 {
		t.Fatalf("AmneziaWG bind batch size %d", got)
	}
	if got := (&amneziaTUN{}).BatchSize(); got != 1 {
		t.Fatalf("AmneziaWG TUN batch size %d", got)
	}
}

// The default UDP buffers overflowed when several streams arrived at once.
func TestBindEnlargesTheSocketBuffers(t *testing.T) {
	fresh, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defaultSize := receiveBuffer(t, fresh)
	fresh.Close()
	socket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	b := &bind{listenFunc: func() (net.PacketConn, error) { return &PacketCounterConnection{PacketConn: socket}, nil }}
	if _, _, err := b.Open(0); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if size := receiveBuffer(t, socket); size <= defaultSize {
		t.Fatalf("receive buffer %d, default %d", size, defaultSize)
	}
}

func receiveBuffer(t *testing.T, conn syscall.Conn) int {
	t.Helper()
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	size := 0
	raw.Control(func(fd uintptr) { size, err = getReceiveBuffer(fd) })
	if err != nil {
		t.Fatal(err)
	}
	return size
}

// Every received datagram allocated an endpoint, though the one peer rarely changes address.
func TestBindReusesTheEndpointOfAnUnchangedPeer(t *testing.T) {
	for name, wrap := range map[string]func(net.PacketConn) net.PacketConn{
		"batched":      func(c net.PacketConn) net.PacketConn { return c },
		"per datagram": func(c net.PacketConn) net.PacketConn { return plainSocket{c} },
	} {
		t.Run(name, func(t *testing.T) {
			socket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			b := &bind{listenFunc: func() (net.PacketConn, error) { return wrap(socket), nil }}
			fns, _, err := b.Open(0)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			var peers [2]*net.UDPConn
			for i := range peers {
				if peers[i], err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}); err != nil {
					t.Fatal(err)
				}
				defer peers[i].Close()
			}
			target := socket.LocalAddr().(*net.UDPAddr).AddrPort()
			bufs, sizes, eps := [][]byte{make([]byte, 2048)}, make([]int, 1), make([]conn.Endpoint, 1)
			receive := func(peer *net.UDPConn) conn.Endpoint {
				if _, err := peer.WriteToUDPAddrPort([]byte{4, 0, 0, 0}, target); err != nil {
					t.Fatal(err)
				}
				if _, err := fns[0](bufs, sizes, eps); err != nil {
					t.Fatal(err)
				}
				return eps[0]
			}
			first, again, other := receive(peers[0]), receive(peers[0]), receive(peers[1])
			if first != again {
				t.Error("a second datagram from the same peer got a new endpoint")
			}
			if other == again || other.(*conn.StdNetEndpoint).Port() != peers[1].LocalAddr().(*net.UDPAddr).AddrPort().Port() {
				t.Errorf("a datagram from another peer got endpoint %v", other)
			}
			samePeer := testing.AllocsPerRun(100, func() { receive(peers[0]) })
			next := 0
			alternating := testing.AllocsPerRun(100, func() { next ^= 1; receive(peers[next]) })
			if samePeer >= alternating {
				t.Errorf("%v allocations per datagram from one peer, %v from alternating peers", samePeer, alternating)
			}
		})
	}
}

// plainSocket hides the UDP socket, as masks and diagnostics wrappers do.
type plainSocket struct{ net.PacketConn }

func benchmarkBindReceive(b *testing.B, wrap func(net.PacketConn) net.PacketConn) {
	socket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer peer.Close()
	bd := &bind{listenFunc: func() (net.PacketConn, error) { return wrap(socket), nil }}
	fns, _, err := bd.Open(0)
	if err != nil {
		b.Fatal(err)
	}
	defer bd.Close()
	datagram, target := make([]byte, 1200), socket.LocalAddr()
	bufs, sizes, eps := [][]byte{make([]byte, 2048)}, make([]int, 1), make([]conn.Endpoint, 1)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		if _, err := peer.WriteTo(datagram, target); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, err := fns[0](bufs, sizes, eps); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBindReceiveBatched(b *testing.B) {
	benchmarkBindReceive(b, func(c net.PacketConn) net.PacketConn { return c })
}

func BenchmarkBindReceivePerDatagram(b *testing.B) {
	benchmarkBindReceive(b, func(c net.PacketConn) net.PacketConn { return plainSocket{c} })
}

var errFailingSocket = goerrors.New("the socket keeps failing")

// failingSocket fails every read but those of the datagrams it is given, until it is closed.
type failingSocket struct {
	net.PacketConn // only the methods below are called
	mu             sync.Mutex
	reads          int
	datagrams      int
	closed         bool
}

func (s *failingSocket) ReadFrom(p []byte) (int, net.Addr, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	switch {
	case s.closed:
		return 0, nil, net.ErrClosed
	case s.datagrams == 0:
		return 0, nil, errFailingSocket
	}
	s.datagrams--
	return copy(p, []byte{4, 0, 0, 0, 1}), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 51820}, nil
}

func (s *failingSocket) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
}

func (s *failingSocket) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *failingSocket) counts() (reads int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func (s *failingSocket) give(datagrams int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.datagrams += datagrams
}

// A socket error that repeats at once spun the receive loop on a whole core. It waits longer after
// each one, still takes the datagram that comes after them, and ends at once when the bind closes.
func TestBindBacksOffASocketThatKeepsFailing(t *testing.T) {
	socket := &failingSocket{}
	b := &bind{listenFunc: func() (net.PacketConn, error) { return socket, nil }}
	fns, _, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	type result struct {
		n   int
		err error
	}
	bufs, sizes, eps := [][]byte{make([]byte, 2048)}, make([]int, 1), make([]conn.Endpoint, 1)
	receive := func() <-chan result {
		done := make(chan result, 1)
		go func() {
			n, err := fns[0](bufs, sizes, eps)
			done <- result{n, err}
		}()
		return done
	}

	done := receive()
	time.Sleep(150 * time.Millisecond)
	// at most 2 reads by 5 ms, then one per wait of 5, 10, 20, 40 and 80 ms; a spinning loop makes millions
	if reads := socket.counts(); reads < 2 || reads > 20 {
		t.Fatalf("%d reads of a failing socket in 150 ms", reads)
	}
	socket.give(1)
	select {
	case r := <-done:
		if r.err != nil || r.n != 1 || !bytes.Equal(bufs[0][:sizes[0]], []byte{4, 0, 0, 0, 1}) {
			t.Fatalf("receive after the errors: %d datagrams, %v, %v", r.n, r.err, bufs[0][:sizes[0]])
		}
	case <-time.After(5 * receiveRetryMax):
		t.Fatal("the datagram after the errors never came")
	}

	// the next errors start over and back off again; Close ends the wait instead of sleeping through it
	done = receive()
	time.Sleep(400 * time.Millisecond)
	closed := time.Now()
	b.Close()
	select {
	case r := <-done:
		if !goerrors.Is(r.err, net.ErrClosed) {
			t.Fatalf("receive after Close: %v, want net.ErrClosed", r.err)
		}
		if waited := time.Since(closed); waited > 100*time.Millisecond {
			t.Fatalf("receive ended %v after Close", waited)
		}
	case <-time.After(5 * receiveRetryMax):
		t.Fatal("Close did not end the receive")
	}
}
