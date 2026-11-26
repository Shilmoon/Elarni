package main

import (
	"bytes"
	"errors"
	"net/netip"
	"os"
	"slices"
	"time"
)

// fallbackRouteMetric is the route metric of a fallback route on the loopback interface.
// Windows ranks two routes to one prefix by route metric plus interface metric: the tunnel's
// routes have 0 on an adapter whose interface metric the core sets to 0, while the loopback
// interface has 75 by default, so a fallback route weighs about 9575 and the tunnel's route wins
// for as long as its adapter exists. Windows' own loopback routes (127.0.0.0/8, ::1/128,
// multicast, the limited broadcast) have 256, which tells them from these: they are never removed.
const fallbackRouteMetric = 9500

// fallbackRoutes are the routes Block traffic without VPN adds on the loopback interface: each
// prefix the tunnel takes, again, where nothing leaves the PC. The guard's filters see only
// this PC's own connections, not the traffic it forwards for WSL2, Hyper-V guests or Mobile
// Hotspot clients, which follows the route table. Once the tunnel's adapter is gone (a restart,
// another server, a lost tunnel) these routes are still more specific than the physical default
// route, so that traffic is dropped instead of leaving outside the tunnel. Excluded routes are
// not among them and stay on the physical network. None without the kill switch.
// It has no build tag, so its tests run on every platform.
func fallbackRoutes(tunnel []netip.Prefix, block bool) []netip.Prefix {
	if !block {
		return nil
	}
	return slices.Clone(tunnel)
}

// fallbackRouteTable is the loopback interface's part of the route table.
type fallbackRouteTable interface {
	// add puts a fallback route to prefix in place; a route to it that is there already stays as it is.
	add(prefix netip.Prefix) error
	// remove deletes the fallback route to prefix. A missing one is no error, and a route
	// Windows put there itself stays.
	remove(prefix netip.Prefix) error
}

// guardMarkerFile is the guard's marker (.dns-guard). That it exists tells that this boot may
// hold a guard; its lines name the fallback routes that may be in place, one prefix each. An
// empty marker, which builds before the fallback routes wrote, names none. The routes do not
// outlive a restart, so those an earlier boot's marker names are gone, and removing them again
// finds nothing; the wall clock, which may be set after boot, does not decide it.
type guardMarkerFile struct{ path string }

func (marker guardMarkerFile) routes() []netip.Prefix {
	data, err := os.ReadFile(marker.path)
	if err != nil {
		return nil
	}
	var routes []netip.Prefix
	for _, line := range bytes.Split(data, []byte("\n")) {
		if prefix, err := netip.ParsePrefix(string(bytes.TrimSpace(line))); err == nil && prefix == prefix.Masked() {
			routes = append(routes, prefix)
		}
	}
	return routes
}

// write makes the marker name routes and dates it now: rewriting an empty marker left by an
// earlier boot may keep its old time.
func (marker guardMarkerFile) write(routes []netip.Prefix) error {
	var data []byte
	for _, prefix := range routes {
		data = append(append(data, prefix.String()...), '\n')
	}
	if err := os.WriteFile(marker.path, data, 0o644); err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(marker.path, now, now)
}

// placeFallbackRoutes puts a tunnel's fallback routes in place, before the tunnel adds its own.
// The marker names each one before it is added, so Repair finds every route a crash leaves. The
// routes an earlier tunnel left that this one has not (other excluded routes, the kill switch
// turned off) go once this one's are in place. Without the kill switch nothing is added and the
// marker stays as it is, its time included: protectDNS rewrites it once the guard is in place.
func placeFallbackRoutes(table fallbackRouteTable, marker guardMarkerFile, routes []netip.Prefix) error {
	listed := marker.routes()
	if len(routes) == 0 {
		return removeFallbackRoutes(table, listed)
	}
	kept := make(map[netip.Prefix]bool, len(routes)+len(listed))
	for _, prefix := range routes {
		kept[prefix] = true
	}
	var stale []netip.Prefix
	for _, prefix := range listed {
		if !kept[prefix] {
			kept[prefix] = true
			stale = append(stale, prefix)
		}
	}
	if err := marker.write(append(slices.Clone(stale), routes...)); err != nil {
		return err
	}
	for _, prefix := range routes {
		if err := table.add(prefix); err != nil {
			return err
		}
	}
	if len(stale) == 0 {
		return nil
	}
	if err := removeFallbackRoutes(table, stale); err != nil {
		return err
	}
	return marker.write(routes)
}

// clearFallbackRoutes removes the fallback routes the marker names, then the marker. When one
// cannot be removed the others still go and the marker stays, for the next try and for Repair.
func clearFallbackRoutes(table fallbackRouteTable, marker guardMarkerFile) error {
	if err := removeFallbackRoutes(table, marker.routes()); err != nil {
		return err
	}
	_ = os.Remove(marker.path)
	return nil
}

func removeFallbackRoutes(table fallbackRouteTable, routes []netip.Prefix) error {
	var failed []error
	for _, prefix := range routes {
		if err := table.remove(prefix); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}
