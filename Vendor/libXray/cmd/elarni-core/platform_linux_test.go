//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeLinuxKeyring stands in for secret-tool and gdbus. The key item is the
// file key; SearchItems answers as ELARNI_TEST_GDBUS says (none, unlocked,
// locked, garbage, or a failed call), and both tools log their arguments.
type fakeLinuxKeyring struct{ directory, key, calls, query string }

func newFakeLinuxKeyring(t *testing.T) fakeLinuxKeyring {
	t.Helper()
	directory := t.TempDir()
	keyring := fakeLinuxKeyring{
		directory: directory,
		key:       filepath.Join(directory, "key"),
		calls:     filepath.Join(directory, "calls"),
		query:     filepath.Join(directory, "query"),
	}
	secretTool := `#!/bin/sh
printf '%s\n' "$*" >> "$ELARNI_TEST_CALLS"
case "$1" in
lookup) [ -f "$ELARNI_TEST_KEY" ] || exit 1; cat "$ELARNI_TEST_KEY" ;;
store) cat > "$ELARNI_TEST_KEY" ;;
clear) rm -f "$ELARNI_TEST_KEY" ;;
*) exit 2 ;;
esac
`
	gdbus := `#!/bin/sh
printf '%s\n' "$@" > "$ELARNI_TEST_QUERY"
case "$ELARNI_TEST_GDBUS" in
none) echo '(@ao [], @ao [])' ;;
unlocked) echo "([objectpath '/org/freedesktop/secrets/collection/login/1'], @ao [])" ;;
locked) echo "(@ao [], [objectpath '/org/freedesktop/secrets/collection/login/1'])" ;;
garbage) echo 'unexpected' ;;
*) echo 'Error: GDBus.Error:org.freedesktop.DBus.Error.ServiceUnknown' >&2; exit 1 ;;
esac
`
	for name, script := range map[string]string{"secret-tool": secretTool, "gdbus": gdbus} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ELARNI_TEST_KEY", keyring.key)
	t.Setenv("ELARNI_TEST_CALLS", keyring.calls)
	t.Setenv("ELARNI_TEST_QUERY", keyring.query)
	t.Setenv("ELARNI_TEST_GDBUS", "none")
	previousSecretTool, previousGdbus := linuxSecretTool, linuxGdbus
	linuxSecretTool = func() string { return filepath.Join(directory, "secret-tool") }
	linuxGdbus = func() string { return filepath.Join(directory, "gdbus") }
	t.Cleanup(func() { linuxSecretTool, linuxGdbus = previousSecretTool, previousGdbus })
	return keyring
}

func (keyring fakeLinuxKeyring) cleared() bool {
	calls, _ := os.ReadFile(keyring.calls)
	return strings.Contains(string(calls), "clear ")
}

func linuxCatalogCode(root, method string, payload any) string {
	encoded, _ := json.Marshal(payload)
	if _, err := catalogOperation(root, method, encoded); err != nil {
		return err.Error()
	}
	return ""
}

// Providers see the distribution release, not just "linux".
func TestLinuxDeviceVersionReadsTheDistributionRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte("NAME=\"Ubuntu\"\nVERSION_ID=\"22.04\"\nID=ubuntu\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if version := osReleaseVersion(path); version != "22.04" {
		t.Fatalf("version = %q", version)
	}
	if version := osReleaseVersion(filepath.Join(t.TempDir(), "missing")); version != "" {
		t.Fatalf("missing file version = %q", version)
	}
	if version := deviceVersion(); version == "" || version == "linux" {
		t.Fatalf("device version = %q", version)
	}
}

