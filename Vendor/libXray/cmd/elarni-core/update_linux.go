//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xtls/libxray/share"
	"github.com/xtls/libxray/update"
)

// debCore is where the Debian package puts this binary (scripts/package-linux).
const debCore = "/opt/elarni/elarni-core"

// Root's copies of a download, in a directory only root can create entries in.
const updateStagingRoot = "/var/cache"

// runningAppImage is the image file this core was started from, or empty. Its runtime sets
// APPIMAGE to the file and APPDIR to the mount, which must hold this binary.
func runningAppImage(executable string) string {
	image, mount := os.Getenv("APPIMAGE"), strings.TrimRight(os.Getenv("APPDIR"), "/")
	if !filepath.IsAbs(image) || !filepath.IsAbs(mount) || !strings.HasPrefix(executable, mount+"/") {
		return ""
	}
	if info, err := os.Stat(image); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return image
}

// updateInstall reports a Debian package, whose update asks for the administrator's
// password, or an AppImage its user can replace. A portable folder is its user's to replace.
func updateInstall() (update.Kind, bool) {
	executable, err := os.Executable()
	if err != nil {
		return "", false
	}
	if image := runningAppImage(executable); image != "" {
		if syscall.Access(image, 2) == nil && syscall.Access(filepath.Dir(image), 2) == nil {
			return update.AppImage, true
		}
		return "", false
	}
	if _, err := os.Stat("/var/lib/dpkg/info/elarni.list"); err == nil && executable == debCore && linuxTrusted(executable) {
		return update.Deb, false
	}
	return "", false
}

func installUpdate(kind update.Kind, directory string, _ bool) (any, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, errors.New("update_failed")
	}
	if kind == update.AppImage {
		staged, err := update.Staged(directory, kind, share.AppVersion())
		if err != nil {
			return nil, err
		}
		if err := replaceAppImage(filepath.Join(directory, staged.Name), runningAppImage(executable), staged); err != nil {
			return nil, err
		}
	} else {
		err := exec.Command(linuxTool("pkexec", "/usr/bin", "/run/wrappers/bin"), executable, "--install-update", directory).Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) && (exit.ExitCode() == 126 || exit.ExitCode() == 127) {
			return nil, errors.New("elevation_denied")
		}
		if err != nil {
			if staged, stagedErr := update.Staged(directory, kind, share.AppVersion()); stagedErr == nil {
				markFailedUpdate(directory, staged.Version)
			}
			return nil, errors.New("update_failed")
		}
	}
	update.Discard(directory)
	// The running app is the old build until it starts again.
	return map[string]any{"restart": true}, nil
}

// replaceAppImage puts the downloaded image where the running one is, so every launcher,
// menu entry and login item that names the file starts the new build. The running app
// keeps its open image.
func replaceAppImage(source, image string, staged update.Package) error {
	if image == "" {
		return errors.New("update_unsupported")
	}
	// Beside the image: a rename within one file system replaces it whole or not at all.
	next := filepath.Join(filepath.Dir(image), "."+filepath.Base(image)+".update")
	_ = os.Remove(next)
	defer os.Remove(next)
	input, err := os.Open(source)
	if err != nil {
		return errors.New("update_failed")
	}
	defer input.Close()
	output, err := os.OpenFile(next, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return errors.New("update_failed")
	}
	_, err = io.Copy(output, input)
	if syncErr := output.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("update_failed")
	}
	if err := staged.CheckFile(next); err != nil {
		return err
	}
	if os.Rename(next, image) != nil {
		return errors.New("update_failed")
	}
	return nil
}

// installStagedUpdate runs as root behind pkexec. directory is its caller's and may hold
// anything: only the release key's signature on the copies made here lets the package install.
func installStagedUpdate(directory string) error {
	return installStagedUpdateWith(updateStagingRoot, directory, stageUpdate(update.Deb), func(path string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		// apt-get also installs what a new release starts to depend on; its sandbox user reads the file.
		tool, arguments := linuxTool("apt-get"), []string{"install", "-y", "--no-install-recommends", path}
		if tool == "" {
			tool, arguments = linuxTool("dpkg"), []string{"-i", path}
		}
		if tool == "" {
			return errors.New("update_unsupported")
		}
		command := exec.CommandContext(ctx, tool, arguments...)
		command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C"}
		return command.Run()
	})
}
