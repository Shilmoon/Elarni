package wireguard

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	awg "github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/xtls/xray-core/common/errors"
	"golang.zx2c4.com/wireguard/device"
)

// Elarni: both engines age a key by Go's monotonic clock, which stands still while the device
// sleeps (Linux, Android, macOS, iOS), and the server drops the key 180 s of real time after the
// handshake. After a long sleep the client sent with a key the server no longer had and noticed
// only after KeepaliveTimeout + RekeyTimeout (15 s) of awake time without an answer. The bind compares
// the wall clock with the monotonic one as it sends, and the handler before each dial; after enough
// sleep since a peer's last handshake the peer's send keys expire, so the next packet to it starts a
// handshake. An idle tunnel reads no clock and sends nothing on waking.
const (
	// smallestSleep is the least difference between the two clocks' steps that counts as sleep.
	smallestSleep = 2 * time.Second
	// sleepWindow is one key lifetime as the engines see it: sleep is summed over this much
	// monotonic time.
	sleepWindow = device.RejectAfterTime
	// rekeyAfterSleep is how much sleep since a peer's last handshake expires its keys. Below it a
	// sending initiator renews its key (at RekeyAfterTime) before the server drops it.
	rekeyAfterSleep = device.RejectAfterTime - device.RekeyAfterTime
)

// monotonicBase anchors readings of the monotonic clock.
var monotonicBase = time.Now()

// readClocks returns the wall clock and the monotonic clock; tests replace it.
var readClocks = func() (wall time.Time, monotonic time.Duration) {
	now := time.Now()
	return now.Round(0), now.Sub(monotonicBase)
}

// clockSkew is how far the wall clock is ahead of the monotonic one; sleep moves it forward.
func clockSkew(wall time.Time, monotonic time.Duration) time.Duration {
	return wall.Sub(monotonicBase.Round(0)) - monotonic
}

// sleepGap is one sleep, with the time it was seen to end by both clocks.
type sleepGap struct {
	wall              time.Time
	monotonic, length time.Duration
}

// sleepWatch tells the device to expire the send keys of the peers whose last handshake the server
// may have dropped while the device slept.
type sleepWatch struct {
	clocks func() (time.Time, time.Duration)
	state  func() (string, error) // the engine's IpcGet
	expire func(peers [][32]byte)
	peers  [][32]byte

	skew      atomic.Int64 // clockSkew when the last check ended
	scheduled atomic.Bool  // a check from sending runs or is about to

	run     sync.Mutex // guards stopped and the Add of checks
	stopped bool
	checks  sync.WaitGroup

	mu        sync.Mutex // held through a check
	reference time.Duration
	gaps      []sleepGap // within the last sleepWindow of monotonic time
	renewed   map[[32]byte]time.Time
}

func newSleepWatch(clocks func() (time.Time, time.Duration), state func() (string, error), expire func([][32]byte), peers [][32]byte) *sleepWatch {
	w := &sleepWatch{clocks: clocks, state: state, expire: expire, peers: peers, renewed: map[[32]byte]time.Time{}}
	w.reference = clockSkew(clocks())
	w.skew.Store(int64(w.reference))
	return w
}

// moved reports whether the clocks moved apart by smallestSleep or more since the last check.
func (w *sleepWatch) moved() bool {
	moved := clockSkew(w.clocks()) - time.Duration(w.skew.Load())
	return moved >= smallestSleep || moved <= -smallestSleep
}

// sending checks for sleep as the bind sends. The engine holds its network lock there, which its
// state also takes, so the check runs on its own goroutine; the datagram goes out meanwhile.
func (w *sleepWatch) sending() {
	if !w.moved() || !w.scheduled.CompareAndSwap(false, true) {
		return
	}
	w.run.Lock()
	defer w.run.Unlock()
	if w.stopped {
		w.scheduled.Store(false)
		return
	}
	w.checks.Add(1)
	go func() {
		defer w.checks.Done()
		defer w.scheduled.Store(false)
		w.check()
	}()
}

// dialing checks for sleep before a dial, and waits for a check that sending started, so that the
// dial's first packet already waits for the new handshake.
func (w *sleepWatch) dialing() {
	if w.moved() {
		w.check()
	}
}

