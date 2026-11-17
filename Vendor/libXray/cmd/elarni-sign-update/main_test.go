package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAKeyIsNeverOverwrittenAndAnUnknownKeySignsNothing(t *testing.T) {
	directory := t.TempDir()
	key, sums := filepath.Join(directory, "key"), filepath.Join(directory, "SHA256SUMS")
	if err := run(key, "", nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(key); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v, %v", info, err)
	}
	first, _ := os.ReadFile(key)
	if err := run(key, "", nil); err == nil {
		t.Fatal("a second -generate replaced the key")
	}
	if again, _ := os.ReadFile(key); string(again) != string(first) {
		t.Fatal("the key changed")
	}
	if err := os.WriteFile(sums, []byte(strings.Repeat("a", 64)+"  elarni_0.7.0-20_amd64.deb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The new key is not one the build trusts, so its signature would update nobody.
	if err := run("", key, []string{sums}); err == nil {
		t.Fatal("signed with a key no build trusts")
	}
	if _, err := os.Stat(sums + ".sig"); !os.IsNotExist(err) {
		t.Fatalf("a signature was written: %v", err)
	}
}
