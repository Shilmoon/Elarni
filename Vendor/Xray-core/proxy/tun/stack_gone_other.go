//go:build !unix

package tun

// tunGoneErrors are the read errors of a tun whose device is no more, see tunGone. Wintun's end of a session,
// ERROR_HANDLE_EOF, already reads as closed (Elarni)
var tunGoneErrors []error
