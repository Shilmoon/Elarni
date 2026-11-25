package retry

import "time"

// Backoff spaces out the attempts of a read loop that keeps failing, so an error that repeats at
// once does not spin a core. The first failure after a success is retried at once, as a one-off
// error is; from the second the wait starts at Min and doubles up to Max. One goroutine uses it.
type Backoff struct {
	Min, Max time.Duration
	next     time.Duration
	failed   bool
}

// Wait waits before the attempt that follows a failure. It returns false, at once, when done is
// closed first: the loop should end then.
func (b *Backoff) Wait(done <-chan struct{}) bool {
	if !b.failed {
		b.failed = true
		b.next = b.Min
		select {
		case <-done:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(b.next)
	defer timer.Stop()
	b.next = min(2*b.next, b.Max)
	select {
	case <-done:
		return false
	case <-timer.C:
		return true
	}
}

// Reset starts over after a success.
func (b *Backoff) Reset() {
	b.failed = false
}
