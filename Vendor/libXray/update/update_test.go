package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The file list of the 0.6.1 release, with the digests replaced.
const releaseSums = `1111111111111111111111111111111111111111111111111111111111111111  Elarni-0.6.1-16-android-arm64.apk
2222222222222222222222222222222222222222222222222222222222222222  Elarni-0.6.1-16-go-module-sources.tar.gz
3333333333333333333333333333333333333333333333333333333333333333  elarni-0.6.1-16-linux-x64.tar.gz
4444444444444444444444444444444444444444444444444444444444444444  Elarni-0.6.1-16-project-source.tar.gz
5555555555555555555555555555555555555555555555555555555555555555  Elarni-0.6.1-16-windows-x64-UNSIGNED.msi
6666666666666666666666666666666666666666666666666666666666666666  Elarni-0.6.1-16-windows-x64-UNSIGNED.zip
7777777777777777777777777777777777777777777777777777777777777777  Elarni-0.6.1-16-x86_64.AppImage
8888888888888888888888888888888888888888888888888888888888888888  Elarni-0.6.1-16.intoto.jsonl
9999999999999999999999999999999999999999999999999999999999999999  elarni_0.6.1-16_amd64.deb
`

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	saved := keys
	keys = []ed25519.PublicKey{public}
	t.Cleanup(func() { keys = saved })
	return private
}

func TestSelectFindsEachPackageOfASignedRelease(t *testing.T) {
	private := testKey(t)
	signature := Sign(private, []byte(releaseSums))
	for kind, want := range map[Kind]Package{
		MSI:      {"Elarni-0.6.1-16-windows-x64-UNSIGNED.msi", "0.6.1", strings.Repeat("5", 64)},
		Deb:      {"elarni_0.6.1-16_amd64.deb", "0.6.1", strings.Repeat("9", 64)},
		AppImage: {"Elarni-0.6.1-16-x86_64.AppImage", "0.6.1", strings.Repeat("7", 64)},
		APK:      {"Elarni-0.6.1-16-android-arm64.apk", "0.6.1", strings.Repeat("1", 64)},
	} {
		got, err := Select([]byte(releaseSums), signature, kind, "0.6.0")
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v; want %+v", kind, got, err, want)
		}
	}
}

