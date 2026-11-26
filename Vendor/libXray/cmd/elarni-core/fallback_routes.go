package main

import (
	"bytes"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"slices"
	"sync"
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

// placedFallbackRoutes is the route table as this helper process knows it: the fallback routes it
// put in place, or found in place when it added them, and has not tried to remove since. A later
// placement in the same process (a server switch, a restart's hold) adds only the others, instead
// of every route again (up to 8,192 calls during a connect). The marker cannot tell this: it names
// the routes that may be in place, an earlier helper's, those a failed add left out and those a
// restart of Windows removed, so routes known only from it are added as before. Only the holder
// of the guard's claim changes the routes, so the process forgets them once it gives the claim up.
type placedFallbackRoutes struct {
	table  fallbackRouteTable
	mu     sync.Mutex
	placed map[netip.Prefix]bool
}

func (routes *placedFallbackRoutes) add(prefix netip.Prefix) error {
	routes.mu.Lock()
	defer routes.mu.Unlock()
	if routes.placed[prefix] {
		return nil
	}
	if err := routes.table.add(prefix); err != nil {
		return err
	}
	if routes.placed == nil {
		routes.placed = map[netip.Prefix]bool{}
	}
	routes.placed[prefix] = true
	return nil
}

// remove forgets the route before it tries: a removal that fails may still have taken it.
func (routes *placedFallbackRoutes) remove(prefix netip.Prefix) error {
	routes.mu.Lock()
	defer routes.mu.Unlock()
	delete(routes.placed, prefix)
	return routes.table.remove(prefix)
}

// forget drops what this process knows, for when another one may change the routes.
func (routes *placedFallbackRoutes) forget() {
	routes.mu.Lock()
	defer routes.mu.Unlock()
	clear(routes.placed)
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

// present tells whether the marker is there. Only one that surely is not names no guard: an
// earlier helper's guard may hold whenever it cannot be told.
func (marker guardMarkerFile) present() bool {
	_, err := os.Stat(marker.path)
	return !errors.Is(err, fs.ErrNotExist)
}

// fallbackPlacement is what placeFallbackRoutes did for one start.
type fallbackPlacement struct {
	// named are the routes the marker names once the guard is in place (protectDNS writes them):
	// the tunnel's, and without the kill switch those of an earlier tunnel that could not go.
	named []netip.Prefix
	// added are the tunnel's routes the marker did not name before this start, the only ones a
	// start that fails takes back.
	added []netip.Prefix
	// marked: the marker was there before this start, so an earlier helper's guard may hold.
	marked bool
	// left says why, without the kill switch, an earlier tunnel's routes could not all go. It
	// refuses no connect: named keeps them, for the stop that releases the guard, a later start
	// and Repair.
	left error
}

// placeFallbackRoutes puts a tunnel's fallback routes in place, before the tunnel adds its own.
// The marker names each one before it is added, so Repair finds every route a crash leaves. The
// routes an earlier tunnel left that this one has not (other excluded routes, the kill switch
// turned off) go once this one's are in place. Without the kill switch nothing is added and the
// marker stays as it is, its time included: protectDNS rewrites it once the guard is in place.
// The placement is filled in on failure too, for undoFallbackRoutes.
func placeFallbackRoutes(table fallbackRouteTable, marker guardMarkerFile, routes []netip.Prefix) (fallbackPlacement, error) {
	listed := marker.routes()
	if len(routes) == 0 {
		// A route that cannot go is no reason to refuse a connect without the kill switch.
		left, err := removeFallbackRoutes(table, listed)
		return fallbackPlacement{named: left, left: err}, nil
	}
	wanted := make(map[netip.Prefix]bool, len(routes))
	for _, prefix := range routes {
		wanted[prefix] = true
	}
	before := make(map[netip.Prefix]bool, len(listed))
	var stale []netip.Prefix
	for _, prefix := range listed {
		if !before[prefix] {
			before[prefix] = true
			if !wanted[prefix] {
				stale = append(stale, prefix)
			}
		}
	}
	marked := marker.present()
	if err := marker.write(append(slices.Clone(stale), routes...)); err != nil {
		return fallbackPlacement{}, err
	}
	placement := fallbackPlacement{named: routes, marked: marked}
	for _, prefix := range routes {
		if !before[prefix] {
			placement.added = append(placement.added, prefix)
		}
	}
	for _, prefix := range routes {
		if err := table.add(prefix); err != nil {
			return placement, err
		}
	}
	if len(stale) == 0 {
		return placement, nil
	}
	if _, err := removeFallbackRoutes(table, stale); err != nil {
		return placement, err
	}
	return placement, marker.write(routes)
}

// undoFallbackRoutes takes back, after a start that failed before its guard was in place, the
// routes placeFallbackRoutes added for it: those the marker did not name before. A guard an
// earlier helper left stays, and so do the routes its marker named: with Block traffic without
// VPN the app keeps a crashed helper's guard until a tunnel replaces it. A marker that was not
// there before goes with the routes; otherwise it names those left. One that cannot be removed
// stays named, for a later start, a stop and Repair.
func undoFallbackRoutes(table fallbackRouteTable, marker guardMarkerFile, placement fallbackPlacement) error {
	if len(placement.added) == 0 {
		return nil
	}
	left, err := removeFallbackRoutes(table, placement.added)
	gone := make(map[netip.Prefix]bool, len(placement.added))
	for _, prefix := range placement.added {
		gone[prefix] = true
	}
	for _, prefix := range left {
		gone[prefix] = false
	}
	named := slices.DeleteFunc(marker.routes(), func(prefix netip.Prefix) bool { return gone[prefix] })
	if !placement.marked && len(named) == 0 {
		if removed := os.Remove(marker.path); removed != nil && !errors.Is(removed, fs.ErrNotExist) {
			return errors.Join(err, removed)
		}
		return err
	}
	return errors.Join(err, marker.write(named))
}

// clearFallbackRoutes removes the fallback routes the marker names, then the marker. When one
// cannot be removed the others still go and the marker stays, for the next try and for Repair.
func clearFallbackRoutes(table fallbackRouteTable, marker guardMarkerFile) error {
	if _, err := removeFallbackRoutes(table, marker.routes()); err != nil {
		return err
	}
	_ = os.Remove(marker.path)
	return nil
}

// removeFallbackRoutes tries each route; left are those that could not be removed.
func removeFallbackRoutes(table fallbackRouteTable, routes []netip.Prefix) (left []netip.Prefix, err error) {
	var failed []error
	for _, prefix := range routes {
		if err := table.remove(prefix); err != nil {
			left = append(left, prefix)
			failed = append(failed, err)
		}
	}
	return left, errors.Join(failed...)
}
