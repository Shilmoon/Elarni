package main

import (
	"time"

	"github.com/xtls/xray-core/common/log"
)

// firewallKeeper puts the tunnel's firewall back when another program removed it, as a
// firewall service does that reloads its rules with nft flush ruleset. Without it, DNS sent
// to a resolver outside the tunnel leaves unredirected and the guard stops nothing, while
// the tunnel still shows connected. The Linux worker calls keep every second.
type firewallKeeper struct {
	present  func() bool
	restore  func() error
	restored time.Time
}

// keep puts the firewall back when it is missing, at most once a second, and logs each time.
// An error is a firewall that is missing and could not be put back; its line says why.
func (keeper *firewallKeeper) keep(now time.Time) error {
	if !keeper.restored.IsZero() && now.Sub(keeper.restored) < time.Second || keeper.present() {
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
