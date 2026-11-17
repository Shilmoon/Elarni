//go:build linux && !android

package shadowquic

import "syscall"

// readBinding reads the routing mark of a socket of the core. Android is not here: its
// sockets carry a mark of the system that an app cannot set, and the app is outside its
// own VPN.
func readBinding(socket syscall.RawConn, _ bool) (bound binding) {
	_ = socket.Control(func(fd uintptr) {
		if mark, err := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK); err == nil {
			bound.mark = uint32(mark)
		}
	})
	return bound
}
