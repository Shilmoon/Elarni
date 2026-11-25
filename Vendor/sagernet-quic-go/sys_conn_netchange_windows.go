//go:build windows

package quic

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Elarni: isNetworkChangeErr reports a send that failed because the interface, the route or the
// local address is gone for a moment, as while a laptop moves between networks.
// https://docs.microsoft.com/en-us/windows/win32/winsock/windows-sockets-error-codes-2
func isNetworkChangeErr(err error) bool {
	return errors.Is(err, windows.WSAENETUNREACH) ||
		errors.Is(err, windows.WSAEHOSTUNREACH) ||
		errors.Is(err, windows.WSAEADDRNOTAVAIL) ||
		errors.Is(err, windows.WSAENETDOWN)
}
