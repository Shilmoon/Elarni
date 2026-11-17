package share

import (
	"context"
	"errors"
	"time"

	"github.com/xtls/libxray/update"
)

// AppVersion is the running build's version: an update must be newer (Elarni).
func AppVersion() string { return appVersion }

// DownloadUpdate fetches the feed's release for this install into request.Directory and
// verifies it (update.Download). proxy is the running core's loopback SOCKS inbound, or
// empty: Android's own sockets bypass its VPN.
func DownloadUpdate(ctx context.Context, request update.Request, proxy string) (any, error) {
	found, err := update.Download(ctx, request, appVersion, func(ctx context.Context, address string, limit int64, destination string) ([]byte, error) {
		fetch := SubscriptionFetchRequest{URL: address, Proxy: proxy}
		var result any
		var err error
		if destination != "" {
			result, err = FetchResourceToFile(ctx, fetch, limit, 30*time.Minute, destination)
		} else {
			result, err = FetchResource(ctx, fetch, limit, 30*time.Second, true)
		}
		if err != nil {
			return nil, err
		}
		value, _ := result.(map[string]any)
		// A release file on a private address would need the approval a subscription asks for.
		if value == nil || value["warning"] != nil {
			return nil, errors.New("approval_required")
		}
		body, _ := value["body"].([]byte)
		return body, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": found.Version, "name": found.Name}, nil
}
