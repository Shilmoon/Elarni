// Package update decides whether a downloaded release may replace this build (Elarni).
//
// A release publishes SHA256SUMS and SHA256SUMS.sig, the release key's Ed25519 signature of it.
// The installers run with more rights than the app that downloaded the files, so they trust
// only what this package verified: the signature, the package's digest, and that the version
// in the signed package name is newer than the running one.
package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Kind is a package form that can be installed over itself.
type Kind string

const (
	MSI      Kind = "msi"
	Deb      Kind = "deb"
	AppImage Kind = "appimage"
	APK      Kind = "apk"
)

// SumsName and SignatureName are the release files that name and sign the packages.
const (
	SumsName      = "SHA256SUMS"
	SignatureName = "SHA256SUMS.sig"
)

// The names scripts/package-windows, scripts/package-linux and the release machine give
// the packages. A diagnostics build has another name and never matches.
var names = map[Kind]*regexp.Regexp{
	MSI:      regexp.MustCompile(`^Elarni-(\d+\.\d+\.\d+)-\d+-windows-x64(?:-UNSIGNED)?\.msi$`),
	Deb:      regexp.MustCompile(`^elarni_(\d+\.\d+\.\d+)-\d+_amd64\.deb$`),
	AppImage: regexp.MustCompile(`^Elarni-(\d+\.\d+\.\d+)-\d+-x86_64\.AppImage$`),
	APK:      regexp.MustCompile(`^Elarni-(\d+\.\d+\.\d+)-\d+-android-arm64\.apk$`),
}

// The signature covers this line and then the file, so it is good for nothing else.
const signedFor = "elarni-update-v1\n"

// The release keys' public halves. A new key is added before the old one retires, so
// the releases in between are signed by a key every installed build knows.
var keys = publicKeys(
	"cX8DIk4pLsEYHDd41RHWqeUQcjglqxmLgpfTp/9aAgg=",
	extraKey,
)

// extraKey is set by the linker in builds that test updates against their own releases
// (-ldflags "-X github.com/xtls/libxray/update.extraKey=<base64>"); release builds leave it empty.
var extraKey string

func publicKeys(encoded ...string) []ed25519.PublicKey {
	var result []ed25519.PublicKey
	for _, text := range encoded {
		if key, err := base64.StdEncoding.DecodeString(text); err == nil && len(key) == ed25519.PublicKeySize {
			result = append(result, key)
		}
	}
	return result
}

// Package is one file of a release.
type Package struct {
	Name    string
	Version string // major.minor.patch
	SHA256  string // lowercase hex
}

var (
	errSignature = errors.New("update_signature_invalid")
	errInvalid   = errors.New("update_invalid")
	errMissing   = errors.New("update_package_missing")
	errNotNewer  = errors.New("update_not_newer")
	errChecksum  = errors.New("update_checksum_mismatch")
)

// Sign returns the contents of SHA256SUMS.sig for sums.
func Sign(key ed25519.PrivateKey, sums []byte) []byte {
	signature := ed25519.Sign(key, append([]byte(signedFor), sums...))
	return []byte(base64.StdEncoding.EncodeToString(signature) + "\n")
}

// Signed reports whether signature, the contents of SHA256SUMS.sig, is a release key's
// signature of sums.
func Signed(sums, signature []byte) bool {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(decoded) != ed25519.SignatureSize {
		return false
	}
	message := append([]byte(signedFor), sums...)
	for _, key := range keys {
		if ed25519.Verify(key, message, decoded) {
			return true
		}
	}
	return false
}

