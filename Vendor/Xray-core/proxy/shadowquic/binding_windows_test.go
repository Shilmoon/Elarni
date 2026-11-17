package shadowquic

import (
	"encoding/binary"
	"math/bits"
	"net"
	"testing"

	"golang.org/x/sys/windows"
)

// A socket bound the way the core binds its own (transport/internet/sockopt_windows.go:
// the IPv4 option takes the index in network byte order, the IPv6 option in host byte
// order) reports that interface, for both families.
func TestReadBindingReportsTheInterfaceOfABoundSocket(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) == 0 {
		t.Fatalf("no interfaces: %v", err)
	}
	index := uint32(interfaces[0].Index)
	for _, family := range []struct {
		network string
		ipv6    bool
	}{{"udp4", false}, {"udp6", true}} {
		conn, err := net.ListenUDP(family.network, nil)
		if err != nil {
			t.Fatalf("%s: %v", family.network, err)
		}
		defer conn.Close()
		raw, err := conn.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		if bound := readBinding(raw, family.ipv6); bound != (binding{}) {
			t.Fatalf("%s: an unbound socket reports %+v", family.network, bound)
		}
		var failure error
		if err := raw.Control(func(fd uintptr) {
			if family.ipv6 {
				failure = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, unicastInterface, int(index))
				return
			}
			var bytes [4]byte
			binary.BigEndian.PutUint32(bytes[:], index)
			failure = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, unicastInterface, int(binary.NativeEndian.Uint32(bytes[:])))
		}); err != nil || failure != nil {
			t.Fatalf("%s: binding to interface %d: %v %v", family.network, index, err, failure)
		}
		if bound := readBinding(raw, family.ipv6); bound.index != index {
			t.Fatalf("%s: bound to interface %d, read %d", family.network, index, bound.index)
		}
	}
}

// The index is taken in whichever byte order names an interface of this machine.
func TestInterfaceIndexAcceptsEitherByteOrder(t *testing.T) {
	only := func(index uint32) func(uint32) bool { return func(asked uint32) bool { return asked == index } }
	for _, index := range []uint32{1, 3, 27, 300} {
		if got := interfaceIndex(index, only(index)); got != index {
			t.Fatalf("host order %d read as %d", index, got)
		}
		if got := interfaceIndex(bits.ReverseBytes32(index), only(index)); got != index {
			t.Fatalf("network order %d read as %d", index, got)
		}
	}
	if got := interfaceIndex(0, only(0)); got != 0 {
		t.Fatalf("no binding read as %d", got)
	}
	// An index no interface has is passed on as reported, so the client names it in its error.
	if got := interfaceIndex(9, only(3)); got != 9 {
		t.Fatalf("unknown index 9 read as %d", got)
	}
}
