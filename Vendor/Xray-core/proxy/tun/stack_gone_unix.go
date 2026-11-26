//go:build unix && !linux

package tun

import "syscall"

// tunGoneErrors are the read errors of a tun whose device is no more, see tunGone: a destroyed utun or tun
// answers ENXIO or EBADF (Elarni). These systems have no EBADFD
var tunGoneErrors = []error{syscall.EBADF, syscall.ENXIO, syscall.ENODEV}
