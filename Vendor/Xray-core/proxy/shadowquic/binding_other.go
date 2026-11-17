//go:build !windows && (!linux || android)

package shadowquic

import "syscall"

// On Android the app is outside its own VPN, and Apple's tunnels exclude the server's
// addresses by route: a socket needs nothing there.
func readBinding(syscall.RawConn, bool) binding { return binding{} }
