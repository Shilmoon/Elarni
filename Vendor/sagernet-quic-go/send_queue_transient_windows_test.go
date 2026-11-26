//go:build windows

package quic

import "golang.org/x/sys/windows"

var (
	noBufferSpaceErrno  error = windows.WSAENOBUFS
	networkChangeErrnos       = []error{windows.WSAENETUNREACH, windows.WSAEHOSTUNREACH, windows.WSAEADDRNOTAVAIL, windows.WSAENETDOWN}
)
