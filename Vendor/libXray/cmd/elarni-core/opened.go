//go:build windows || linux

package main

import (
	"net/netip"
	"time"

	"github.com/xtls/libxray/share"
	"github.com/xtls/libxray/xray"
	"github.com/xtls/xray-core/common/flightrecorder"
)

// openedSettle is how long after the tunnel is up a connection that was being opened outside it
// may still complete.
const openedSettle = time.Second

// moveOpenedConnections resets the listed apps' connections that were open before the tunnel,
// so they reconnect through it; the connections view lists them. The tunnel's own addresses
// are its gateways (share.WindowsTunnelConfig, also on Linux).
//
// routed is set on Windows, which keeps every program's connection on the network it was opened
// on: see xray.MoveOpenedConnections. A second pass then takes those whose handshake ended after
// the first.
//
// since is set after a reload, to the per-app gate it replaced: only the programs the reload put
// into the tunnel are looked at, see xray.MoveOpenedConnectionsSince.
func moveOpenedConnections(routed func(netip.Addr) bool, since *xray.PerAppGate) {
	tunnel := make([]netip.Prefix, 0, 2)
	for _, gateway := range []string{share.TunGateway4, share.TunGateway6} {
		if prefix, err := netip.ParsePrefix(gateway); err == nil {
			tunnel = append(tunnel, prefix.Masked())
		}
	}
	passes := 1
	if routed != nil {
		passes = 2
	}
	for pass := range passes {
		if pass > 0 {
			time.Sleep(openedSettle)
		}
		reset, kept := xray.MoveOpenedConnectionsSince(tunnel, routed, since)
		if flightrecorder.Enabled() && reset+kept > 0 {
			flightrecorder.Record(flightrecorder.Event{Name: "opened_connections", Values: map[string]int64{"reset": int64(reset), "kept": int64(kept)}})
		}
	}
}
