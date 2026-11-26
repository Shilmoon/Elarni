package main

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xtls/libxray/share"
)

func prefixes(values ...string) []netip.Prefix {
	list := make([]netip.Prefix, len(values))
	for i, value := range values {
		list[i] = netip.MustParsePrefix(value)
	}
	return list
}

// Regression (WSL2, Hyper-V and Mobile Hotspot traffic): the guard's filters see only this
// PC's own connections, so what it forwarded left by the physical default route whenever the
// tunnel's routes were gone. Every prefix the tunnel takes gets a fallback route, both IP versions.
func TestFallbackRoutesAreTheTunnelsRoutesOnlyWithTheKillSwitch(t *testing.T) {
	block := &share.WindowsRoutePolicy{IncludedRoutes: []string{"0.0.0.0/0", "::/0"}, BlockWithoutVPN: true}
	tunnel, err := share.WindowsRoutes(block)
	if err != nil {
		t.Fatal(err)
	}
	fallback := fallbackRoutes(tunnel, true)
	if !slices.Equal(fallback, tunnel) {
		t.Fatalf("fallback routes %v, want the tunnel's %v", fallback, tunnel)
	}
	for _, prefix := range prefixes("0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1") {
		if !slices.Contains(fallback, prefix) {
			t.Errorf("no fallback route to %s in %v", prefix, fallback)
		}
	}
	fallback[0] = netip.MustParsePrefix("192.0.2.0/24")
	if tunnel[0] == fallback[0] {
		t.Fatal("the fallback routes share the tunnel's list")
	}
	// Excluded routes stay on the physical network, the LAN and the user's exclusions alike.
	block.ExcludedRoutes = []string{"192.168.0.0/16", "198.51.100.0/24", "fd00::/8"}
	if tunnel, err = share.WindowsRoutes(block); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range fallbackRoutes(tunnel, true) {
		for _, excluded := range prefixes(block.ExcludedRoutes...) {
			if prefix.Overlaps(excluded) {
				t.Errorf("fallback route %s covers the excluded %s", prefix, excluded)
			}
		}
	}
	if got := fallbackRoutes(tunnel, false); len(got) != 0 {
		t.Fatalf("fallback routes without the kill switch: %v", got)
	}
}

// fakeLoopbackTable is the loopback interface's routes, by prefix, with their metric. Like
// Windows' table, it keeps a route that exists already and removes only the helper's own.
type fakeLoopbackTable struct {
	marker   guardMarkerFile
	routes   map[netip.Prefix]uint32
	fail     map[netip.Prefix]error
	calls    []string
	unlisted []netip.Prefix // added while the marker did not name them
}

func newFakeLoopbackTable(marker guardMarkerFile, routes map[netip.Prefix]uint32) *fakeLoopbackTable {
	if routes == nil {
		routes = map[netip.Prefix]uint32{}
	}
	return &fakeLoopbackTable{marker: marker, routes: routes, fail: map[netip.Prefix]error{}}
}

func (table *fakeLoopbackTable) add(prefix netip.Prefix) error {
	table.calls = append(table.calls, "add "+prefix.String())
	if err := table.fail[prefix]; err != nil {
		return err
	}
	if !slices.Contains(table.marker.routes(), prefix) {
		table.unlisted = append(table.unlisted, prefix)
	}
	if _, found := table.routes[prefix]; !found {
		table.routes[prefix] = fallbackRouteMetric
	}
	return nil
}

func (table *fakeLoopbackTable) remove(prefix netip.Prefix) error {
	table.calls = append(table.calls, "remove "+prefix.String())
	if err := table.fail[prefix]; err != nil {
		return err
	}
	if table.routes[prefix] == fallbackRouteMetric {
		delete(table.routes, prefix)
	}
	return nil
}

func testGuardMarker(t *testing.T) guardMarkerFile {
	return guardMarkerFile{path: filepath.Join(t.TempDir(), ".dns-guard")}
}

