//go:build !cgo || !(darwin || android)

package shadowquic

func startLinked([]byte) (nativeClient, error) { return nil, errNotLinked }
