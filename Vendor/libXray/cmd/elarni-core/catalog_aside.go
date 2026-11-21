//go:build windows || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// setAsideCatalog's request names the revision of a stored catalog the app
// could not use although it decrypts, or "" when it read none.
func setAsideRevision(payload json.RawMessage) (string, error) {
	var request struct {
		Revision string `json:"revision"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		return "", errors.New("invalid_request")
	}
	return request.Revision, nil
}

// catalogUnusable says whether a stored catalog is set aside: one that cannot
// be decrypted or parsed, or the revision the app rejected. A busy store or a
// locked keyring keeps its error, since a later read may succeed.
func catalogUnusable(read error, stored catalogEnvelope, rejected string) (bool, error) {
	switch {
	case read == nil:
		return rejected != "" && rejected == stored.Revision, nil
	case read.Error() == "storage_corrupt":
		return true, nil
	default:
		return false, read
	}
}

// unreadableCatalogPath is a free name beside the catalog for a file set aside
// now. The caller holds catalog.lock, which every writer of these names takes,
// so no set-aside file is ever replaced.
func unreadableCatalogPath(root, extension string) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for attempt := 1; attempt <= 100; attempt++ {
		name := "catalog.unreadable-" + stamp + extension
		if attempt > 1 {
			name = fmt.Sprintf("catalog.unreadable-%s-%d%s", stamp, attempt, extension)
		}
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		} else if err != nil {
			break
		}
	}
	return "", errors.New("storage_unavailable")
}
