package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// site serves a signed release of version and counts the fetches of each address.
type site struct {
	files   map[string][]byte
	fetched map[string]int
}

func newSite(private ed25519.PrivateKey, version string, body []byte) (*site, []Asset) {
	name := "elarni_" + version + "-20_amd64.deb"
	sum := sha256.Sum256(body)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	server := &site{fetched: map[string]int{}, files: map[string][]byte{
		"https://releases.test/" + name:          body,
		"https://releases.test/" + SumsName:      sums,
		"https://releases.test/" + SignatureName: Sign(private, sums),
	}}
	return server, []Asset{
		{Name: "notes.txt", URL: "https://releases.test/notes.txt"},
		{Name: name, URL: "https://releases.test/" + name},
		{Name: SumsName, URL: "https://releases.test/" + SumsName},
		{Name: SignatureName, URL: "https://releases.test/" + SignatureName},
	}
}

func (s *site) fetch(_ context.Context, address string, limit int64, destination string) ([]byte, error) {
	s.fetched[address]++
	body, found := s.files[address]
	if !found {
		return nil, errors.New("subscription_unavailable")
	}
	if int64(len(body)) > limit {
		return nil, errors.New("input_too_large")
	}
	if destination == "" {
		return body, nil
	}
	return nil, os.WriteFile(destination, body, 0o600)
}

func entries(t *testing.T, directory string) []string {
	t.Helper()
	list, err := os.ReadDir(directory)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range list {
		names = append(names, entry.Name())
	}
	return names
}

func TestDownloadStagesAVerifiedReleaseOnce(t *testing.T) {
	private := testKey(t)
	server, assets := newSite(private, "0.7.0", []byte("package"))
	directory := filepath.Join(t.TempDir(), "update")
	request := Request{Assets: assets, Kind: Deb, Directory: directory}
	for range 2 {
		got, err := Download(context.Background(), request, "0.6.1", server.fetch)
		if err != nil || got.Version != "0.7.0" || got.Name != "elarni_0.7.0-20_amd64.deb" {
			t.Fatalf("%+v, %v", got, err)
		}
		if staged, err := Staged(directory, Deb, "0.6.1"); err != nil || staged != got {
			t.Fatalf("%+v, %v", staged, err)
		}
	}
	// The second run reads the small files again and keeps the package it has.
	if count := server.fetched["https://releases.test/elarni_0.7.0-20_amd64.deb"]; count != 1 {
		t.Fatalf("the package was fetched %d times", count)
	}
	if got := entries(t, directory); len(got) != 3 {
		t.Fatalf("staged %v", got)
	}
}

func TestDownloadReplacesAnOlderDownload(t *testing.T) {
	private := testKey(t)
	directory := filepath.Join(t.TempDir(), "update")
	for _, version := range []string{"0.7.0", "0.7.1"} {
		server, assets := newSite(private, version, []byte("package "+version))
		got, err := Download(context.Background(), Request{Assets: assets, Kind: Deb, Directory: directory}, "0.6.1", server.fetch)
		if err != nil || got.Version != version {
			t.Fatalf("%+v, %v", got, err)
		}
	}
	if got := entries(t, directory); len(got) != 3 {
		t.Fatalf("staged %v", got)
	}
}

func TestDownloadRefusesAnUnsignedOrChangedRelease(t *testing.T) {
	private := testKey(t)
	without := func(assets []Asset, name string) []Asset {
		var kept []Asset
		for _, asset := range assets {
			if asset.Name != name {
				kept = append(kept, asset)
			}
		}
		return kept
	}
	for name, test := range map[string]struct {
		change func(*site, []Asset) []Asset
		want   string
	}{
		"no signature": {func(_ *site, assets []Asset) []Asset { return without(assets, SignatureName) }, "update_unsigned"},
		"no sums":      {func(_ *site, assets []Asset) []Asset { return without(assets, SumsName) }, "update_unsigned"},
		"cleartext signature": {func(_ *site, assets []Asset) []Asset {
			return append(without(assets, SignatureName), Asset{Name: SignatureName, URL: "http://releases.test/" + SignatureName})
		}, "update_unsigned"},
		"no package": {func(_ *site, assets []Asset) []Asset { return without(assets, "elarni_0.7.0-20_amd64.deb") }, "update_package_missing"},
		"changed package": {func(server *site, assets []Asset) []Asset {
			server.files["https://releases.test/elarni_0.7.0-20_amd64.deb"] = []byte("another")
			return assets
		}, "update_checksum_mismatch"},
		"changed sums": {func(server *site, assets []Asset) []Asset {
			server.files["https://releases.test/"+SumsName] = []byte("0000000000000000000000000000000000000000000000000000000000000000  elarni_0.7.0-20_amd64.deb\n")
			return assets
		}, "update_signature_invalid"},
		"unreachable package": {func(server *site, assets []Asset) []Asset {
			delete(server.files, "https://releases.test/elarni_0.7.0-20_amd64.deb")
			return assets
		}, "subscription_unavailable"},
		"relative directory": {func(_ *site, assets []Asset) []Asset { return assets }, "update_invalid"},
	} {
		server, assets := newSite(private, "0.7.0", []byte("package"))
		directory := filepath.Join(t.TempDir(), "update")
		if name == "relative directory" {
			directory = "update"
		}
		_, err := Download(context.Background(), Request{Assets: test.change(server, assets), Kind: Deb, Directory: directory}, "0.6.1", server.fetch)
		if errorText(err) != test.want {
			t.Errorf("%s: got %q, want %q", name, errorText(err), test.want)
		}
		if got := entries(t, directory); len(got) != 0 {
			t.Errorf("%s: left %v", name, got)
		}
	}
}

func TestDownloadKeepsNothingOfAnOlderReleaseItCannotReplace(t *testing.T) {
	private := testKey(t)
	directory := filepath.Join(t.TempDir(), "update")
	server, assets := newSite(private, "0.7.0", []byte("package"))
	if _, err := Download(context.Background(), Request{Assets: assets, Kind: Deb, Directory: directory}, "0.6.1", server.fetch); err != nil {
		t.Fatal(err)
	}
	// The running build caught up: the feed's release is no longer newer.
	if _, err := Download(context.Background(), Request{Assets: assets, Kind: Deb, Directory: directory}, "0.7.0", server.fetch); errorText(err) != "update_not_newer" {
		t.Fatal(err)
	}
	if got := entries(t, directory); len(got) != 0 {
		t.Fatalf("left %v", got)
	}
}
