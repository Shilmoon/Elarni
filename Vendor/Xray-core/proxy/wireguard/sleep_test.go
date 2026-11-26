package wireguard

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/device"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

// sleepScene is what a sleepWatch sees: the two clocks and an engine with two peers, one of which
// never had a handshake.
type sleepScene struct {
	wall      time.Time
	monotonic time.Duration
	handshake time.Time // the active peer's last, by the wall clock; zero for none
	expired   [][32]byte
}

var activePeer, idlePeer = [32]byte{1}, [32]byte{2}

func (s *sleepScene) clocks() (time.Time, time.Duration) { return s.wall, s.monotonic }

// state is an IpcGet answer of either engine, trimmed to what the watch reads.
func (s *sleepScene) state() (string, error) {
	var seconds, nanoseconds int64
	if !s.handshake.IsZero() {
		seconds, nanoseconds = s.handshake.Unix(), int64(s.handshake.Nanosecond())
	}
	return fmt.Sprintf("private_key=%x\npublic_key=%x\nlast_handshake_time_sec=%d\nlast_handshake_time_nsec=%d\ntx_bytes=1\n"+
		"public_key=%x\nlast_handshake_time_sec=0\nlast_handshake_time_nsec=0\n", [32]byte{9}, activePeer, seconds, nanoseconds, idlePeer), nil
}

type sleepStep func(*sleepScene)

func awake(d time.Duration) sleepStep {
	return func(s *sleepScene) { s.wall, s.monotonic = s.wall.Add(d), s.monotonic+d }
}

func asleep(d time.Duration) sleepStep {
	return func(s *sleepScene) { s.wall = s.wall.Add(d) }
}

// handshake is one the engine completed on its own.
func handshake(s *sleepScene) { s.handshake = s.wall }

func TestSleepSinceTheLastHandshakeExpiresTheKeysFromOneMinute(t *testing.T) {
	for _, c := range []struct {
		name     string
		noKey    bool
		steps    []sleepStep
		expiries int
	}{
		{"one sleep of 61 s", false, []sleepStep{asleep(61 * time.Second)}, 1},
		{"one sleep of 59 s", false, []sleepStep{asleep(59 * time.Second)}, 0},
		{"three sleeps of 25 s on one key", false, []sleepStep{asleep(25 * time.Second), awake(20 * time.Second), asleep(25 * time.Second),
			awake(20 * time.Second), asleep(25 * time.Second)}, 1},
		// The engine's own rekey starts the count again.
		{"sleeps of 40 and 25 s with a new key between them", false, []sleepStep{asleep(40 * time.Second), awake(30 * time.Second), handshake,
			asleep(25 * time.Second)}, 0},
		{"sleeps of 40 and 25 s on one key", false, []sleepStep{asleep(40 * time.Second), awake(30 * time.Second), asleep(25 * time.Second)}, 1},
		// From RekeyAfterTime of awake time the engine renews the key itself as it sends.
		{"a sleep of 61 s on a key the engine renews", false, []sleepStep{awake(125 * time.Second), asleep(61 * time.Second)}, 0},
		{"a sleep of 61 s before the first handshake", true, []sleepStep{asleep(61 * time.Second)}, 0},
		// The keys expired once wait for the handshake that renews them.
		{"a short sleep after an expiry", false, []sleepStep{asleep(61 * time.Second), awake(time.Second), asleep(5 * time.Second)}, 1},
		{"a long sleep after an expiry", false, []sleepStep{asleep(61 * time.Second), awake(time.Second), asleep(61 * time.Second)}, 2},
		{"the wall clock set back ten minutes", false, []sleepStep{asleep(-10 * time.Minute), awake(time.Minute)}, 0},
		{"the wall clock set back, then a sleep of 30 s", false, []sleepStep{asleep(-10 * time.Minute), awake(time.Second), asleep(30 * time.Second)}, 0},
		{"ten minutes awake", false, slices.Repeat([]sleepStep{awake(2 * time.Second)}, 300), 0},
		// A wall clock that NTP slews by up to 500 ppm is no sleep.
		{"a slewed wall clock", false, slices.Repeat([]sleepStep{awake(2 * time.Second), asleep(time.Millisecond)}, 60), 0},
		// Sleep is summed from the clocks, not from steps: naps between sends add up.
		{"naps of 1.9 s that add up to a minute", false, slices.Repeat([]sleepStep{awake(100 * time.Millisecond), asleep(1900 * time.Millisecond)}, 33), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &sleepScene{wall: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC), monotonic: time.Hour}
			if !c.noKey {
				handshake(s)
			}
			w := newSleepWatch(s.clocks, s.state, func(peers [][32]byte) { s.expired = append(s.expired, peers...) }, [][32]byte{activePeer, idlePeer})
			for _, step := range c.steps {
				step(s)
				w.dialing() // as a send or a dial reads the clocks
			}
			for _, peer := range s.expired {
				if peer != activePeer {
					t.Fatalf("expired the keys of a peer without a handshake")
				}
			}
			if len(s.expired) != c.expiries {
				t.Fatalf("%d expiries, want %d", len(s.expired), c.expiries)
			}
		})
	}
}

