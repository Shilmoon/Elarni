package splithttp

import (
	"context"
	"time"

	"github.com/apernet/quic-go/http3"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
)

// SetSetupTimeout lets tests exercise the establishment bound without waiting the real budget.
func SetSetupTimeout(d time.Duration) (restore func()) {
	previous := setupTimeout
	setupTimeout = d
	return func() { setupTimeout = previous }
}

// SetPathChangeTiming shortens how long a connection may stay silent after a network change, and how long changes coalesce.
func SetPathChangeTiming(bound, window time.Duration) (restore func()) {
	pathChange.Lock()
	defer pathChange.Unlock()
	previousBound, previousWindow := pathCheckBound, pathChangeWindow
	pathCheckBound, pathChangeWindow = bound, window
	return func() {
		pathChange.Lock()
		defer pathChange.Unlock()
		pathCheckBound, pathChangeWindow = previousBound, previousWindow
	}
}

// DialQUIC runs the HTTP/3 dial of a new XHTTP client for dest and settings, as its first request
// would, and returns the dial's error. A request bounded by the same setup time fails with its
// own error first.
func DialQUIC(ctx context.Context, dest net.Destination, settings *internet.MemoryStreamConfig) error {
	releaseCtx, release := context.WithCancel(context.Background())
	defer release()
	transport := createHTTPClient(releaseCtx, dest, settings).(*DefaultDialerClient).client.Transport.(*http3.Transport)
	conn, err := transport.Dial(ctx, dest.NetAddr(), transport.TLSClientConfig.Clone(), transport.QUICConfig)
	if conn != nil {
		conn.CloseWithError(0, "")
	}
	return err
}

// XmuxConnClosed reports whether XMUX holds a connection for dest and settings that it would retire as closed.
func XmuxConnClosed(dest net.Destination, settings *internet.MemoryStreamConfig) bool {
	globalDialerAccess.Lock()
	defer globalDialerAccess.Unlock()
	for _, client := range globalDialerMap[dialerConf{dest, settings}].xmuxClients {
		if client.XmuxConn.IsClosed() {
			return true
		}
	}
	return false
}
