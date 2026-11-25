//go:build !unix && !windows

package quic

func isNetworkChangeErr(error) bool {
	// to be implemented for more specific platforms
	return false
}
