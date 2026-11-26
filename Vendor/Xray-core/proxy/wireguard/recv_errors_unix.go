//go:build unix

package wireguard

import "syscall"

// perPacketErrors are the read errors that answer one datagram, see perPacketError: a connected
// socket reports an ICMP port unreachable for an earlier send as ECONNREFUSED (Elarni).
var perPacketErrors = []error{syscall.ECONNREFUSED}
