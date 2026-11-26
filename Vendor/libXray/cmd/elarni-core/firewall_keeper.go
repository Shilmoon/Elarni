package main

import (
	"time"

	"github.com/xtls/xray-core/common/log"
)

// firewallState is what a check of the tunnel's firewall found.
type firewallState int

const (
	// firewallUnknown: the check failed for another reason than a missing table (a netlink
	// error while another program changes the ruleset, a timeout). The firewall may be there.
	firewallUnknown firewallState = iota
	firewallMissing
	// firewallIncomplete: the table is there without one of its chains or a chain's marker
	// rule, as after nft flush table or a deleted chain.
	firewallIncomplete
	firewallWhole
)

// firewallKeeper puts the tunnel's firewall back when another program removed it, as a
// firewall service does that reloads its rules with nft flush ruleset. Without it, DNS sent
// to a resolver outside the tunnel leaves unredirected and the guard stops nothing, while
// the tunnel still shows connected. The Linux worker calls keep every second.
type firewallKeeper struct {
	check    func() firewallState
	restore  func() error
	restored time.Time
	// paused is set when the supervisor is about to remove the firewall itself.
	paused bool
}

// keep puts the firewall back when it is missing or incomplete, at most once a second, and logs
// each time. A check that tells nothing waits for the next one, with nothing put back. An error is
// a firewall that is missing and could not be put back; its line says why.
func (keeper *firewallKeeper) keep(now time.Time) error {
	if keeper.paused || !keeper.restored.IsZero() && now.Sub(keeper.restored) < time.Second {
		return nil
	}
	if state := keeper.check(); state != firewallMissing && state != firewallIncomplete {
		return nil
	}
	if err := keeper.restore(); err != nil {
		log.Record(&log.GeneralMessage{Severity: log.Severity_Error,
			Content: "Another program removed the VPN firewall rules (DNS redirect and kill switch guard), and they could not be put back, so the VPN stops: " + err.Error()})
		return err
	}
	keeper.restored = now
	log.Record(&log.GeneralMessage{Severity: log.Severity_Warning,
		Content: "Another program removed the VPN firewall rules (DNS redirect and kill switch guard); they were put back"})
	return nil
}
