package wireguard

import "syscall"

// perPacketErrors are the read errors that answer one datagram, see perPacketError: Windows reports
// an ICMP port unreachable for an earlier send as WSAECONNRESET, on a socket that has not turned
// SIO_UDP_CONNRESET off as Go's own UDP sockets do (Elarni).
var perPacketErrors = []error{syscall.WSAECONNRESET}
