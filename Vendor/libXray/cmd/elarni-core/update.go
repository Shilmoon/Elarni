package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xtls/libxray/share"
	"github.com/xtls/libxray/update"
)

// The app downloads a release into its user's cache, where that user can change it. An
// install therefore trusts none of it: the installer copies the files to where only it can
// write and verifies the copies (update.Stage) before it runs anything.

// failedUpdate names the version whose install failed, so the app offers its page
// instead of installing it again at every start.
const failedUpdate = "failed"

func updateDirectory() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", errors.New("update_unavailable")
	}
	return filepath.Join(root, "Elarni", "update"), nil
}

// updateArgument admits a directory that is safe to pass on a command line.
func updateArgument(directory string) bool {
	return filepath.IsAbs(directory) && len(directory) <= 1024 && !strings.ContainsAny(directory, "\"\r\n\x00")
}

func updateRequest(method string, payload json.RawMessage) (any, error, bool) {
	if method != "updateStatus" && method != "downloadUpdate" && method != "installUpdate" {
		return nil, nil, false
	}
	// kind is empty for an install that does not replace itself: a portable folder, a
	// diagnostics build.
	kind, silent := updateInstall()
	directory, err := updateDirectory()
	if kind == "" || diagnosticsBuild || err != nil || !updateArgument(directory) {
		if method == "updateStatus" {
			return map[string]any{"kind": ""}, nil, true
		}
		return nil, errors.New("update_unsupported"), true
	}
	switch method {
	case "updateStatus":
		status := map[string]any{"kind": kind, "silent": silent}
		if failed, err := os.ReadFile(filepath.Join(directory, failedUpdate)); err == nil && len(failed) <= 32 {
			status["failed"] = strings.TrimSpace(string(failed))
		} else if staged, err := update.Staged(directory, kind, share.AppVersion()); err == nil {
			status["ready"] = staged.Version
		} else {
			// The installed build caught up with the download, or the download is damaged.
			update.Discard(directory)
		}
		return status, nil, true
	case "downloadUpdate":
		var request struct {
			Assets []update.Asset `json:"assets"`
			Proxy  string         `json:"proxy"`
		}
		if json.Unmarshal(payload, &request) != nil {
			return nil, errors.New("update_invalid"), true
		}
		data, err := share.DownloadUpdate(context.Background(), update.Request{Assets: request.Assets, Kind: kind, Directory: directory}, request.Proxy)
		if err != nil {
			return nil, err, true
		}
		data.(map[string]any)["silent"] = silent
		return data, nil, true
	}
	var request struct {
		Relaunch bool `json:"relaunch"`
	}
	if json.Unmarshal(payload, &request) != nil {
		return nil, errors.New("update_invalid"), true
	}
	if _, err := update.Staged(directory, kind, share.AppVersion()); err != nil {
		update.Discard(directory)
		return nil, err, true
	}
	data, err := installUpdate(kind, directory, request.Relaunch)
	return data, err, true
}

// stageUpdate is how an installer copies and verifies its caller's download (update.Stage).
func stageUpdate(kind update.Kind) func(source, target string) (string, error) {
	return func(source, target string) (string, error) {
		path, _, err := update.Stage(source, target, kind, share.AppVersion())
		return path, err
	}
}

// stagedUpdatePrefix names an installer's copy. The dot keeps the Windows helper install,
// which the new MSI runs while its package is read from here, from pruning it.
const stagedUpdatePrefix = ".elarni-update-"

// installStagedUpdateWith installs the verified copy that stage makes in a new directory under
// root, where only an administrator can create or change entries, and leaves no copy behind.
func installStagedUpdateWith(root, directory string, stage func(source, target string) (string, error), install func(path string) error) error {
	if !updateArgument(directory) {
		return errors.New("update_invalid")
	}
	// A killed install leaves its copy.
	if entries, err := os.ReadDir(root); err == nil {
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil && strings.HasPrefix(entry.Name(), stagedUpdatePrefix) && time.Since(info.ModTime()) > time.Hour {
				_ = os.RemoveAll(filepath.Join(root, entry.Name()))
			}
		}
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	target := filepath.Join(root, stagedUpdatePrefix+hex.EncodeToString(random[:]))
	path, err := stage(directory, target)
	if err != nil {
		return err
	}
	defer os.RemoveAll(target)
	// Windows gives the copy the root's access list. Elsewhere apt-get's sandbox user reads it.
	if runtime.GOOS != "windows" && (os.Chmod(target, 0o755) != nil || os.Chmod(path, 0o644) != nil) {
		return errors.New("update_failed")
	}
	return install(path)
}

// markFailedUpdate replaces a download whose install failed with its version alone.
func markFailedUpdate(directory, version string) {
	update.Discard(directory)
	_ = os.WriteFile(filepath.Join(directory, failedUpdate), []byte(version+"\n"), 0o600)
}
