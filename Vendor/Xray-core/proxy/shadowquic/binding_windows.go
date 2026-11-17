package shadowquic

import (
	"encoding/binary"
	"syscall"

	"golang.org/x/sys/windows"
)

// IP_UNICAST_IF and IPV6_UNICAST_IF.
const unicastInterface = 31

// readBinding reads the interface a socket of the core is bound to. The IPv4 option
// holds the index in network byte order, the IPv6 option in host byte order.
func readBinding(socket syscall.RawConn, ipv6 bool) (bound binding) {
	_ = socket.Control(func(fd uintptr) {
		if ipv6 {
			if index, err := windows.GetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, unicastInterface); err == nil {
				bound.index = uint32(index)
			}
			return
		}
		if index, err := windows.GetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, unicastInterface); err == nil {
			var bytes [4]byte
			binary.NativeEndian.PutUint32(bytes[:], uint32(index))
			bound.index = binary.BigEndian.Uint32(bytes[:])
		}
	})
	return bound
}