// stop waits for the checks that sending started and starts no more.
func (w *sleepWatch) stop() {
	w.run.Lock()
	w.stopped = true
	w.run.Unlock()
	w.checks.Wait()
}

func (w *sleepWatch) check() {
	w.mu.Lock()
	defer w.mu.Unlock()
	wall, monotonic := w.clocks()
	skew := clockSkew(wall, monotonic)
	defer w.skew.Store(int64(skew))
	moved := skew - w.reference
	w.reference = skew
	// A wall clock set back is no sleep; one set forward counts as one.
	if moved < smallestSleep {
		return
	}
	w.gaps = append(w.gaps, sleepGap{wall: wall, monotonic: monotonic, length: moved})
	var slept time.Duration
	kept := w.gaps[:0]
	for _, g := range w.gaps {
		if monotonic-g.monotonic < sleepWindow {
			kept = append(kept, g)
			slept += g.length
		}
	}
	w.gaps = kept
	if slept < rekeyAfterSleep {
		return
	}
	state, err := w.state()
	if err != nil {
		errors.LogDebugInner(context.Background(), err, "WireGuard state after sleep")
		return
	}
	handshakes := lastHandshakes(state)
	var due [][32]byte
	for _, peer := range w.peers {
		last, ok := handshakes[peer]
		if !ok {
			continue // no keys to expire; a first handshake may be under way
		}
		since := last
		if renewed := w.renewed[peer]; renewed.After(since) {
			since = renewed
		}
		var sleptSinceKey, sleptSinceRenewal time.Duration
		for _, g := range w.gaps {
			if g.wall.After(last) {
				sleptSinceKey += g.length
			}
			if g.wall.After(since) {
				sleptSinceRenewal += g.length
			}
		}
		// From RekeyAfterTime of monotonic age the engine renews its own key as it sends, and an
		// expiry could drop the handshake it has under way.
		if sleptSinceRenewal >= rekeyAfterSleep && wall.Sub(last)-sleptSinceKey < device.RekeyAfterTime {
			due = append(due, peer)
		}
	}
	if len(due) > 0 {
		w.expire(due)
		for _, peer := range due {
			w.renewed[peer] = wall
		}
	}
}

// lastHandshakes reads the wall time of each peer's last handshake from the engine's IpcGet state,
// which both engines write alike; a peer without one is left out.
func lastHandshakes(state string) map[[32]byte]time.Time {
	handshakes := map[[32]byte]time.Time{}
	var peer [32]byte
	var seconds int64
	for _, line := range strings.Split(state, "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "public_key":
			peer, seconds = [32]byte{}, 0
			hex.Decode(peer[:], []byte(value))
		case "last_handshake_time_sec":
			seconds, _ = strconv.ParseInt(value, 10, 64)
		case "last_handshake_time_nsec":
			if nanoseconds, _ := strconv.ParseInt(value, 10, 64); seconds != 0 || nanoseconds != 0 {
				handshakes[peer] = time.Unix(seconds, nanoseconds)
			}
		}
	}
	return handshakes
}

// expireSendKeys expires the send keys of each of peers that lookup finds, in either engine. The
// next packet to such a peer starts a handshake; packets received with the old keys are still taken.
func expireSendKeys[K ~[32]byte, P interface {
	comparable
	ExpireCurrentKeypairs()
}](lookup func(K) P, peers [][32]byte) {
	var none P
	for _, key := range peers {
		if peer := lookup(K(key)); peer != none {
			peer.ExpireCurrentKeypairs()
		}
	}
}

// wireguardDevice is the standard engine as a tunnelDevice.
type wireguardDevice struct{ *device.Device }

func (d wireguardDevice) ExpireKeys(peers [][32]byte) { expireSendKeys(d.LookupPeer, peers) }

// amneziaDevice is the AmneziaWG engine as a tunnelDevice.
type amneziaDevice struct{ *awg.Device }

func (d amneziaDevice) ExpireKeys(peers [][32]byte) { expireSendKeys(d.LookupPeer, peers) }