func TestSelectRefusesWhatTheReleaseKeyDidNotSign(t *testing.T) {
	private := testKey(t)
	sums := []byte(releaseSums)
	good := Sign(private, sums)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	raw := base64.StdEncoding.EncodeToString(ed25519.Sign(private, sums)) // without the context line
	changed := []byte(strings.Replace(releaseSums, "5555", "5556", 1))
	for name, test := range map[string]struct{ sums, signature []byte }{
		"another key":         {sums, Sign(other, sums)},
		"changed sums":        {changed, good},
		"no context":          {sums, []byte(raw)},
		"not base64":          {sums, []byte("!!!")},
		"short signature":     {sums, []byte(base64.StdEncoding.EncodeToString([]byte("short")))},
		"empty signature":     {sums, nil},
		"signature of a line": {sums[:100], good},
	} {
		if _, err := Select(test.sums, test.signature, MSI, "0.6.0"); err == nil || err.Error() != "update_signature_invalid" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Select(sums, good, MSI, "0.6.0"); err != nil {
		t.Fatal(err)
	}
}

func TestSelectTakesOnlyANewerRelease(t *testing.T) {
	private := testKey(t)
	signature := Sign(private, []byte(releaseSums))
	for current, want := range map[string]string{
		"0.6.0":    "",
		"0.5.9+40": "",
		"0.6.1":    "update_not_newer",
		"0.6.1+15": "update_not_newer", // the build number never decides
		"0.6.2":    "update_not_newer",
		"0.10.0":   "update_not_newer",
		"1.0":      "update_invalid",
		"":         "update_invalid",
		"0.6.0-rc": "update_invalid",
	} {
		_, err := Select([]byte(releaseSums), signature, Deb, current)
		if got := errorText(err); got != want {
			t.Errorf("current %q: got %q, want %q", current, got, want)
		}
	}
}

func TestSelectReadsOnlyPlainUnambiguousNames(t *testing.T) {
	private := testKey(t)
	digest := strings.Repeat("a", 64)
	for name, test := range map[string]struct {
		sums string
		want string
	}{
		"no package of the kind": {digest + "  Elarni-0.7.0-17-windows-x64-UNSIGNED.zip\n", "update_package_missing"},
		"diagnostics build":      {digest + "  Elarni-0.7.0-17-windows-x64-DIAGNOSTICS-UNSIGNED.msi\n", "update_package_missing"},
		"path in the name":       {digest + "  ../Elarni-0.7.0-17-windows-x64.msi\n", "update_package_missing"},
		"two packages":           {digest + "  Elarni-0.7.0-17-windows-x64.msi\n" + digest + "  Elarni-0.7.1-18-windows-x64.msi\n", "update_invalid"},
		"short digest":           {"abc  Elarni-0.7.0-17-windows-x64.msi\n", "update_package_missing"},
		"signed msi":             {digest + "  Elarni-0.7.0-17-windows-x64.msi\n", ""},
		"binary mark and CRLF":   {strings.ToUpper(digest) + " *Elarni-0.7.0-17-windows-x64-UNSIGNED.msi\r\n", ""},
		"too large":              {strings.Repeat(digest+"  filler\n", 2000) + digest + "  Elarni-0.7.0-17-windows-x64.msi\n", "update_invalid"},
	} {
		got, err := Select([]byte(test.sums), Sign(private, []byte(test.sums)), MSI, "0.6.1")
		if errorText(err) != test.want {
			t.Errorf("%s: got %q, want %q", name, errorText(err), test.want)
		}
		if err == nil && (got.Version != "0.7.0" || got.SHA256 != digest) {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestCheckFileComparesTheDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package")
	if err := os.WriteFile(path, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("release"))
	if err := (Package{SHA256: hex.EncodeToString(sum[:])}).CheckFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("released"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Package{SHA256: hex.EncodeToString(sum[:])}).CheckFile(path); errorText(err) != "update_checksum_mismatch" {
		t.Fatal(err)
	}
	if err := (Package{SHA256: hex.EncodeToString(sum[:])}).CheckFile(path + ".missing"); errorText(err) != "update_checksum_mismatch" {
		t.Fatal(err)
	}
}

func TestTheEmbeddedKeysAreEd25519PublicKeys(t *testing.T) {
	if len(keys) == 0 {
		t.Fatal("no release key")
	}
	for _, key := range keys {
		if len(key) != ed25519.PublicKeySize {
			t.Fatalf("key of %d bytes", len(key))
		}
	}
}

// release writes a signed release of version into a new directory and returns it with the package's name.
func release(t *testing.T, private ed25519.PrivateKey, version string, body []byte) (string, string) {
	t.Helper()
	directory := t.TempDir()
	name := "elarni_" + version + "-20_amd64.deb"
	sum := sha256.Sum256(body)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	for file, data := range map[string][]byte{name: body, SumsName: sums, SignatureName: Sign(private, sums)} {
		if err := os.WriteFile(filepath.Join(directory, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return directory, name
}

func TestStagedAcceptsOnlyAWholeVerifiedDownload(t *testing.T) {
	private := testKey(t)
	directory, name := release(t, private, "0.7.0", []byte("package"))
	got, err := Staged(directory, Deb, "0.6.1")
	if err != nil || got.Name != name || got.Version != "0.7.0" {
		t.Fatalf("%+v, %v", got, err)
	}
	if _, err := Staged(directory, Deb, "0.7.0"); errorText(err) != "update_not_newer" {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Staged(directory, Deb, "0.6.1"); errorText(err) != "update_checksum_mismatch" {
		t.Fatal(err)
	}
	if _, err := Staged(t.TempDir(), Deb, "0.6.1"); errorText(err) != "update_package_missing" {
		t.Fatal(err)
	}
}

func TestStageCopiesWhatItVerifiedAndNothingElse(t *testing.T) {
	private := testKey(t)
	source, name := release(t, private, "0.7.0", []byte("package"))
	target := filepath.Join(t.TempDir(), "protected")
	path, got, err := Stage(source, target, Deb, "0.6.1")
	if err != nil || path != filepath.Join(target, name) || got.Version != "0.7.0" {
		t.Fatalf("%q, %+v, %v", path, got, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "package" {
		t.Fatalf("%q, %v", data, err)
	}
	// The source changes after the copy: the copy is what was verified.
	if err := os.WriteFile(filepath.Join(source, name), []byte("swapped"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := got.CheckFile(path); err != nil {
		t.Fatal(err)
	}
	// A swapped package never stays in the protected directory.
	second := filepath.Join(t.TempDir(), "protected")
	if _, _, err := Stage(source, second, Deb, "0.6.1"); errorText(err) != "update_checksum_mismatch" {
		t.Fatal(err)
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatalf("the refused copy stayed: %v", err)
	}
	// An existing target is never reused: another program may have filled it.
	if _, _, err := Stage(source, target, Deb, "0.6.1"); err == nil {
		t.Fatal("staged into an existing directory")
	}
}

func TestStageRefusesLinks(t *testing.T) {
	private := testKey(t)
	source, name := release(t, private, "0.7.0", []byte("package"))
	real := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(filepath.Join(source, name), real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(source, name)); err != nil {
		t.Skip("no symbolic links here")
	}
	target := filepath.Join(t.TempDir(), "protected")
	if _, _, err := Stage(source, target, Deb, "0.6.1"); errorText(err) != "update_invalid" {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the refused copy stayed: %v", err)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