func TestGuardMarkerNamesTheFallbackRoutes(t *testing.T) {
	marker := testGuardMarker(t)
	if got := marker.routes(); got != nil {
		t.Fatalf("a missing marker names %v", got)
	}
	routes := prefixes("0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1")
	if err := marker.write(routes); err != nil {
		t.Fatal(err)
	}
	if got := marker.routes(); !slices.Equal(got, routes) {
		t.Fatalf("marker names %v, want %v", got, routes)
	}
	// Builds before the fallback routes write an empty marker; it still marks a guard and names none.
	if err := marker.write(nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(marker.path); err != nil || info.Size() != 0 {
		t.Fatalf("a marker without routes is not empty: %v", err)
	}
	if got := marker.routes(); got != nil {
		t.Fatalf("an empty marker names %v", got)
	}
	if err := os.WriteFile(marker.path, []byte("0.0.0.0/1\nnot a prefix\n10.1.2.3/8\n\n::/1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := marker.routes(); !slices.Equal(got, prefixes("0.0.0.0/1", "::/1")) {
		t.Fatalf("a damaged marker names %v", got)
	}
	// The wall clock may be set forward after boot, so that this boot's marker looks older than
	// the boot: it still names its routes, or a release would leave them in place.
	if err := marker.write(routes); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(marker.path, old, old); err != nil {
		t.Fatal(err)
	}
	if got := marker.routes(); !slices.Equal(got, routes) {
		t.Fatalf("an old marker names %v, want %v", got, routes)
	}
}

func TestFallbackRoutesAreListedBeforeTheyAreAdded(t *testing.T) {
	marker := testGuardMarker(t)
	// Windows' own loopback route to a prefix the tunnel takes too (::1/128 when the VPN icon is hidden).
	own := netip.MustParsePrefix("::1/128")
	table := newFakeLoopbackTable(marker, map[netip.Prefix]uint32{own: 256})
	routes := prefixes("0.0.0.0/1", "128.0.0.0/1", "::1/128", "::/1", "8000::/1")
	if _, err := placeFallbackRoutes(table, marker, routes); err != nil {
		t.Fatal(err)
	}
	if len(table.unlisted) != 0 {
		t.Fatalf("added before the marker named them: %v", table.unlisted)
	}
	if got := marker.routes(); !slices.Equal(got, routes) {
		t.Fatalf("marker names %v, want %v", got, routes)
	}
	for _, prefix := range routes {
		if _, found := table.routes[prefix]; !found {
			t.Errorf("no fallback route to %s", prefix)
		}
	}
	if table.routes[own] != 256 {
		t.Fatalf("Windows' own route to %s was changed to metric %d", own, table.routes[own])
	}
	// The next tunnel of a restart finds them in place: that is no failure.
	if _, err := placeFallbackRoutes(table, marker, routes); err != nil {
		t.Fatal("routes in place failed the next tunnel:", err)
	}
	// Release takes the helper's routes and the marker, never Windows' own.
	if err := clearFallbackRoutes(table, marker); err != nil {
		t.Fatal(err)
	}
	if len(table.routes) != 1 || table.routes[own] != 256 {
		t.Fatalf("after release the loopback interface has %v", table.routes)
	}
	if _, err := os.Stat(marker.path); !os.IsNotExist(err) {
		t.Fatal("release kept the marker:", err)
	}
}

func TestFallbackRoutesWithoutTheKillSwitchChangeNothing(t *testing.T) {
	marker := testGuardMarker(t)
	table := newFakeLoopbackTable(marker, nil)
	if _, err := placeFallbackRoutes(table, marker, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker.path); !os.IsNotExist(err) || len(table.calls) != 0 {
		t.Fatalf("without the kill switch: marker %v, route calls %v", err, table.calls)
	}
	// An earlier boot's marker stays as it is, its time included: a fresh one would report a stale
	// guard to clear. Its routes went with that boot, and the loopback interface keeps Windows' own.
	listed := []byte("0.0.0.0/1\n::/1\n")
	if err := os.WriteFile(marker.path, listed, 0o644); err != nil {
		t.Fatal(err)
	}
	// Whole seconds: every file system keeps them as they are.
	old := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(marker.path, old, old); err != nil {
		t.Fatal(err)
	}
	table.routes[netip.MustParsePrefix("::1/128")] = 256
	if _, err := placeFallbackRoutes(table, marker, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(marker.path)
	if err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("an earlier boot's marker was rewritten: %v", err)
	}
	if data, err := os.ReadFile(marker.path); err != nil || string(data) != string(listed) {
		t.Fatalf("an earlier boot's marker now reads %q: %v", data, err)
	}
	if len(table.routes) != 1 || len(table.unlisted) != 0 {
		t.Fatalf("without the kill switch the loopback interface has %v", table.routes)
	}
}

// Regression: a restart into a tunnel with other excluded routes, or with the kill switch off,
// kept the earlier tunnel's fallback routes, which then dropped the newly excluded destinations.
func TestFallbackRoutesOfAnEarlierTunnelGoOnceTheNextOnesAreIn(t *testing.T) {
	marker := testGuardMarker(t)
	table := newFakeLoopbackTable(marker, nil)
	before, after := prefixes("0.0.0.0/1", "128.0.0.0/1"), prefixes("128.0.0.0/1", "0.0.0.0/2", "64.0.0.0/3")
	if _, err := placeFallbackRoutes(table, marker, before); err != nil {
		t.Fatal(err)
	}
	table.calls = nil
	if _, err := placeFallbackRoutes(table, marker, after); err != nil {
		t.Fatal(err)
	}
	want := []string{"add 128.0.0.0/1", "add 0.0.0.0/2", "add 64.0.0.0/3", "remove 0.0.0.0/1"}
	if !slices.Equal(table.calls, want) || len(table.unlisted) != 0 {
		t.Fatalf("route calls %v (unlisted %v), want %v", table.calls, table.unlisted, want)
	}
	if got := marker.routes(); !slices.Equal(got, after) || len(table.routes) != len(after) {
		t.Fatalf("marker names %v and the table holds %v, want %v", got, table.routes, after)
	}
	// The kill switch turned off while the guard holds: none are left, and the marker stays for
	// the guard, naming them until protectDNS rewrites it (removing them again finds nothing).
	if _, err := placeFallbackRoutes(table, marker, nil); err != nil {
		t.Fatal(err)
	}
	if len(table.routes) != 0 {
		t.Fatalf("with the kill switch off the table holds %v", table.routes)
	}
	if got := marker.routes(); !slices.Equal(got, after) {
		t.Fatalf("with the kill switch off the marker names %v, want %v", got, after)
	}
}

func TestFallbackRouteFailuresKeepTheirList(t *testing.T) {
	failed := errors.New("route refused")
	marker := testGuardMarker(t)
	table := newFakeLoopbackTable(marker, nil)
	routes := prefixes("0.0.0.0/1", "128.0.0.0/1", "::/1")
	table.fail[routes[1]] = failed
	if _, err := placeFallbackRoutes(table, marker, routes); !errors.Is(err, failed) {
		t.Fatalf("a refused route did not fail the connect: %v", err)
	}
	if got := marker.routes(); !slices.Equal(got, routes) {
		t.Fatalf("after a failed add the marker names %v, want %v", got, routes)
	}
	// A removal that fails keeps the marker for the next try and for Repair, after removing the others.
	if err := clearFallbackRoutes(table, marker); !errors.Is(err, failed) {
		t.Fatalf("a failed removal was not reported: %v", err)
	}
	if got := marker.routes(); !slices.Equal(got, routes) {
		t.Fatalf("after a failed removal the marker names %v, want %v", got, routes)
	}
	if _, found := table.routes[routes[0]]; found {
		t.Fatal("a failed removal kept the routes after it")
	}
	delete(table.fail, routes[1])
	if err := clearFallbackRoutes(table, marker); err != nil || len(table.routes) != 0 {
		t.Fatalf("the retry left %v: %v", table.routes, err)
	}
	// Without a marker a crash would leave routes nobody removes: nothing is added then.
	unwritable := guardMarkerFile{path: filepath.Join(t.TempDir(), "missing", ".dns-guard")}
	table = newFakeLoopbackTable(unwritable, nil)
	if _, err := placeFallbackRoutes(table, unwritable, routes); err == nil || len(table.calls) != 0 {
		t.Fatalf("routes were added without a marker: %v, %v", err, table.calls)
	}
}

func fallbackTableWith(marker guardMarkerFile, routes []netip.Prefix) *fakeLoopbackTable {
	table := newFakeLoopbackTable(marker, nil)
	for _, prefix := range routes {
		table.routes[prefix] = fallbackRouteMetric
	}
	return table
}

// Regression (Block traffic without VPN after a helper crash): the app keeps the crashed helper's
// guard until a tunnel replaces it, but a start that failed before its own guard was in place
// released the whole machine-wide guard and every route the marker named, so the PC was open. It
// takes back only the routes it added.
func TestAFailedStartTakesBackOnlyTheFallbackRoutesItAdded(t *testing.T) {
	earlier := prefixes("0.0.0.0/1", "::/1")
	routes := prefixes("0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1")
	marker := testGuardMarker(t)
	if err := marker.write(earlier); err != nil {
		t.Fatal(err)
	}
	table := fallbackTableWith(marker, earlier)
	placement, err := placeFallbackRoutes(table, marker, routes)
	if err != nil {
		t.Fatal(err)
	}
	if want := prefixes("128.0.0.0/1", "8000::/1"); !slices.Equal(placement.added, want) || !placement.marked {
		t.Fatalf("placement added %v (marked %v), want %v after an earlier marker", placement.added, placement.marked, want)
	}
	if err := undoFallbackRoutes(table, marker, placement); err != nil {
		t.Fatal(err)
	}
	if len(table.routes) != len(earlier) || table.routes[earlier[0]] != fallbackRouteMetric || table.routes[earlier[1]] != fallbackRouteMetric {
		t.Fatalf("after the failed start the loopback interface has %v, want the crashed helper's %v", table.routes, earlier)
	}
	if got := marker.routes(); !slices.Equal(got, earlier) {
		t.Fatalf("after the failed start the marker names %v, want the crashed helper's %v", got, earlier)
	}

	// A crashed helper's guard without routes (its tunnel ran without the kill switch) keeps its
	// empty marker: that marker is how the app finds the guard.
	marker = testGuardMarker(t)
	if err := os.WriteFile(marker.path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	table = newFakeLoopbackTable(marker, nil)
	if placement, err = placeFallbackRoutes(table, marker, routes); err != nil {
		t.Fatal(err)
	}
	if err := undoFallbackRoutes(table, marker, placement); err != nil || len(table.routes) != 0 {
		t.Fatalf("after the failed start the loopback interface has %v: %v", table.routes, err)
	}
	if info, err := os.Stat(marker.path); err != nil || info.Size() != 0 {
		t.Fatalf("the crashed helper's empty marker did not stay: %v", err)
	}

	// Without an earlier guard a failed start leaves nothing, the marker included.
	marker = testGuardMarker(t)
	table = newFakeLoopbackTable(marker, nil)
	if placement, err = placeFallbackRoutes(table, marker, routes); err != nil || placement.marked {
		t.Fatalf("placement without a marker: %v (marked %v)", err, placement.marked)
	}
	if err := undoFallbackRoutes(table, marker, placement); err != nil || len(table.routes) != 0 {
		t.Fatalf("after the failed start the loopback interface has %v: %v", table.routes, err)
	}
	if _, err := os.Stat(marker.path); !os.IsNotExist(err) {
		t.Fatal("a failed start kept its marker:", err)
	}

	// The same when the start failed on one of its routes: those it added before it go.
	failed := errors.New("route refused")
	table.fail[routes[2]] = failed
	if placement, err = placeFallbackRoutes(table, marker, routes); !errors.Is(err, failed) {
		t.Fatalf("a refused route did not fail the start: %v", err)
	}
	delete(table.fail, routes[2])
	if err := undoFallbackRoutes(table, marker, placement); err != nil || len(table.routes) != 0 {
		t.Fatalf("after a refused route the loopback interface has %v: %v", table.routes, err)
	}
	if _, err := os.Stat(marker.path); !os.IsNotExist(err) {
		t.Fatal("a start that failed on a route kept its marker:", err)
	}

	// A route that cannot be taken back stays named, for a later start, a stop and Repair.
	if placement, err = placeFallbackRoutes(table, marker, routes); err != nil {
		t.Fatal(err)
	}
	table.fail[routes[1]] = failed
	if err := undoFallbackRoutes(table, marker, placement); !errors.Is(err, failed) {
		t.Fatalf("a failed removal was not reported: %v", err)
	}
	if got := marker.routes(); !slices.Equal(got, routes[1:2]) || len(table.routes) != 1 {
		t.Fatalf("after a failed removal the marker names %v and the table holds %v, want %v", got, table.routes, routes[1:2])
	}

	// A start whose marker could not be written added nothing, so it takes nothing back.
	unwritable := guardMarkerFile{path: filepath.Join(t.TempDir(), "missing", ".dns-guard")}
	table = newFakeLoopbackTable(unwritable, nil)
	placement, _ = placeFallbackRoutes(table, unwritable, routes)
	if err := undoFallbackRoutes(table, unwritable, placement); err != nil || len(table.calls) != 0 {
		t.Fatalf("a start without a marker took back %v: %v", table.calls, err)
	}
}

func addCalls(table *fakeLoopbackTable) []string {
	var adds []string
	for _, call := range table.calls {
		if strings.HasPrefix(call, "add ") {
			adds = append(adds, call)
		}
	}
	return adds
}

// Each server switch and restart placed every fallback route again, up to 8,192 route calls during
// the connect. The helper process adds only the routes it has not put in place itself.
func TestALaterPlacementAddsOnlyTheRoutesThisHelperHasNotPlaced(t *testing.T) {
	marker := testGuardMarker(t)
	table := newFakeLoopbackTable(marker, nil)
	helper := &placedFallbackRoutes{table: table}
	routes := prefixes("0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1")
	if _, err := placeFallbackRoutes(helper, marker, routes); err != nil || len(addCalls(table)) != len(routes) {
		t.Fatalf("the first placement added %v: %v", addCalls(table), err)
	}
	table.calls = nil
	if _, err := placeFallbackRoutes(helper, marker, routes); err != nil || len(table.calls) != 0 {
		t.Fatalf("the same routes again made route calls %v: %v", table.calls, err)
	}
	// Another tunnel's routes: only the new one is added, and the one it has not goes.
	next := prefixes("0.0.0.0/1", "128.0.0.0/2", "::/1", "8000::/1")
	if _, err := placeFallbackRoutes(helper, marker, next); err != nil {
		t.Fatal(err)
	}
	if want := []string{"add 128.0.0.0/2", "remove 128.0.0.0/1"}; !slices.Equal(table.calls, want) {
		t.Fatalf("route calls %v, want %v", table.calls, want)
	}
	// A route this process removed, here a failed start's, is added again.
	placement, err := placeFallbackRoutes(helper, marker, routes)
	if err != nil {
		t.Fatal(err)
	}
	if err := undoFallbackRoutes(helper, marker, placement); err != nil {
		t.Fatal(err)
	}
	table.calls = nil
	if _, err := placeFallbackRoutes(helper, marker, routes); err != nil {
		t.Fatal(err)
	}
	if want := []string{"add 128.0.0.0/1"}; !slices.Equal(table.calls, want) {
		t.Fatalf("after a failed start route calls %v, want %v", table.calls, want)
	}
	// A route whose add failed is not taken as placed.
	failed := errors.New("route refused")
	if err := clearFallbackRoutes(helper, marker); err != nil || len(table.routes) != 0 {
		t.Fatalf("release left %v: %v", table.routes, err)
	}
	table.fail[routes[2]] = failed
	if _, err := placeFallbackRoutes(helper, marker, routes); !errors.Is(err, failed) {
		t.Fatalf("a refused route did not fail the start: %v", err)
	}
	delete(table.fail, routes[2])
	table.calls = nil
	if _, err := placeFallbackRoutes(helper, marker, routes); err != nil {
		t.Fatal(err)
	}
	if want := []string{"add ::/1", "add 8000::/1"}; !slices.Equal(addCalls(table), want) {
		t.Fatalf("after a refused route the next placement added %v, want %v", addCalls(table), want)
	}
	// What the marker names is not taken as placed: a restart of Windows removed the routes it names,
	// and the next helper process adds them all again.
	table.routes = map[netip.Prefix]uint32{}
	table.calls = nil
	if _, err := placeFallbackRoutes(&placedFallbackRoutes{table: table}, marker, routes); err != nil {
		t.Fatal(err)
	}
	if len(addCalls(table)) != len(routes) || len(table.routes) != len(routes) {
		t.Fatalf("a new helper process added %v and the table holds %v", addCalls(table), table.routes)
	}
	// Once the process gave up the guard's claim, another may have removed them: it adds them all again.
	table.routes = map[netip.Prefix]uint32{}
	helper.forget()
	table.calls = nil
	if _, err := placeFallbackRoutes(helper, marker, routes); err != nil || len(addCalls(table)) != len(routes) || len(table.routes) != len(routes) {
		t.Fatalf("after forget the placement added %v and the table holds %v: %v", addCalls(table), table.routes, err)
	}
}

// Regression: with Block traffic without VPN off, an earlier tunnel's fallback route that could not
// be removed refused every connect with a kill-switch error. The connect goes on, and the routes
// left stay named, for the stop that releases the guard and the next start.
func TestWithoutTheKillSwitchARouteThatCannotGoRefusesNoConnect(t *testing.T) {
	failed := errors.New("route refused")
	earlier := prefixes("0.0.0.0/1", "128.0.0.0/1", "::/1")
	marker := testGuardMarker(t)
	if err := marker.write(earlier); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(marker.path, old, old); err != nil {
		t.Fatal(err)
	}
	table := fallbackTableWith(marker, earlier)
	table.fail[earlier[1]] = failed
	placement, err := placeFallbackRoutes(table, marker, nil)
	if err != nil {
		t.Fatal("a route that could not go refused the connect:", err)
	}
	if !errors.Is(placement.left, failed) {
		t.Fatalf("the failure was not kept for the log: %v", placement.left)
	}
	if !slices.Equal(placement.named, earlier[1:2]) || len(placement.added) != 0 {
		t.Fatalf("placement names %v and added %v, want %v and none", placement.named, placement.added, earlier[1:2])
	}
	if len(table.routes) != 1 || table.routes[earlier[1]] != fallbackRouteMetric {
		t.Fatalf("the routes that could go stayed: %v", table.routes)
	}
	// The marker stays as it is, its time included, until protectDNS names what is left.
	if info, err := os.Stat(marker.path); err != nil || !info.ModTime().Equal(old) || !slices.Equal(marker.routes(), earlier) {
		t.Fatalf("the marker changed before the guard was in place: %v, names %v", err, marker.routes())
	}
	if err := marker.write(placement.named); err != nil {
		t.Fatal(err)
	}
	// The next start tries again.
	delete(table.fail, earlier[1])
	if placement, err = placeFallbackRoutes(table, marker, nil); err != nil || placement.left != nil || len(placement.named) != 0 || len(table.routes) != 0 {
		t.Fatalf("the retry left %v, naming %v: %v, %v", table.routes, placement.named, err, placement.left)
	}

	// With the kill switch on, a route that cannot go still refuses the connect, and stays named.
	if err := marker.write(earlier); err != nil {
		t.Fatal(err)
	}
	table = fallbackTableWith(marker, earlier)
	table.fail[earlier[1]] = failed
	if _, err := placeFallbackRoutes(table, marker, prefixes("0.0.0.0/1", "::/1", "8000::/1")); !errors.Is(err, failed) {
		t.Fatalf("with the kill switch on a route that could not go did not fail the connect: %v", err)
	}
	if !slices.Contains(marker.routes(), earlier[1]) {
		t.Fatalf("the route that could not go is no longer named: %v", marker.routes())
	}
}

// Markers of builds before the fallback routes are empty: Repair clears them without a route call.
func TestAnEmptyGuardMarkerClearsWithoutRoutes(t *testing.T) {
	marker := testGuardMarker(t)
	if err := os.WriteFile(marker.path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	table := newFakeLoopbackTable(marker, nil)
	if err := clearFallbackRoutes(table, marker); err != nil || len(table.calls) != 0 {
		t.Fatalf("an empty marker: %v, route calls %v", err, table.calls)
	}
	if _, err := os.Stat(marker.path); !os.IsNotExist(err) {
		t.Fatal("the marker was kept:", err)
	}
	// Repair without any marker has nothing to do.
	if err := clearFallbackRoutes(table, marker); err != nil || len(table.calls) != 0 {
		t.Fatalf("no marker: %v, route calls %v", err, table.calls)
	}
}
