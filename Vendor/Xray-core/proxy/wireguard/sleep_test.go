package wireguard

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	awg "github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/xtls/xray-core/transport/internet"
	"golang.zx2c4.com/wireguard/device"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

// One reading of the watcher: how far the monotonic clock moved since the last one, and how much
// further the wall clock moved, which is the time the device slept.
type clockStep struct{ awake, asleep time.Duration }

func TestSleepWithinOneKeyLifetimeAsksForAHandshakeFromOneMinute(t *testing.T) {
	const tick = 2 * time.Second
	sleep := func(d time.Duration) []clockStep { return []clockStep{{tick, d}} }
	awake := func(d time.Duration) []clockStep {
		return slices.Repeat([]clockStep{{tick, 0}}, int(d/tick))
	}
	for _, c := range []struct {
		name  string
		steps []clockStep
		due   []int // the readings that ask for a handshake
	}{
		{"one sleep of 61 s", sleep(61 * time.Second), []int{0}},
		{"one sleep of 59 s", sleep(59 * time.Second), nil},
		{"three sleeps of 25 s within 180 s", slices.Concat(sleep(25*time.Second), awake(20*time.Second), sleep(25*time.Second),
			awake(20*time.Second), sleep(25*time.Second)), []int{22}},
		{"three sleeps of 25 s 100 s apart", slices.Concat(sleep(25*time.Second), awake(98*time.Second), sleep(25*time.Second),
			awake(98*time.Second), sleep(25*time.Second)), nil},
		{"the wall clock set back ten minutes", slices.Concat(sleep(-10*time.Minute), awake(time.Minute)), nil},
		{"ten minutes of steady ticks", awake(10 * time.Minute), nil},
		// Timer and clock jitter is no sleep, however much of it adds up.
		{"gaps under 2 s", slices.Repeat([]clockStep{{tick, 1900 * time.Millisecond}}, 40), nil},
		// A handshake starts the count again.
		{"a short sleep after a handshake", slices.Concat(sleep(61*time.Second), awake(4*time.Second), sleep(30*time.Second)), []int{0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			wall, monotonic := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC), time.Hour
			var watch sleepWatch
			if watch.observe(wall, monotonic) {
				t.Fatal("the first reading asked for a handshake")
			}
			var due []int
			for i, step := range c.steps {
				monotonic += step.awake
				wall = wall.Add(step.awake + step.asleep)
				if watch.observe(wall, monotonic) {
					due = append(due, i)
				}
			}
			if !slices.Equal(due, c.due) {
				t.Fatalf("readings %v asked for a handshake, want %v", due, c.due)
			}
		})
	}
}

// initiationCounter counts the handshake initiations that reach a peer's socket.
type initiationCounter struct {
	net.PacketConn
	amnezia bool // headers as the fixture's h1 range sets them
	count   atomic.Int32
}

func (c *initiationCounter) ReadFrom(p []byte) (int, net.Addr, error) {
	n, from, err := c.PacketConn.ReadFrom(p)
	if err == nil && n == device.MessageInitiationSize {
		header := binary.LittleEndian.Uint32(p)
		if !c.amnezia && header == device.MessageInitiationType || c.amnezia && header >= 100000 && header <= 100100 {
			c.count.Add(1)
		}
	}
	return n, from, err
}

