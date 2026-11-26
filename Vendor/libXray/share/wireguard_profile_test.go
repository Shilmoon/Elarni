package share

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// Regression: amneziawg-go v3.1.20261116 counts a missing Jmax as 0 and refuses Jmin above it. An
// AmneziaWG profile that named only Jmin was refused at import when it had junk packets, and without
// them was stored without Jmax, so the stored profile and its backup disagreed with the connect.
func TestAmneziaProfileWithOnlyJminStoresJmaxEqualToJmin(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, junk := range []string{"Jmin=40\n", "Jc=0\nJmin=40\n", "Jc=4\nJmin=40\n"} {
		input := "[Interface]\nPrivateKey=" + key + "\nAddress=10.20.0.2/32\n" + junk +
			"[Peer]\nPublicKey=" + key + "\nEndpoint=127.0.0.1:51820\nAllowedIPs=0.0.0.0/0\n"
		result, err := ImportClientProfiles(input)
		if err != nil {
			t.Errorf("%q refused: %v", junk, err)
			continue
		}
		config := result.Profiles[0].Config
		if err := validateClientProfile(config); err != nil {
			t.Errorf("%q: stored profile invalid: %v", junk, err)
		}
		var root struct {
			Outbounds []struct {
				Protocol string
				Settings struct{ Amnezia map[string]string }
			}
		}
		if err := json.Unmarshal(config, &root); err != nil || len(root.Outbounds) != 1 {
			t.Fatalf("%q: stored profile %s: %v", junk, config, err)
		}
		outbound := root.Outbounds[0]
		if outbound.Protocol != "amneziawg" || outbound.Settings.Amnezia["jmin"] != "40" || outbound.Settings.Amnezia["jmax"] != "40" {
			t.Errorf("%q: stored %s with %v, want amneziawg with jmin and jmax 40", junk, outbound.Protocol, outbound.Settings.Amnezia)
		}
	}
}
