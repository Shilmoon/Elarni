package main

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/xtls/libxray/xray"
)

// A change of per-app routing while connected reaches the running tunnel as "reloadVPN": the
// tunnel's owner hands the new configuration to the core, which takes it when only routing
// rules and their outbounds changed. Anything else is answered with vpn_reload_restart, and
// the app restarts the tunnel as it did before.

// sameSession reports whether two tunnel requests differ in nothing the running tunnel was set
// up with. strip clears what a reload may change: the configuration, and what only frames it.
func sameSession[T any](running, next T, strip func(*T)) bool {
	strip(&running)
	strip(&next)
	before, errBefore := json.Marshal(running)
	after, errAfter := json.Marshal(next)
	return errBefore == nil && errAfter == nil && bytes.Equal(before, after)
}

// reloadCode is the reply's code for a reload that did not happen. The tunnel runs as it did.
func reloadCode(err error) string {
	if errors.Is(err, xray.ErrRestartNeeded) {
		return "vpn_reload_restart"
	}
	return "vpn_reload_failed"
}
