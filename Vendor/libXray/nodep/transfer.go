package nodep

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Verdicts of a transfer check.
const (
	// TransferOK: the pass threshold arrived.
	TransferOK = "ok"
	// TransferStalled: the answer started, then no byte came for the idle limit before the threshold.
	TransferStalled = "stalled"
	// TransferInconclusive: the check says nothing about a freeze (connect, TLS or status failure, a short
	// body, a reset, or the total limit while bytes still came).
	TransferInconclusive = "inconclusive"
)

// TransferLimits bound one transfer check.
type TransferLimits struct {
	// Total limit on the body, from the answer's headers to the last byte. The caller's context bounds the
	// connection and the request before them, so a slow answer leaves the idle limit its whole time.
	Total time.Duration
	// Idle is how long the body may send nothing once the answer started.
	Idle time.Duration
	// Pass is the number of body bytes that pass the check.
	Pass int64
}

// DefaultTransferLimits: 96 KB is about 65 full packets, above every per-connection budget reported for
// frozen hosting networks (about 16 KB, 11 KB on mobile), which a small latency probe fits inside.
var DefaultTransferLimits = TransferLimits{Total: 8 * time.Second, Idle: 4 * time.Second, Pass: 96_000}

// TransferResult is what one transfer check saw.
type TransferResult struct {
	Verdict string
	// Bytes of the body received.
	Bytes int64
	// Elapsed time of the check, in milliseconds.
	Elapsed int64
	// Err is why an inconclusive check ended, if it failed.
	Err error
}

// MeasureTransferContext fetches url over one connection, through proxy when it is set (like
// "socks5://127.0.0.1:1080"), and counts the body bytes.
func MeasureTransferContext(ctx context.Context, url, proxy string, limits TransferLimits) TransferResult {
	c, err := coreHTTPClient(0, proxy, false)
	if err != nil {
		return TransferResult{Verdict: TransferInconclusive, Err: err}
	}
	defer c.CloseIdleConnections()
	return TransferHTTPRequestContext(ctx, c, url, limits)
}

// TransferHTTPRequestContext runs one transfer check with the caller's client, such as one that dials through a
// running core. The client must not follow redirects or reuse connections.
func TransferHTTPRequestContext(parent context.Context, c *http.Client, url string, limits TransferLimits) TransferResult {
	start := time.Now()
	result := func(verdict string, bytes int64, err error) TransferResult {
		return TransferResult{Verdict: verdict, Bytes: bytes, Elapsed: time.Since(start).Milliseconds(), Err: err}
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return result(TransferInconclusive, 0, err)
	}
	// A compressed body could end below the threshold; this also keeps Go from decompressing it.
	req.Header.Set("Accept-Encoding", "identity")
	response, err := c.Do(req)
	if err != nil {
		return result(TransferInconclusive, 0, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result(TransferInconclusive, 0, errUnexpectedStatus)
	}
	if response.ContentLength >= 0 && response.ContentLength < limits.Pass {
		return result(TransferInconclusive, 0, errShortBody)
	}
	// The status line arrived: from here a silence of the idle limit is a stall, and the total limit runs.
	answered := time.Now()
	total := time.AfterFunc(limits.Total, cancel)
	defer total.Stop()
	// Times since the answer, in nanoseconds: of the last byte read, and of the silence that ended the check
	// (-1 while none did). The watchdog reads the first, so a byte that arrives as it fires is not missed.
	var lastByte, stalledAt atomic.Int64
	stalledAt.Store(-1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		timer := time.NewTimer(limits.Idle)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			now := time.Since(answered)
			if silent := now - time.Duration(lastByte.Load()); silent < limits.Idle {
				timer.Reset(limits.Idle - silent)
				continue
			}
			stalledAt.Store(int64(now))
			cancel()
			return
		}
	}()
	var received int64
	buffer := make([]byte, 32*1024)
	for received < limits.Pass {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			received += int64(n)
			lastByte.Store(int64(time.Since(answered)))
		}
		if err != nil {
			if received >= limits.Pass {
				break
			}
			// A stall only when no byte came within the idle limit before the watchdog ended the check:
			// one read just as it fired leaves the check inconclusive instead.
			if at := stalledAt.Load(); at >= 0 && time.Duration(at-lastByte.Load()) >= limits.Idle {
				return result(TransferStalled, received, nil)
			}
			if err == io.EOF {
				err = errShortBody
			}
			return result(TransferInconclusive, received, err)
		}
	}
	return result(TransferOK, received, nil)
}

type transferError string

func (e transferError) Error() string { return string(e) }

const (
	errUnexpectedStatus = transferError("unexpected_response_status")
	errShortBody        = transferError("short_response_body")
)
