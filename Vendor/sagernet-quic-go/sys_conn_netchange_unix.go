//go:build unix

package quic

import (
	"errors"
	"syscall"
)

// Elarni: isNetworkChangeErr reports a send that failed because the interface, the route or the
// local address is gone for a moment, as while a phone or laptop moves between Wi-Fi and cellular.
func isNetworkChangeErr(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EADDRNOTAVAIL) ||
		errors.Is(err, syscall.ENETDOWN)
}
