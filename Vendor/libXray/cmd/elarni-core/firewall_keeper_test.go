package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xtls/libxray/xray"
)

// N6-16: nft flush ruleset while connected removed the DNS redirect and the guard for good.
func TestFirewallKeeperPutsTheFirewallBack(t *testing.T) {
	present, restores := true, 0
	var failure error
	keeper := firewallKeeper{
		present: func() bool { return present },
		restore: func() error {
			restores++
			if failure == nil {
				present = true
			}
			return failure
		},
	}
	start := time.Unix(1_800_000_000, 0)
	lines := func(after int64, text string) []string {
		var found []string
		for _, entry := range xray.ReadLogs(after).Entries {
			if strings.Contains(entry.Text, text) {
				found = append(found, entry.Text)
			}
		}
		return found
	}
	logged := func(after int64) int { return len(lines(after, "they were put back")) }
	cursor := xray.LastLogSeq()
	for second := range 3 {
		if err := keeper.keep(start.Add(time.Duration(second) * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if restores != 0 || logged(cursor) != 0 {
		t.Fatalf("a firewall that is there was restored %d times, logged %d", restores, logged(cursor))
	}

	present = false
	if err := keeper.keep(start.Add(3 * time.Second)); err != nil || restores != 1 || logged(cursor) != 1 {
		t.Fatalf("missing firewall: error %v, %d restores, %d log lines; want nil, 1, 1", err, restores, logged(cursor))
	}
	// Removed again at once, as by a second flush: within the second nothing runs.
	present = false
	if err := keeper.keep(start.Add(3*time.Second + 400*time.Millisecond)); err != nil || restores != 1 {
		t.Fatalf("a second restore within a second: error %v, %d restores", err, restores)
	}
	if err := keeper.keep(start.Add(4 * time.Second)); err != nil || restores != 2 || logged(cursor) != 2 {
		t.Fatalf("the next second: error %v, %d restores, %d log lines; want nil, 2, 2", err, restores, logged(cursor))
	}

	present, failure = false, fmt.Errorf("%w: Error: Could not process rule: Operation not permitted", errors.New("vpn_firewall_failed"))
	if err := keeper.keep(start.Add(5 * time.Second)); !errors.Is(err, failure) {
		t.Fatalf("a failed restore returned %v, want %v so the worker ends the tunnel", err, failure)
	}
	if logged(cursor) != 2 {
		t.Fatal("a failed restore was logged as restored")
	}
	// One line says why the tunnel ends, with what nft said.
	if failed := lines(cursor, "could not be put back"); len(failed) != 1 || !strings.Contains(failed[0], "Operation not permitted") {
		t.Fatalf("a failed restore logged %q, want one line with the nft error", failed)
	}
}
