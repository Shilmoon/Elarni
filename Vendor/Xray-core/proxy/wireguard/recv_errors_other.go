//go:build !unix && !windows

package wireguard

// perPacketErrors are the read errors that answer one datagram, see perPacketError; this system
// reports none (Elarni).
var perPacketErrors []error