// A send starts the check on its own goroutine, and a dial waits for that check to end.
func TestSleepCheckOfASendEndsBeforeTheNextDial(t *testing.T) {
	s := &sleepScene{wall: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC), monotonic: time.Hour}
	handshake(s)
	expiring, release := make(chan struct{}), make(chan struct{})
	var expiries atomic.Int32
	w := newSleepWatch(s.clocks, s.state, func([][32]byte) {
		close(expiring)
		<-release
		expiries.Add(1)
	}, [][32]byte{activePeer})
	asleep(61 * time.Second)(s)
	w.sending()
	select {
	case <-expiring:
	case <-time.After(5 * time.Second):
		t.Fatal("the send started no check")
	}
	w.sending() // a check runs: no second one
	dialed := make(chan struct{})
	go func() { w.dialing(); close(dialed) }()
	select {
	case <-dialed:
		t.Fatal("the dial did not wait for the check of the send")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-dialed:
	case <-time.After(5 * time.Second):
		t.Fatal("the dial still waits after the check ended")
	}
	w.stop()
	if n := expiries.Load(); n != 1 {
		t.Fatalf("%d expiries, want 1", n)
	}
}

// initiationCounter counts the handshake initiations that reach the server's socket and keeps the
// kind of the first message after mark.
type initiationCounter struct {
	net.PacketConn
	amnezia bool // headers as the fixture's h1 range sets them
	count   atomic.Int32
	marked  atomic.Bool
	first   atomic.Int32 // after mark: 0 none yet, 1 an initiation, 2 another message
}

func (c *initiationCounter) ReadFrom(p []byte) (int, net.Addr, error) {
	n, from, err := c.PacketConn.ReadFrom(p)
	if err != nil {
		return n, from, err
	}
	header := binary.LittleEndian.Uint32(p)
	initiation := n == device.MessageInitiationSize && (!c.amnezia && header == device.MessageInitiationType || c.amnezia && header >= 100000 && header <= 100100)
	if initiation {
		c.count.Add(1)
	}
	// AmneziaWG's junk packets (10 to 20 bytes here) are shorter than any message.
	if c.marked.Load() && n >= device.MessageKeepaliveSize {
		kind := int32(2)
		if initiation {
			kind = 1
		}
		c.first.CompareAndSwap(0, kind)
	}
	return n, from, err
}

func (c *initiationCounter) mark() { c.first.Store(0); c.marked.Store(true) }

