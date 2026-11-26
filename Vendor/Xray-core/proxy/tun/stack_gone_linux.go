package tun

import "syscall"

// tunGoneErrors are the read errors of a tun whose device is no more, see tunGone. A tun whose interface was
// deleted answers EBADFD, as wireguard-go knows, and an fd that Android's VpnService revoked EBADF (Elarni)
var tunGoneErrors = []error{syscall.EBADF, syscall.EBADFD, syscall.ENXIO, syscall.ENODEV}
