//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A portable bundle under the user's home must never be started through pkexec.
func TestLinuxTrustedRejectsUserWritablePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elarni-core")
	if err := os.WriteFile(path, []byte("core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 && linuxTrusted(path) {
		t.Fatal("a file the user owns was trusted")
	}
	if !linuxTrusted("/") {
		t.Fatal("the root directory was not trusted")
	}
	// Root helpers run whatever linuxTool returns, so a failed check must not fall back to it.
	if tool := linuxTool("elarni-core", filepath.Dir(path)); os.Geteuid() != 0 && tool != "" {
		t.Fatalf("an untrusted tool was returned: %s", tool)
	}
}

// pkexec runs the staged installer, so it must hold exactly the bytes whose digest names the helper.
func TestStagedLinuxInstallerHoldsTheDigestedBytes(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	source := filepath.Join(t.TempDir(), "elarni-core")
	if err := os.WriteFile(source, []byte("core"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := fileDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stageLinuxInstaller(source, digest)
	if err != nil {
		t.Fatal(err)
	}
	if copied, err := fileDigest(staged); err != nil || copied != digest || filepath.Dir(filepath.Dir(staged)) != cache {
		t.Fatalf("staged %s (digest %s, %v)", staged, copied, err)
	}
	if linuxFUSE(staged) {
		t.Fatal("a temporary directory was reported as FUSE")
	}
	if _, err := stageLinuxInstaller(source, strings.Repeat("0", len(digest))); err == nil {
		t.Fatal("a copy that differs from the digest was staged")
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 1 {
		t.Fatalf("a failed stage left %d entries in the cache", len(entries))
	}
}

// Root copies the ShadowQUIC client from the staged folder, and keeps it only when it is the
// one this core was built with.
func TestStagedLinuxInstallerBringsThePinnedShadowQUICClient(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	bundle := t.TempDir()
	source, client := filepath.Join(bundle, "elarni-core"), filepath.Join(bundle, shadowquicProgram)
	for path, content := range map[string]string{source: "core", client: "client"} {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	digest, _ := fileDigest(source)
	pinned, _ := fileDigest(client)
	t.Cleanup(func() { shadowquicSHA256 = "" })
	shadowquicSHA256 = pinned
	staged, err := stageLinuxInstaller(source, digest)
	if err != nil {
		t.Fatal(err)
	}
	if copied, err := fileDigest(filepath.Join(filepath.Dir(staged), shadowquicProgram)); err != nil || copied != pinned {
		t.Fatalf("staged client has digest %s (%v)", copied, err)
	}
	if err := os.RemoveAll(filepath.Dir(staged)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(client, []byte("another program"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := stageLinuxInstaller(source, digest); err == nil {
		t.Fatal("a client that differs from the pinned build was staged")
	}
	if err := os.Remove(client); err != nil {
		t.Fatal(err)
	}
	if _, err := stageLinuxInstaller(source, digest); err == nil {
		t.Fatal("a bundle without its client was staged")
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Fatalf("failed stages left %d entries in the cache", len(entries))
	}
}