// Regression (N4-47): both engines age a key by Go's monotonic clock, which stands still while the
// device sleeps. After a sleep past the key's lifetime the client kept sending with a key the server
// had dropped, and noticed only after 15 s of awake time without an answer. The server here forgets
// the client's keys when the client sleeps; a fresh peer also takes the next initiation at once,
// which the engine's flood limit (HandshakeInitationRate) would otherwise refuse.
func TestWireGuardHandshakesAgainAfterTheDeviceSlept(t *testing.T) {
	previousClocks := readClocks
	t.Cleanup(func() { readClocks = previousClocks })
	for _, amnezia := range []bool{false, true} {
		for _, flow := range []string{"open connection", "new connection", "no sleep"} {
			t.Run(fmt.Sprintf("amnezia=%t/%s", amnezia, flow), func(t *testing.T) {
				var slept atomic.Int64
				readClocks = func() (time.Time, time.Duration) {
					wall, monotonic := previousClocks()
					return wall.Add(time.Duration(slept.Load())), monotonic
				}
				settings := map[string]string{}
				if amnezia {
					settings = map[string]string{"jc": "2", "jmin": "10", "jmax": "20", "h1": "100000-100100", "h2": "200000-200100", "h3": "300000-300100", "h4": "400000-400100"}
				}
				counter := &initiationCounter{amnezia: amnezia}
				f := newTunnelFixture(t, fixtureOptions{amnezia: amnezia, settings: settings, wrapServer: func(c net.PacketConn) net.PacketConn {
					counter.PacketConn = c
					return counter
				}})
				if err := f.handler.init(context.Background()); err != nil {
					t.Fatal(err)
				}
				remote := netip.MustParseAddrPort("10.29.0.1:18187")
				full, family := convertToFullAddr(remote)
				listener, err := gonet.ListenTCP(f.serverStack, full, family)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				go func() {
					for {
						conn, err := listener.Accept()
						if err != nil {
							return
						}
						go func() {
							defer conn.Close()
							conn.SetDeadline(time.Now().Add(10 * time.Second))
							io.Copy(conn, conn)
						}()
					}
				}()
				udpServer, err := gonet.DialUDP(f.serverStack, &full, nil, family)
				if err != nil {
					t.Fatal(err)
				}
				defer udpServer.Close()
				go func() {
					buffer := make([]byte, 2048)
					for {
						n, from, err := udpServer.ReadFrom(buffer)
						if err != nil {
							return
						}
						udpServer.WriteTo(buffer[:n], from)
					}
				}()
				dial := func(network string, wait time.Duration) net.Conn {
					t.Helper()
					var connection net.Conn
					var err error
					if network == "udp" {
						connection, err = f.clientNet.DialUDPAddrPort(netip.AddrPort{}, remote)
					} else {
						ctx, cancel := context.WithTimeout(context.Background(), wait)
						connection, err = f.clientNet.DialContextTCPAddrPort(ctx, remote)
						cancel()
					}
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { connection.Close() })
					return connection
				}
				roundTrip := func(connection net.Conn, payload string, wait time.Duration) {
					t.Helper()
					connection.SetDeadline(time.Now().Add(wait))
					if _, err := connection.Write([]byte(payload)); err != nil {
						t.Fatal(err)
					}
					reply := make([]byte, len(payload))
					if _, err := io.ReadFull(connection, reply); err != nil {
						t.Fatalf("%s: %v", payload, err)
					}
					if string(reply) != payload {
						t.Fatal("tunneled payload changed")
					}
				}
				// A datagram leaves no acknowledgement behind to go out after the mark.
				first := "udp"
				if flow == "open connection" {
					first = "tcp"
				}
				connection := dial(first, 10*time.Second)
				roundTrip(connection, "before the sleep", 10*time.Second)
				handshakes := counter.count.Load()
				if handshakes == 0 {
					t.Fatal("the counter saw no handshake initiation")
				}
				if flow != "no sleep" {
					f.removeClient(t)
					f.addClient(t)
					slept.Store(int64(300 * time.Second))
				}
				counter.mark()
				// Well before the engine's own new handshake after 15 s.
				const recovery = 3 * time.Second
				switch flow {
				case "new connection":
					if err := f.handler.init(context.Background()); err != nil {
						t.Fatal(err)
					}
					roundTrip(dial("tcp", recovery), "after the sleep", recovery)
					if kind := counter.first.Load(); kind != 1 {
						t.Fatalf("the first message after the sleep was not an initiation (%d)", kind)
					}
				default:
					roundTrip(connection, "after the sleep", recovery)
				}
				if sent := counter.count.Load() - handshakes; flow != "no sleep" && sent == 0 {
					t.Fatal("no handshake initiation reached the server after the sleep")
				} else if flow == "no sleep" && sent != 0 {
					t.Fatalf("%d handshake initiations reached the server without a sleep", sent)
				}
			})
		}
	}
}
