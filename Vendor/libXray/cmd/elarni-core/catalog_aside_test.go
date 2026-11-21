//go:build windows || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Reset sets an unreadable catalog aside beside it and never deletes it: the
// next load starts empty and a save works. A readable catalog stays unless the
// app rejected that very revision, and the folder's other files stay as they are.
func testCatalogSetAside(t *testing.T, root, name string) {
	t.Helper()
	others := map[string][]byte{
		"session.lock":                            []byte("lease"),
		"system-proxy.json":                       []byte(`{"user":"pac"}`),
		filepath.Join("assets", "geoip.dat"):      []byte("geo"),
		filepath.Join("update", "pending.json"):   []byte("{}"),
		filepath.Join("control", "endpoint.json"): []byte("{}"),
	}
	for path, data := range others {
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	call := func(method string, payload any) (any, error) {
		encoded, _ := json.Marshal(payload)
		return catalogOperation(root, method, encoded)
	}
	setAside := func(revision string) (map[string]any, string) {
		t.Helper()
		result, err := call("setAsideCatalog", map[string]any{"revision": revision})
		if err != nil {
			t.Fatal("set aside", err)
		}
		name, _ := result.(map[string]any)["name"].(string)
		return result.(map[string]any), name
	}
	save := func(revision, catalog string) string {
		t.Helper()
		result, err := call("saveCatalog", map[string]any{"revision": revision, "catalog": json.RawMessage(catalog)})
		if err != nil {
			t.Fatal("save", err)
		}
		return result.(map[string]any)["revision"].(string)
	}
	loadRevision := func() string {
		t.Helper()
		value, err := catalogOperation(root, "loadCatalog", nil)
		if err != nil {
			t.Fatal("load", err)
		}
		return value.(catalogEnvelope).Revision
	}

	if result, _ := setAside(""); result["setAside"] != false || len(result) != 1 {
		t.Fatal("a store without a catalog was set aside", result)
	}
	if _, err := call("setAsideCatalog", map[string]any{"revision": 1}); err == nil || err.Error() != "invalid_request" {
		t.Fatal("a malformed request was accepted", err)
	}
	save("", `{"generation":1}`)
	path := filepath.Join(root, name)
	stored := read(path)
	for _, rejected := range []string{"", strings.Repeat("0", 64)} {
		if result, _ := setAside(rejected); result["setAside"] != false || len(result) != 1 {
			t.Fatal("a readable catalog was set aside", rejected, result)
		}
	}
	if !bytes.Equal(read(path), stored) {
		t.Fatal("a readable catalog changed")
	}

	stored[len(stored)/2] ^= 0x80
	if err := os.WriteFile(path, stored, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogOperation(root, "loadCatalog", nil); err == nil || err.Error() != "storage_corrupt" {
		t.Fatal("damaged data was not reported as corrupt", err)
	}
	pattern := regexp.MustCompile(`^catalog\.unreadable-\d{8}T\d{6}Z(-\d+)?` + regexp.QuoteMeta(filepath.Ext(name)) + `$`)
	result, first := setAside("")
	if result["setAside"] != true || !pattern.MatchString(first) {
		t.Fatal("damaged data was not set aside", result)
	}
	if !bytes.Equal(read(filepath.Join(root, first)), stored) {
		t.Fatal("the set-aside file is not the unreadable catalog")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the unreadable catalog stayed in place", err)
	}
	if loadRevision() != "" {
		t.Fatal("the next load did not start empty")
	}

	// The app rejected a document that decrypts: that revision is set aside too.
	rejected := save("", `{"generation":2}`)
	kept := read(path)
	result, second := setAside(rejected)
	if result["setAside"] != true || !pattern.MatchString(second) || second == first {
		t.Fatal("the rejected revision was not set aside under a new name", result, first)
	}
	if !bytes.Equal(read(filepath.Join(root, second)), kept) || !bytes.Equal(read(filepath.Join(root, first)), stored) {
		t.Fatal("a set-aside file was replaced")
	}
	if loadRevision() != "" {
		t.Fatal("the next load did not start empty")
	}
	save("", `{"generation":3}`)
	for path, data := range others {
		if !bytes.Equal(read(filepath.Join(root, path)), data) {
			t.Fatal("set aside touched another file", path)
		}
	}
}
