//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/xtls/libxray/update"
)

func TestRunningAppImageNeedsTheRuntimesFileAndMount(t *testing.T) {
	directory := t.TempDir()
	image, mount := filepath.Join(directory, "Elarni.AppImage"), filepath.Join(directory, "mount")
	if err := os.WriteFile(image, []byte("image"), 0o755); err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(mount, "elarni-core")
	for name, test := range map[string]struct{ image, mount, executable, want string }{
		"an AppImage":            {image, mount, core, image},
		"a trailing slash":       {image, mount + "/", core, image},
		"launched by another":    {image, mount, "/opt/elarni/elarni-core", ""},
		"no runtime":             {"", "", core, ""},
		"a relative image":       {"Elarni.AppImage", mount, core, ""},
		"an image that is gone":  {image + ".gone", mount, core, ""},
		"a directory for a file": {directory, mount, core, ""},
	} {
		t.Setenv("APPIMAGE", test.image)
		t.Setenv("APPDIR", test.mount)
		if got := runningAppImage(test.executable); got != test.want {
			t.Errorf("%s: %q", name, got)
		}
	}
}

func TestReplaceAppImageSwapsTheFileWholeOrNotAtAll(t *testing.T) {
	directory := t.TempDir()
	image, source := filepath.Join(directory, "Elarni-0.6.1-16-x86_64.AppImage"), filepath.Join(t.TempDir(), "download")
	if err := os.WriteFile(image, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new build"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("new build"))
	staged := update.Package{Name: "Elarni-0.7.0-17-x86_64.AppImage", Version: "0.7.0", SHA256: hex.EncodeToString(sum[:])}
	// A download that changed after its check replaces nothing.
	wrong := staged
	wrong.SHA256 = hex.EncodeToString(make([]byte, 32))
	if err := replaceAppImage(source, image, wrong); err == nil {
		t.Fatal("a changed download replaced the image")
	}
	if data, _ := os.ReadFile(image); string(data) != "old build" {
		t.Fatalf("the image is %q", data)
	}
	if err := replaceAppImage(source, image, staged); err != nil {
		t.Fatal(err)
	}
	// The path every launcher names now holds the new build, and it runs.
	info, err := os.Stat(image)
	if data, _ := os.ReadFile(image); err != nil || string(data) != "new build" || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the image is %q, %v, %v", data, info.Mode(), err)
	}
	if entries, _ := os.ReadDir(directory); len(entries) != 1 {
		t.Fatalf("left %v", entries)
	}
	if err := replaceAppImage(source, "", staged); err == nil {
		t.Fatal("replaced no image")
	}
}
