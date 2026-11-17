package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdateArgumentAdmitsOnlyAPlainAbsoluteDirectory(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "update")
	for directory, want := range map[string]bool{
		absolute:                             true,
		"update":                             false,
		"":                                   false,
		absolute + `" /qn TRANSFORMS="x`:     false,
		absolute + "\n":                      false,
		absolute + strings.Repeat("a", 1024): false,
	} {
		if got := updateArgument(directory); got != want {
			t.Errorf("%q: %v", directory, got)
		}
	}
}

func TestInstallStagedUpdateInstallsTheVerifiedCopyAndRemovesIt(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	var installed, staged string
	stage := func(from, target string) (string, error) {
		if from != source {
			t.Errorf("staged from %q", from)
		}
		if err := os.Mkdir(target, 0o700); err != nil {
			return "", err
		}
		staged = filepath.Join(target, "package")
		return staged, os.WriteFile(staged, []byte("verified"), 0o600)
	}
	err := installStagedUpdateWith(root, source, stage, func(path string) error {
		installed = path
		if filepath.Dir(filepath.Dir(path)) != root {
			t.Errorf("installed %q, outside the protected root", path)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "verified" {
			t.Errorf("installed %q, %v", data, err)
		}
		if runtime.GOOS != "windows" {
			// apt-get's sandbox user reads the package.
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
				t.Errorf("package mode %v, %v", info.Mode(), err)
			}
		}
		return nil
	})
	if err != nil || installed == "" || installed != staged {
		t.Fatalf("installed %q, staged %q: %v", installed, staged, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("left %v, %v", entries, err)
	}
}

func TestInstallStagedUpdateRunsNothingItCouldNotVerify(t *testing.T) {
	root := t.TempDir()
	refused := errors.New("update_signature_invalid")
	ran := false
	install := func(string) error { ran = true; return nil }
	err := installStagedUpdateWith(root, t.TempDir(), func(string, string) (string, error) { return "", refused }, install)
	if !errors.Is(err, refused) || ran {
		t.Fatalf("%v, installer ran: %v", err, ran)
	}
	if err := installStagedUpdateWith(root, "relative", func(string, string) (string, error) { return "", nil }, install); err == nil || ran {
		t.Fatalf("%v, installer ran: %v", err, ran)
	}
	// A failed installer still leaves no copy.
	failed := errors.New("installer failed")
	err = installStagedUpdateWith(root, t.TempDir(), func(_, target string) (string, error) {
		if err := os.Mkdir(target, 0o700); err != nil {
			return "", err
		}
		return filepath.Join(target, "package"), os.WriteFile(filepath.Join(target, "package"), nil, 0o600)
	}, func(string) error { return failed })
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("left %v, %v", entries, err)
	}
}

func TestMarkFailedUpdateKeepsOnlyTheVersion(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "package"), []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	markFailedUpdate(directory, "0.7.0")
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != failedUpdate {
		t.Fatalf("%v, %v", entries, err)
	}
	if data, _ := os.ReadFile(filepath.Join(directory, failedUpdate)); strings.TrimSpace(string(data)) != "0.7.0" {
		t.Fatalf("%q", data)
	}
}