// Regression (N4-47): both engines age a key by Go's monotonic clock, which stands still while
// the device sleeps. After a sleep past the key's lifetime the client kept sending with a key the
// server had dropped, and noticed only after 15 s of awake time without an answer.
func TestWireGuardHandshakesAgainAfterTheDeviceSlept(t *testing.T) {
	previousTick, previousClocks := sleepTick, readClocks
	t.Cleanup(func() { sleepTick, readClocks = previousTick, previousClocks })
	sleepTick = 20 * time.Millisecond
	for _, amnezia := range []bool{false, true} {
		for _, slept := range []bool{true, false} {
			t.Run(fmt.Sprintf("amnezia=%t/slept=%t", amnezia, slept), func(t *testing.T) {
				var asleep atomic.Int64
				readClocks = func() (time.Time, time.Duration) {
					wall, monotonic := previousClocks()
					return wall.Add(time.Duration(asleep.Load())), monotonic
				}
				clientKey, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				clientTun, clientNet, _, err := CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.29.0.2")}, nil, 1280, true)
				if err != nil {
					t.Fatal(err)
				}
				serverTun, _, serverStack, err := CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.29.0.1")}, nil, 1280, true)
				if err != nil {
					clientTun.Close()
					t.Fatal(err)
				}
				serverSocket, err := net.ListenPacket("udp4", "127.0.0.1:0")
				if err != nil {
					clientTun.Close()
					serverTun.Close()
					t.Fatal(err)
				}
				received := &initiationCounter{PacketConn: serverSocket, amnezia: amnezia}
				serverBind := &bind{preserveHeader: amnezia, listenFunc: func() (net.PacketConn, error) { return received, nil }}
				var server tunnelDevice
				settings := map[string]string{}
				if amnezia {
					server = amneziaDevice{Device: awg.NewDevice(adaptAmneziaTUN(serverTun), &amneziaBind{serverBind}, &awg.Logger{Verbosef: func(string, ...any) {}, Errorf: t.Logf})}
					settings = map[string]string{"jc": "2", "jmin": "10", "jmax": "20", "h1": "100000-100100", "h2": "200000-200100", "h3": "300000-300100", "h4": "400000-400100"}
				} else {
					server = wireguardDevice{Device: device.NewDevice(serverTun, serverBind, &device.Logger{Verbosef: func(string, ...any) {}, Errorf: t.Logf})}
				}
				defer serverSocket.Close()
				defer server.Close()
				serverConfig := "private_key=" + hex.EncodeToString(serverKey.Bytes()) + "\n"
				for key, value := range settings {
					serverConfig += key + "=" + value + "\n"
				}
				if err := server.IpcSet(serverConfig + "public_key=" + hex.EncodeToString(clientKey.PublicKey().Bytes()) + "\nallowed_ip=10.29.0.2/32\n"); err != nil {
					t.Fatal(err)
				}
				if err := server.Up(); err != nil {
					t.Fatal(err)
				}
				handler := &Handler{conf: &DeviceConfig{SecretKey: hex.EncodeToString(clientKey.Bytes()), Amnezia: amnezia, AmneziaSettings: settings,
					Peers: []*PeerConfig{{PublicKey: hex.EncodeToString(serverKey.PublicKey().Bytes()), Endpoint: serverSocket.LocalAddr().String(), AllowedIps: []string{"0.0.0.0/0"}}}},
					tun: clientTun, tnet: clientNet, streamSettings: &internet.MemoryStreamConfig{}}
				defer handler.Close()
				if err := handler.init(context.Background()); err != nil {
					t.Fatal(err)
				}
				remote := netip.MustParseAddrPort("10.29.0.1:18187")
				full, family := convertToFullAddr(remote)
				listener, err := gonet.ListenTCP(serverStack, full, family)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(10 * time.Second))
					io.Copy(conn, conn)
				}()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				connection, err := clientNet.DialContextTCPAddrPort(ctx, remote)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Close()
				connection.SetDeadline(time.Now().Add(10 * time.Second))
				roundTrip := func(payload string) {
					t.Helper()
					if _, err := connection.Write([]byte(payload)); err != nil {
						t.Fatal(err)
					}
					reply := make([]byte, len(payload))
					if _, err := io.ReadFull(connection, reply); err != nil {
						t.Fatal(err)
					}
					if string(reply) != payload {
						t.Fatal("tunneled payload changed")
					}
				}
				roundTrip("before the sleep")
				handshakes := received.count.Load()
				if handshakes == 0 {
					t.Fatal("the counter saw no handshake initiation")
				}
				// A peer takes one initiation per HandshakeInitationRate; a real sleep comes minutes
				// after the last handshake, not milliseconds.
				time.Sleep(2 * device.HandshakeInitationRate)
				if slept {
					asleep.Store(int64(300 * time.Second))
				}
				for deadline := time.Now().Add(time.Second); received.count.Load() == handshakes && time.Now().Before(deadline); {
					time.Sleep(10 * time.Millisecond)
				}
				if sent := received.count.Load() - handshakes; slept && sent == 0 {
					t.Fatal("no handshake initiation reached the peer within 1 s of waking")
				} else if !slept && sent != 0 {
					t.Fatalf("%d handshake initiations reached the peer without a sleep", sent)
				}
				// Well before the engine's own retry after RekeyTimeout (5 s).
				connection.SetDeadline(time.Now().Add(3 * time.Second))
				roundTrip("after the sleep")
			})
		}
	}
}
