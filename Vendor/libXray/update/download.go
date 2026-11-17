package update

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

// Asset is a file of a release and where the update feed says it is.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Request asks for the feed's release in the form this install has.
type Request struct {
	Assets []Asset `json:"assets"`
	Kind   Kind    `json:"kind"`
	// Directory holds this download only: Download deletes whatever else is in it.
	Directory string `json:"directory"`
}

// Fetch reads address, which is https, into memory, or into the file destination when
// that is set. It fails on a body of more than limit bytes.
type Fetch func(ctx context.Context, address string, limit int64, destination string) ([]byte, error)

var errUnsigned = errors.New("update_unsigned")

// Download leaves the release's package for this install in request.Directory, beside the
// signed sums that vouch for it, where Staged and Stage find them. A release that is unsigned,
// not newer than current or damaged leaves the directory empty. A package an earlier run
// downloaded is kept as it is.
func Download(ctx context.Context, request Request, current string, fetch Fetch) (Package, error) {
	if !filepath.IsAbs(request.Directory) || len(request.Assets) > 1000 {
		return Package{}, errInvalid
	}
	found, err := download(ctx, request, current, fetch)
	if err != nil {
		clearDirectory(request.Directory)
	}
	return found, err
}

func download(ctx context.Context, request Request, current string, fetch Fetch) (Package, error) {
	address := func(name string) string {
		for _, asset := range request.Assets {
			if target, err := url.Parse(asset.URL); asset.Name == name && err == nil && target.Scheme == "https" && target.Host != "" {
				return asset.URL
			}
		}
		return ""
	}
	// A release from before updates were signed, or a feed that lists no files.
	if address(SumsName) == "" || address(SignatureName) == "" {
		return Package{}, errUnsigned
	}
	sums, err := fetch(ctx, address(SumsName), 64*1024, "")
	if err != nil {
		return Package{}, err
	}
	signature, err := fetch(ctx, address(SignatureName), 4*1024, "")
	if err != nil {
		return Package{}, err
	}
	found, err := Select(sums, signature, request.Kind, current)
	if err != nil {
		return Package{}, err
	}
	if staged, err := Staged(request.Directory, request.Kind, current); err == nil && staged == found {
		return found, nil
	}
	source := address(found.Name)
	if source == "" {
		return Package{}, errMissing
	}
	clearDirectory(request.Directory)
	if err := os.MkdirAll(request.Directory, 0o700); err != nil {
		return Package{}, errInvalid
	}
	path := filepath.Join(request.Directory, found.Name)
	if _, err := fetch(ctx, source, maxPackage, path); err != nil {
		return Package{}, err
	}
	if err := found.CheckFile(path); err != nil {
		return Package{}, err
	}
	// The signed sums come last: Staged finds nothing until the package is whole.
	for name, data := range map[string][]byte{SumsName: sums, SignatureName: signature} {
		if err := os.WriteFile(filepath.Join(request.Directory, name), data, 0o600); err != nil {
			return Package{}, errInvalid
		}
	}
	return found, nil
}

func clearDirectory(directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		os.RemoveAll(filepath.Join(directory, entry.Name()))
	}
}

// Discard removes a download, when the release in directory can no longer be installed.
func Discard(directory string) { clearDirectory(directory) }
