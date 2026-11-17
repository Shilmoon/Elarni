//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xtls/libxray/share"
	"github.com/xtls/libxray/update"
	"golang.org/x/sys/windows"
)

// An MSI install updates in three processes, none of which runs from the folder the MSI
// replaces:
//
//   - the app's core starts this build's protected helper copy as the user (--apply-update)
//     and tells the app to exit;
//   - that copy waits for the app, has the MSI installed and starts the app again;
//   - the install itself (--install-update) runs the helper copy as SYSTEM through the
//     ElarniVPN service, or elevated behind a consent prompt for an account without it.
const updateInstallTimeout = 15 * time.Minute

// windowsInstallDirectory is where the MSI puts the app (windows/installer/Elarni.wxs).
func windowsInstallDirectory() (string, error) {
	programs, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(programs, "Elarni"), nil
}

// installedHelper is this build's protected copy, or empty when it is not installed.
func installedHelper() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	root, err := windowsHelperRoot()
	if err != nil {
		return ""
	}
	digest, err := fileDigest(executable)
	if err != nil {
		return ""
	}
	directory := helperDirectory(root, digest)
	if !windowsHelperInstalled(directory) {
		return ""
	}
	return filepath.Join(directory, "elarni-core.exe")
}

// updateInstall reports an MSI install, and whether its update needs no consent prompt.
// A portable folder is its user's to replace.
func updateInstall() (update.Kind, bool) {
	executable, err := os.Executable()
	if err != nil {
		return "", false
	}
	directory, err := windowsInstallDirectory()
	if err != nil || !strings.EqualFold(filepath.Dir(executable), directory) || installedHelper() == "" {
		return "", false
	}
	enabled, _ := vpnServiceStatus().(map[string]any)["enabled"].(bool)
	return update.MSI, enabled
}

// installUpdate hands the update to the helper copy, which outlives this core and the app.
func installUpdate(_ update.Kind, directory string, relaunch bool) (any, error) {
	helper := installedHelper()
	if helper == "" {
		return nil, errors.New("update_unsupported")
	}
	command := exec.Command(helper, "--apply-update", directory, strconv.Itoa(os.Getppid()), strconv.FormatBool(relaunch))
	command.Dir = filepath.Dir(helper)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
	if err := command.Start(); err != nil {
		return nil, errors.New("update_failed")
	}
	_ = command.Process.Release()
	// The install starts once the app is gone.
	return map[string]any{"exit": true}, nil
}

// applyUpdate runs as the user from the helper copy. Its exit code is for tests: nobody waits for it.
func applyUpdate(directory string, app uint32, relaunch bool) int {
	if process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, app); err == nil {
		// The installer ends an app that is still running after this.
		windows.WaitForSingleObject(process, 60000)
		windows.CloseHandle(process)
	}
	staged, err := update.Staged(directory, update.MSI, share.AppVersion())
	if err == nil {
		err = elevateUpdate(directory)
	}
	if err == nil {
		update.Discard(directory)
	} else if staged.Version != "" {
		markFailedUpdate(directory, staged.Version)
	}
	// The app exited for this update, so it comes back whatever became of the install.
	if relaunch {
		// A failed install left the old build, which reports the failure.
		if install, pathErr := windowsInstallDirectory(); pathErr == nil {
			command := exec.Command(filepath.Join(install, "elarni.exe"))
			command.Dir = install
			command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
			if command.Start() == nil {
				_ = command.Process.Release()
			}
		}
	}
	if err != nil {
		return 1
	}
	return 0
}

// elevateUpdate has the staged update installed and waits for it: through the service for an
// account it serves, otherwise behind a consent prompt.
func elevateUpdate(directory string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	var process windows.Handle
	if reply, err := askVPNService(serviceRequest{Action: "update", Directory: directory}); err == nil && reply.Handle != 0 {
		process = windows.Handle(reply.Handle)
	} else if process, err = shellRunAs(executable, windows.ComposeCommandLine([]string{"--install-update", directory})); err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	if state, err := windows.WaitForSingleObject(process, uint32(updateInstallTimeout/time.Millisecond)); err != nil || state != windows.WAIT_OBJECT_0 {
		return errors.New("update_failed")
	}
	var code uint32
	if windows.GetExitCodeProcess(process, &code) != nil || code != 0 {
		return errors.New("update_failed")
	}
	return nil
}

// startUpdateChild runs this helper's --install-update for the service, as SYSTEM.
func startUpdateChild(directory string) (windows.Handle, error) {
	return startHelperChild("--install-update", directory)
}

// protectHelperRoot creates the helper root, which only administrators and SYSTEM may change.
func protectHelperRoot() (string, error) {
	root, err := windowsHelperRoot()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	// An explicit protected DACL keeps the folder admin-only whatever it inherited.
	descriptor, err := windows.SecurityDescriptorFromString(windowsHelperACL)
	if err != nil {
		return "", err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return "", err
	}
	return root, windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// installStagedUpdate runs elevated or as SYSTEM. directory is its caller's and may hold
// anything: only the release key's signature on the copies made here lets the MSI run.
func installStagedUpdate(directory string) error {
	root, err := protectHelperRoot()
	if err != nil {
		return err
	}
	return installStagedUpdateWith(root, directory, stageUpdate(update.MSI), func(path string) error {
		install, err := windowsInstallDirectory()
		if err != nil {
			return err
		}
		system, err := windows.GetSystemDirectory()
		if err != nil {
			return err
		}
		// A running app keeps its files locked, and Windows Installer would ask for a restart.
		stopApp(install)
		command := exec.Command(filepath.Join(system, "msiexec.exe"), "/i", path, "/qn", "/norestart")
		command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
		if err := command.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case err := <-done:
			var exit *exec.ExitError
			// 3010: installed, and a restart finishes replacing a file that was in use.
			if errors.As(err, &exit) && exit.ExitCode() == 3010 {
				return nil
			}
			return err
		case <-time.After(updateInstallTimeout - time.Minute):
			// Windows Installer rolls back an install whose client is gone.
			_ = command.Process.Kill()
			return errors.New("update_failed")
		}
	})
}
