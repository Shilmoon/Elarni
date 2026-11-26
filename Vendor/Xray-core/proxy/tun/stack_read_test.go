package tun

import (
	"errors"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/retry"
)

var errTunHiccup = errors.New("no buffer space available")

// reading runs read of s and tells when it has ended
func reading(s *stack) <-chan struct{} {
	ended := make(chan struct{})
	go func() {
		s.read()
		close(ended)
	}()
	return ended
}

func ends(t *testing.T, ended <-chan struct{}, within time.Duration, what string) {
	t.Helper()
	select {
	case <-ended:
	case <-time.After(within):
		t.Fatal("the stack still reads ", what)
	}
}

// An error of the tun that did not say it was closed ended the reading for good, and with it every connection
// of the tunnel while the app still showed it connected. The stack reads on after such errors and ends only
// when the tun is closed.
func TestStackReadsOnAfterAnErrorOfTheTun(t *testing.T) {
	for name, segments := range map[string]bool{"packets": false, "segments": true} {
		t.Run(name, func(t *testing.T) {
			conns := make(chan accepted, 1)
			l := newLink()
			l.segments, l.errs = segments, make(chan error)
			s := newUnstartedStack(t, l, accept(conns))
			defer s.Close()
			ended := reading(s)

			// each send returns when a read has taken the error
			for range 3 {
				select {
				case l.errs <- errTunHiccup:
				case <-time.After(5 * time.Second):
					t.Fatal("the stack stopped reading after an error")
				}
			}
			src, dst := netip.MustParseAddrPort("10.0.0.1:1234"), netip.MustParseAddrPort("10.0.0.2:53")
			l.in <- udpPacket(src, dst, []byte("after"))
			a := take(t, conns)
			mb, err := a.conn.(buf.Reader).ReadMultiBuffer()
			if err != nil || mb.String() != "after" {
				t.Fatal("read ", mb.String(), " after the errors, error: ", err)
			}
			buf.ReleaseMulti(mb)
			a.conn.Close()

			l.Close()
			ends(t, ended, 5*time.Second, "after the tun was closed")
		})
	}
}

// A tun that keeps failing is read again only after a wait instead of at once, on a whole core, and Close of
// the stack ends the wait for the next read; so does an error that comes after Close.
func TestStackBacksOffATunThatKeepsFailingUntilClose(t *testing.T) {
	for name, segments := range map[string]bool{"packets": false, "segments": true} {
		t.Run(name, func(t *testing.T) {
			l := newLink()
			l.segments, l.errs = segments, make(chan error)
			s := newUnstartedStack(t, l, nil)
			// a wait that no test outlasts: only Close ends it
			s.readRetry = retry.Backoff{Min: time.Hour, Max: time.Hour}
			defer l.Close()
			ended := reading(s)

			// the first error is read again at once, as a one-off error is; the second one waits
			for range 2 {
				select {
				case l.errs <- errTunHiccup:
				case <-time.After(5 * time.Second):
					t.Fatal("the stack did not read again after one error")
				}
			}
			select {
			case l.errs <- errTunHiccup:
				t.Fatal("the stack read a tun that keeps failing again at once")
			case <-time.After(50 * time.Millisecond):
			}
			s.Close()
			ends(t, ended, 5*time.Second, "after the stack was closed")
		})
	}

	// the stack is closed first, as Handler.Close does it, and the tun answers with an error of its own
	l := newLink()
	l.errs = make(chan error, 1)
	s := newUnstartedStack(t, l, nil)
	defer l.Close()
	s.Close()
	l.errs <- errTunHiccup
	ends(t, reading(s), 5*time.Second, "after an error that came when the stack was closed")
}

// A tun whose device is gone answers every read with the same error: ENXIO or EBADF for a destroyed utun or
// tun, EBADF for an fd that Android's VpnService revoked. Reading it again went on for ever at the longest wait,
// with a warning each time; such an error ends the reading as a closed tun does.
func TestStackStopsReadingATunWhoseDeviceIsGone(t *testing.T) {
	if len(tunGoneErrors) == 0 {
		t.Skip("no error of this system's tun says that its device is gone")
	}
	for _, gone := range tunGoneErrors {
		for name, segments := range map[string]bool{"packets": false, "segments": true} {
			t.Run(gone.Error()+"/"+name, func(t *testing.T) {
				l := newLink()
				l.segments, l.errs = segments, make(chan error, 1)
				s := newUnstartedStack(t, l, nil)
				defer s.Close()
				defer l.Close()
				// as os.File gives it
				l.errs <- &os.PathError{Op: "read", Path: "/dev/tun", Err: gone}
				ends(t, reading(s), 5*time.Second, "after its device was gone")
			})
		}
	}
}

// An error that repeats is warned of the first time, then once a minute at most with how many times it came
// in between, instead of once a second for as long as it lasts.
func TestReadWarningsArePacedAndCounted(t *testing.T) {
	var w warnings
	start := time.Unix(1_000_000, 0)
	for _, step := range []struct {
		after time.Duration
		log   bool
		held  int
	}{
		{0, true, 0},
		{time.Second, false, 0},
		{30 * time.Second, false, 0},
		{59 * time.Second, false, 0},
		{time.Minute, true, 3},
		{time.Minute + time.Second, false, 0},
		{10 * time.Minute, true, 1},
		{11 * time.Minute, true, 0},
	} {
		log, held := w.pass(start.Add(step.after), time.Minute)
		if log != step.log || held != step.held {
			t.Fatalf("after %v: logged %v with %d held back, want %v with %d", step.after, log, held, step.log, step.held)
		}
	}
}