// Select returns the release's package of this kind once a release key signed sums and the
// package is newer than current, the running build's version (a "+build" suffix is ignored).
func Select(sums, signature []byte, kind Kind, current string) (Package, error) {
	pattern := names[kind]
	if pattern == nil || len(sums) > 64*1024 {
		return Package{}, errInvalid
	}
	if !Signed(sums, signature) {
		return Package{}, errSignature
	}
	running, ok := versionNumbers(strings.SplitN(current, "+", 2)[0])
	if !ok {
		return Package{}, errInvalid
	}
	var found *Package
	for _, line := range bytes.Split(sums, []byte("\n")) {
		// "<digest>  <name>", or " *<name>" as sha256sum prints for binary mode.
		text := strings.TrimSuffix(string(line), "\r")
		if len(text) < 67 || text[64] != ' ' || text[65] != ' ' && text[65] != '*' {
			continue
		}
		digest, name := strings.ToLower(text[:64]), text[66:]
		if raw, err := hex.DecodeString(digest); err != nil || len(raw) != sha256.Size {
			continue
		}
		match := pattern.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		if found != nil {
			return Package{}, errInvalid
		}
		found = &Package{Name: name, Version: match[1], SHA256: digest}
	}
	if found == nil {
		return Package{}, errMissing
	}
	offered, _ := versionNumbers(found.Version)
	for index := range offered {
		if offered[index] != running[index] {
			if offered[index] < running[index] {
				return Package{}, errNotNewer
			}
			return *found, nil
		}
	}
	return Package{}, errNotNewer
}

func versionNumbers(version string) ([3]int, bool) {
	var result [3]int
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return result, false
	}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 || len(part) > 9 {
			return result, false
		}
		result[index] = number
	}
	return result, true
}

// CheckFile reports whether the file at path is this package.
func (p Package) CheckFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return errChecksum
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil || hex.EncodeToString(hash.Sum(nil)) != p.SHA256 {
		return errChecksum
	}
	return nil
}

func readSigned(directory string, kind Kind, current string) (Package, error) {
	sums, err := os.ReadFile(filepath.Join(directory, SumsName))
	if err != nil {
		return Package{}, errMissing
	}
	signature, err := os.ReadFile(filepath.Join(directory, SignatureName))
	if err != nil {
		return Package{}, errMissing
	}
	return Select(sums, signature, kind, current)
}

// Staged returns the package a download left in directory, once it is whole and verified.
func Staged(directory string, kind Kind, current string) (Package, error) {
	found, err := readSigned(directory, kind, current)
	if err != nil {
		return Package{}, err
	}
	return found, found.CheckFile(filepath.Join(directory, found.Name))
}

// maxPackage bounds what an installer copies; the largest package is under 60 MB.
const maxPackage = 512 * 1024 * 1024

// Stage copies a download from source, which its user can still change, into the new
// directory target, which only the installer can, and verifies the copies. It returns the
// copied package's path. A refused download leaves no target.
func Stage(source, target string, kind Kind, current string) (string, Package, error) {
	// Mkdir fails on an existing target, so nothing another program put there is trusted.
	if err := os.Mkdir(target, 0o700); err != nil {
		return "", Package{}, errInvalid
	}
	path, found, err := stage(source, target, kind, current)
	if err != nil {
		os.RemoveAll(target)
	}
	return path, found, err
}

func stage(source, target string, kind Kind, current string) (string, Package, error) {
	for _, name := range []string{SumsName, SignatureName} {
		if err := copyRegular(filepath.Join(source, name), filepath.Join(target, name), 64*1024); err != nil {
			return "", Package{}, err
		}
	}
	found, err := readSigned(target, kind, current)
	if err != nil {
		return "", Package{}, err
	}
	path := filepath.Join(target, found.Name)
	if err := copyRegular(filepath.Join(source, found.Name), path, maxPackage); err != nil {
		return "", Package{}, err
	}
	return path, found, found.CheckFile(path)
}

// copyRegular copies a regular file of at most limit bytes; a link, device or pipe is refused.
func copyRegular(source, destination string, limit int64) error {
	if info, err := os.Lstat(source); err != nil || !info.Mode().IsRegular() {
		if err != nil && os.IsNotExist(err) {
			return errMissing
		}
		return errInvalid
	}
	input, err := os.Open(source)
	if err != nil {
		return errInvalid
	}
	defer input.Close()
	if info, err := input.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return errInvalid
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errInvalid
	}
	copied, err := io.Copy(output, io.LimitReader(input, limit+1))
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil || copied > limit {
		return errInvalid
	}
	return nil
}
