package xray

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/xtls/libxray/nodep"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
)

// PingRunningCore checks a target through the running core's routing, the way app
// traffic goes, without a listener login or a second core.
func PingRunningCore(ctx context.Context, timeout int, url, method string, expected *nodep.ExpectedResponse) (int64, error) {
	client, err := runningCoreClient(time.Duration(timeout) * time.Second)
	if err != nil {
		return nodep.PingDelayError, err
	}
	return nodep.PingHTTPRequestContext(ctx, client, url, timeout, method, expected)
}

// TransferRunningCore runs a transfer check through the running core's routing, over one connection.
func TransferRunningCore(ctx context.Context, url string, limits nodep.TransferLimits) (nodep.TransferResult, error) {
	client, err := runningCoreClient(0)
	if err != nil {
		return nodep.TransferResult{Verdict: nodep.TransferInconclusive, Err: err}, err
	}
	return nodep.TransferHTTPRequestContext(ctx, client, url, limits), nil
}

// runningCoreClient dials through the running core, a new connection for each request.
func runningCoreClient(timeout time.Duration) (*http.Client, error) {
	coreServerMu.Lock()
	server := coreServer
	coreServerMu.Unlock()
	if server == nil {
		return nil, errors.New("not_connected")
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			destination, err := xnet.ParseDestination(network + ":" + address)
			if err != nil {
				return nil, err
			}
			return core.Dial(ctx, server, destination)
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}
