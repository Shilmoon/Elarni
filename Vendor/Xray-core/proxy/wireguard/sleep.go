package wireguard

import (
	"errors"
	"time"

	awg "github.com/amnezia-vpn/amneziawg-go/v3/device"
	"golang.zx2c4.com/wireguard/device"
)

// Elarni: both engines age a key by Go's monotonic clock, which stands still while the device
// sleeps (Linux, Android, macOS, iOS), and the server drops the key 180 s of real time after the
// handshake. After a long sleep the client sent with a key the server no longer had and noticed
// only after KeepaliveTimeout + RekeyTimeout (15 s) of awake time without an answer. A watcher
// compares the wall clock with the monotonic one and starts a new handshake instead.
const (
	// smallestSleep is the least difference between the two clocks' steps that counts as sleep.
	smallestSleep = 2 * time.Second
	// sleepWindow is one key lifetime as the engines see it: sleep is summed over this much
	// monotonic time.
	sleepWindow = device.RejectAfterTime
	// rekeyAfterSleep is how much sleep within sleepWindow starts a new handshake. Below it a
	// sending initiator renews its key (at RekeyAfterTime) before the server drops it.
	rekeyAfterSleep = device.RejectAfterTime - device.RekeyAfterTime
)

// sleepTick is how often the watcher reads the clocks; tests shorten it.
var sleepTick = 2 * time.Second

// monotonicBase anchors readings of the monotonic clock.
var monotonicBase = time.Now()

// readClocks returns the wall clock and the monotonic clock; tests replace it.
var readClocks = func() (wall time.Time, monotonic time.Duration) {
	now := time.Now()
	return now.Round(0), now.Sub(monotonicBase)
}

type sleepGap struct{ at, length time.Duration }

// sleepWatch sums the time the device slept from successive readings of both clocks.
type sleepWatch struct {
	started   bool
	wall      time.Time
	monotonic time.Duration
	gaps      []sleepGap // within the last sleepWindow of monotonic time
}

// observe takes one reading and reports whether the sleep within the last key lifetime reached
// rekeyAfterSleep; the count then starts again. A wall clock set back is no sleep; one set
// forward by a minute or more costs one handshake.
func (w *sleepWatch) observe(wall time.Time, monotonic time.Duration) bool {
	if !w.started {
		w.started, w.wall, w.monotonic = true, wall, monotonic
		return false
	}
	gap := wall.Sub(w.wall) - (monotonic - w.monotonic)
	w.wall, w.monotonic = wall, monotonic
	if gap >= smallestSleep {
		w.gaps = append(w.gaps, sleepGap{at: monotonic, length: gap})
	}
	var slept time.Duration
	kept := w.gaps[:0]
	for _, g := range w.gaps {
		if monotonic-g.at < sleepWindow {
			kept = append(kept, g)
			slept += g.length
		}
	}
	w.gaps = kept
	if slept >= rekeyAfterSleep {
		w.gaps = w.gaps[:0]
		return true
	}
	return false
}

// watchSleep reads the clocks every tick and calls rehandshake when the device slept long
// enough for the server to drop its keys, until done closes.
func watchSleep(done <-chan struct{}, tick time.Duration, clocks func() (time.Time, time.Duration), rehandshake func()) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	var watch sleepWatch
	watch.observe(clocks())
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if watch.observe(clocks()) {
				rehandshake()
			}
		}
	}
}

// wireguardDevice is the standard engine with the public keys (hex, as IpcSet takes them) of the
// peers it is configured with.
type wireguardDevice struct {
	*device.Device
	peers []string
}

// Rehandshake expires each peer's send keys and sends it a handshake initiation. Packets received
// with the old keys are still accepted; packets sent meanwhile wait for the new ones.
func (d wireguardDevice) Rehandshake() error {
	var errs []error
	for _, peer := range d.peers {
		var key device.NoisePublicKey
		if err := key.FromHex(peer); err != nil {
			errs = append(errs, err)
			continue
		}
		if p := d.LookupPeer(key); p != nil {
			p.ExpireCurrentKeypairs()
			errs = append(errs, p.SendHandshakeInitiation(false))
		}
	}
	return errors.Join(errs...)
}

// amneziaDevice is the AmneziaWG engine with the public keys of the peers it is configured with.
type amneziaDevice struct {
	*awg.Device
	peers []string
}

// Rehandshake is wireguardDevice.Rehandshake for the AmneziaWG engine.
func (d amneziaDevice) Rehandshake() error {
	var errs []error
	for _, peer := range d.peers {
		var key awg.NoisePublicKey
		if err := key.FromHex(peer); err != nil {
			errs = append(errs, err)
			continue
		}
		if p := d.LookupPeer(key); p != nil {
			p.ExpireCurrentKeypairs()
			errs = append(errs, p.SendHandshakeInitiation(false))
		}
	}
	return errors.Join(errs...)
}
