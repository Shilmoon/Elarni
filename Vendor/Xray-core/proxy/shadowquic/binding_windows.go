package shadowquic

import (
	"math/bits"
	"net"
	"syscall"

	"golang.org/x/sys/windows"
)

// IP_UNICAST_IF and IPV6_UNICAST_IF.
const unicastInterface = 31

// readBinding reads the interface a socket of the core is bound to.
func readBinding(socket syscall.RawConn, ipv6 bool) (bound binding) {
	level := windows.IPPROTO_IP
	if ipv6 {
		level = windows.IPPROTO_IPV6
	}
	_ = socket.Control(func(fd uintptr) {
		if index, err := windows.GetsockoptInt(windows.Handle(fd), level, unicastInterface); err == nil {
			bound.index = interfaceIndex(uint32(index), func(index uint32) bool {
				_, err := net.InterfaceByIndex(int(index))
				return err == nil
			})
		}
	})
	return bound
}

// interfaceIndex turns what the option reports into an interface index. The IPv4 option
// is set in network byte order, yet Windows Server 2022 reports it, like the IPv6 one, in
// host byte order. The order that names an interface of this machine is the index, so
// either answer binds the client.
func interfaceIndex(reported uint32, exists func(uint32) bool) uint32 {
	if reported == 0 || exists(reported) {
		return reported
	}
	if swapped := bits.ReverseBytes32(reported); exists(swapped) {
		return swapped
	}
	return reported
}