// A transient keyring failure while saving must never replace the key that
// encrypts the existing catalog: a failed rename would then lose the catalog.
func TestLinuxCatalogSaveNeverRegeneratesTheKeyOfAnExistingCatalog(t *testing.T) {
	directory := t.TempDir()
	key := filepath.Join(directory, "key")
	tool := filepath.Join(directory, "secret-tool")
	// When armed, the first lookup succeeds and every later one fails like a
	// keyring that locked or lost its D-Bus connection mid-operation.
	script := `#!/bin/sh
case "$1" in
lookup)
  if [ -f "$ELARNI_TEST_FAIL" ]; then
    [ -f "$ELARNI_TEST_FAIL.used" ] && exit 1
    : > "$ELARNI_TEST_FAIL.used"
  fi
  [ -f "$ELARNI_TEST_KEY" ] || exit 1
  cat "$ELARNI_TEST_KEY" ;;
store) cat > "$ELARNI_TEST_KEY" ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	failure := filepath.Join(directory, "fail")
	t.Setenv("ELARNI_TEST_KEY", key)
	t.Setenv("ELARNI_TEST_FAIL", failure)
	previous := linuxSecretTool
	linuxSecretTool = func() string { return tool }
	defer func() { linuxSecretTool = previous }()

	root := filepath.Join(directory, "Elarni")
	save := func(revision, catalog string) (any, error) {
		payload, _ := json.Marshal(map[string]any{"revision": revision, "catalog": json.RawMessage(catalog)})
		return catalogOperation(root, "saveCatalog", payload)
	}
	first, err := save("", `{"generation":1}`)
	if err != nil {
		t.Fatal("first save", err)
	}
	revision := first.(map[string]any)["revision"].(string)
	original, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failure, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := save(revision, `{"generation":2}`); err != nil {
		t.Fatal("save could not reuse the key that decrypted the catalog", err)
	}
	if current, _ := os.ReadFile(key); !bytes.Equal(current, original) {
		t.Fatal("the existing catalog key was replaced")
	}
	for _, path := range []string{failure, failure + ".used"} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := catalogOperation(root, "loadCatalog", nil)
	if err != nil || string(loaded.(catalogEnvelope).Catalog) != `{"generation":2}` {
		t.Fatal("catalog unreadable after a keyring failure", loaded, err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("catalog directory is not private", info, err)
	}
}

// An existing, too-open data directory is made private before use.
func TestLinuxCatalogLockTightensExistingDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Elarni")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := linuxCatalogLock(root, "catalog.lock")
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("existing catalog directory kept group/other access", info, err)
	}
}

// A catalog whose key item is gone for good can never be decrypted: then the
// Secret Service holds no item with the key's attributes, locked or not. Any
// doubt keeps storage_unavailable, which an unlock may still resolve.
func TestLinuxCatalogWithoutItsKeyItemIsCorrupt(t *testing.T) {
	keyring := newFakeLinuxKeyring(t)
	// The query names the folder in GVariant text, quotes and backslashes escaped.
	root := filepath.Join(keyring.directory, `it's \ Elarni`)
	if code := linuxCatalogCode(root, "saveCatalog", map[string]any{"revision": "", "catalog": map[string]any{}}); code != "" {
		t.Fatal("first save", code)
	}
	if err := os.Remove(keyring.key); err != nil {
		t.Fatal(err)
	}
	for mode, want := range map[string]string{
		"none":     "storage_corrupt",
		"locked":   "storage_unavailable",
		"unlocked": "storage_unavailable",
		"garbage":  "storage_unavailable",
		"fail":     "storage_unavailable",
	} {
		t.Setenv("ELARNI_TEST_GDBUS", mode)
		if code := linuxCatalogCode(root, "loadCatalog", nil); code != want {
			t.Errorf("SearchItems %s: load %q, want %q", mode, code, want)
		}
	}
	query, _ := os.ReadFile(keyring.query)
	escaped := strings.ReplaceAll(root, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	if want := strings.Join([]string{"call", "--session", "--dest", "org.freedesktop.secrets", "--object-path",
		"/org/freedesktop/secrets", "--method", "org.freedesktop.Secret.Service.SearchItems",
		"{'application': 'com.elarni.Elarni', 'catalog': '" + escaped + "'}"}, "\n") + "\n"; string(query) != want {
		t.Fatalf("SearchItems query\n%s\nwant\n%s", query, want)
	}
	t.Setenv("ELARNI_TEST_GDBUS", "none")
	// Without gdbus nothing tells a missing item from a locked one.
	linuxGdbus = func() string { return "" }
	if code := linuxCatalogCode(root, "loadCatalog", nil); code != "storage_unavailable" {
		t.Error("without gdbus:", code)
	}
	// Without secret-tool no reset could save a catalog either.
	linuxGdbus = func() string { return filepath.Join(keyring.directory, "gdbus") }
	linuxSecretTool = func() string { return "" }
	if code := linuxCatalogCode(root, "loadCatalog", nil); code != "storage_unavailable" {
		t.Error("without secret-tool:", code)
	}
}

func TestLinuxCatalogSetAsideKeepsUnreadableData(t *testing.T) {
	keyring := newFakeLinuxKeyring(t)
	root := filepath.Join(keyring.directory, "Elarni")
	testCatalogSetAside(t, root, "catalog.enc")
	// Only the key item that failed to decrypt the catalog stayed; it was never cleared.
	if keyring.cleared() {
		t.Fatal("a well-formed key item was cleared")
	}
}

// After a keyring reset the catalog is set aside and the next save makes a new
// key; the old file keeps its bytes for a key that may come back.
func TestLinuxSetAsideAfterTheKeyItemIsGone(t *testing.T) {
	keyring := newFakeLinuxKeyring(t)
	root := filepath.Join(keyring.directory, "Elarni")
	if code := linuxCatalogCode(root, "saveCatalog", map[string]any{"revision": "", "catalog": map[string]any{"generation": 1}}); code != "" {
		t.Fatal("first save", code)
	}
	stored, _ := os.ReadFile(filepath.Join(root, "catalog.enc"))
	if err := os.Remove(keyring.key); err != nil {
		t.Fatal(err)
	}
	// A locked keyring is never set aside.
	t.Setenv("ELARNI_TEST_GDBUS", "locked")
	if code := linuxCatalogCode(root, "setAsideCatalog", map[string]any{"revision": ""}); code != "storage_unavailable" {
		t.Fatal("a locked keyring was set aside:", code)
	}
	t.Setenv("ELARNI_TEST_GDBUS", "none")
	result, err := catalogOperation(root, "setAsideCatalog", json.RawMessage(`{"revision":""}`))
	if err != nil || result.(map[string]any)["setAside"] != true {
		t.Fatal("the catalog without a key was not set aside", result, err)
	}
	if aside, _ := os.ReadFile(filepath.Join(root, result.(map[string]any)["name"].(string))); !bytes.Equal(aside, stored) {
		t.Fatal("the set-aside catalog changed")
	}
	if code := linuxCatalogCode(root, "saveCatalog", map[string]any{"revision": "", "catalog": map[string]any{"generation": 2}}); code != "" {
		t.Fatal("save after reset", code)
	}
	loaded, err := catalogOperation(root, "loadCatalog", nil)
	if err != nil || string(loaded.(catalogEnvelope).Catalog) != `{"generation":2}` {
		t.Fatal("the new catalog did not load", loaded, err)
	}
}

// A key item that is not a key blocks every catalog, even an empty one. Reset
// clears it after setting any catalog aside, so the next save makes a new key.
func TestLinuxSetAsideClearsAMalformedKeyItem(t *testing.T) {
	keyring := newFakeLinuxKeyring(t)
	root := filepath.Join(keyring.directory, "Elarni")
	path := filepath.Join(root, "catalog.enc")
	save := func(generation int) string {
		return linuxCatalogCode(root, "saveCatalog", map[string]any{"revision": "", "catalog": map[string]any{"generation": generation}})
	}
	damage := func() {
		t.Helper()
		if err := os.WriteFile(keyring.key, []byte("not a key\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setAside := func() map[string]any {
		t.Helper()
		result, err := catalogOperation(root, "setAsideCatalog", json.RawMessage(`{"revision":""}`))
		if err != nil {
			t.Fatal("set aside", err)
		}
		return result.(map[string]any)
	}
	if code := save(1); code != "" {
		t.Fatal("first save", code)
	}
	damage()
	if code := linuxCatalogCode(root, "loadCatalog", nil); code != "storage_corrupt" {
		t.Fatal("a malformed key item loaded:", code)
	}
	if result := setAside(); result["setAside"] != true || result["name"] == nil {
		t.Fatal("the catalog was not set aside", result)
	}
	if _, err := os.Stat(keyring.key); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the malformed key item stayed", err)
	}
	if calls, _ := os.ReadFile(keyring.calls); !strings.Contains(string(calls), "clear application com.elarni.Elarni catalog "+root+"\n") {
		t.Fatalf("secret-tool calls:\n%s", calls)
	}
	if code := save(2); code != "" {
		t.Fatal("save after reset", code)
	}

	// Without a catalog the damaged item still refuses the first save.
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	damage()
	if code := save(3); code != "storage_corrupt" {
		t.Fatal("a malformed key item took a first save:", code)
	}
	if result := setAside(); result["setAside"] != true || len(result) != 1 {
		t.Fatal("the malformed key item was not cleared", result)
	}
	if code := save(3); code != "" {
		t.Fatal("save after reset", code)
	}
	// A well-formed key item stays, and so does the catalog it reads.
	if result := setAside(); result["setAside"] != false {
		t.Fatal("a well-formed key item was cleared", result)
	}
	if _, err := os.Stat(keyring.key); err != nil {
		t.Fatal("the key item is gone", err)
	}
}

// A probe through the running local listener stays in this process: the root
// probe child bypasses the VPN and accepts only configurations with inbounds.
func TestLinuxProxyProbeSkipsTheRootProbe(t *testing.T) {
	previous := linuxProbeRoot
	linuxProbeRoot = t.TempDir()
	defer func() { linuxProbeRoot = previous }()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := linuxProbePath(os.Getuid())
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var calls atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			conn.Close()
		}
	}()
	ping := func(payload map[string]any) bool {
		encoded, _ := json.Marshal(payload)
		_, _, handled := platformRequest("ping", encoded)
		return handled
	}
	// The same request without a proxy is a physical probe, which the root child serves.
	if !ping(map[string]any{"configJSON": `{"inbounds":[],"outbounds":[{"protocol":"freedom"}]}`}) || calls.Load() != 1 {
		t.Fatal("a physical probe did not reach the root probe", calls.Load())
	}
	if ping(map[string]any{"proxy": "socks5://127.0.0.1:1080", "configJSON": `{"inbounds":[],"outbounds":[{"protocol":"freedom"}]}`}) || calls.Load() != 1 {
		t.Fatal("a probe through the local listener went to the root probe", calls.Load())
	}
	// An isolated probe of another server brings its own listener; it must still bypass the VPN.
	if !ping(map[string]any{"proxy": "socks5://u:p@127.0.0.1:1081", "configJSON": `{"inbounds":[{"protocol":"socks","listen":"127.0.0.1","port":1081}],"outbounds":[{"protocol":"freedom"}]}`}) || calls.Load() != 2 {
		t.Fatal("an isolated probe did not reach the root probe", calls.Load())
	}
}
