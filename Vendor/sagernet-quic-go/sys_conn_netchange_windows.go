//go:build windows

package quic

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Elarni: isNetworkChangeErr reports a send that failed because the interface, the route or the
// local address is gone for a moment, as while a laptop moves between networks.
// https://docs.microsoft.com/en-us/windows/win32/winsock/windows-sockets-error-codes-2
// An overlapped send can report the Win32 completion codes ERROR_NETWORK_UNREACHABLE and
// ERROR_HOST_UNREACHABLE instead of WSAENETUNREACH and WSAEHOSTUNREACH.
func isNetworkChangeErr(err error) bool {
	return errors.Is(err, windows.WSAENETUNREACH) ||
		errors.Is(err, windows.WSAEHOSTUNREACH) ||
		errors.Is(err, windows.WSAEADDRNOTAVAIL) ||
		errors.Is(err, windows.WSAENETDOWN) ||
		errors.Is(err, windows.ERROR_NETWORK_UNREACHABLE) ||
		errors.Is(err, windows.ERROR_HOST_UNREACHABLE)
}
