package tun

import (
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
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

// A tun that keeps failing is read at longer and longer intervals instead of on a whole core, and Close of the
// stack ends the wait for the next read; so does an error that comes after Close.
func TestStackBacksOffATunThatKeepsFailingUntilClose(t *testing.T) {
	for name, segments := range map[string]bool{"packets": false, "segments": true} {
		t.Run(name, func(t *testing.T) {
			l := newLink()
			l.segments, l.errs = segments, make(chan error)
			s := newUnstartedStack(t, l, nil)
			defer l.Close()
			ended := reading(s)
			var taken atomic.Int32
			stop := make(chan struct{})
			defer close(stop)
			go func() {
				for {
					select {
					case l.errs <- errTunHiccup:
						taken.Add(1)
					case <-stop:
						return
					}
				}
			}()

			time.Sleep(400 * time.Millisecond)
			// 2 reads at once, then one after each wait of 5, 10, 20, 40, 80 and 160 ms; a loop that spins makes millions
			if n := taken.Load(); n < 2 || n > 20 {
				t.Fatalf("%d reads of a failing tun in 400 ms", n)
			}
			closed := time.Now()
			s.Close()
			ends(t, ended, 5*time.Second, "after the stack was closed")
			if waited := time.Since(closed); waited > 100*time.Millisecond {
				t.Fatalf("the reading ended %v after Close, not when it was closed", waited)
			}
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
