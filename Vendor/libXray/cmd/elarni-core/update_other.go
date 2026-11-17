//go:build !windows && !linux

package main

import (
	"errors"

	"github.com/xtls/libxray/update"
)

// No macOS package is published, so nothing there replaces itself.
func updateInstall() (update.Kind, bool) { return "", false }

func installUpdate(update.Kind, string, bool) (any, error) {
	return nil, errors.New("update_unsupported")
}
