//go:build unix

package quic

import "syscall"

var (
	noBufferSpaceErrno  error = syscall.ENOBUFS
	networkChangeErrnos       = []error{syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.EADDRNOTAVAIL, syscall.ENETDOWN}
)
