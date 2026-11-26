package reality

import (
	"bytes"
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"net"
	"testing"

	utls "github.com/refraction-networking/utls"
	"github.com/xtls/xray-core/transport/internet/tls"
)

func sendsMLKEMKeyShare(t *testing.T, id *utls.ClientHelloID) bool {
	t.Helper()
	c, peer := net.Pipe()
	defer c.Close()
	defer peer.Close()
	u := utls.UClient(c, &utls.Config{ServerName: "example.com"}, *id)
	if err := u.BuildHandshakeState(); err != nil {
		t.Fatalf("%s: %v", id.Str(), err)
	}
	for _, share := range u.HandshakeState.Hello.KeyShares {
		if share.Group == utls.X25519MLKEM768 {
			return true
		}
	}
	return false
}

// REALITY servers since Xray 26.9.8 hand any hello without an X25519MLKEM768
// key share to the cover target (XTLS/Xray-core#7259).
func TestRealityFingerprintsSendMLKEMKeyShare(t *testing.T) {
	families := map[string]string{
		"": "Chrome", "chrome": "Chrome", "firefox": "Firefox", "safari": "Safari",
		"ios": "Safari", "android": "Chrome", "edge": "Chrome", "360": "Chrome", "qq": "Chrome",
		"helloedge_106": "Chrome", "helloios_14": "Safari", "hellochrome_83": "Chrome",
		"hellofirefox_99": "Firefox", "hellosafari_16_0": "Safari",
	}
	for name, family := range families {
		id := realityFingerprint(name)
		if id == nil {
			t.Errorf("%q: no fingerprint", name)
			continue
		}
		if !sendsMLKEMKeyShare(t, id) {
			t.Errorf("%q -> %s sends no X25519MLKEM768 key share", name, id.Str())
		}
		if id.Client != family {
			t.Errorf("%q -> %s, want the %s family", name, id.Str(), family)
		}
	}
	if id := realityFingerprint("random"); id == nil || !sendsMLKEMKeyShare(t, id) {
		t.Errorf("random -> %v sends no X25519MLKEM768 key share", id)
	}
	if realityFingerprint("no-such-fingerprint") != nil {
		t.Error("an unknown fingerprint name must stay unknown")
	}
}

// A randomized hello has the X25519MLKEM768 share with probability one half, and its seed lasts the
// process: half the starts of the app could not reach a REALITY server. REALITY takes the hello that
// "random" chose instead, which always has it.
func TestRealityRandomizedFingerprintsSendMLKEMKeyShare(t *testing.T) {
	random := realityFingerprint("random")
	for _, name := range []string{"randomized", "randomizednoalpn", "hellorandomized", "hellorandomizedalpn", "hellorandomizednoalpn"} {
		id := realityFingerprint(name)
		if id == nil {
			t.Errorf("%q: no fingerprint", name)
			continue
		}
		if id != random {
			t.Errorf("%q -> %s, want the hello of \"random\", %s", name, id.Str(), random.Str())
		}
		if !sendsMLKEMKeyShare(t, id) {
			t.Errorf("%q -> %s sends no X25519MLKEM768 key share", name, id.Str())
		}
	}
}

// builtHello is the hello that UClient sends for the fingerprint name, nil for a name with no fingerprint
func builtHello(t *testing.T, name string) *utls.UConn {
	t.Helper()
	c, peer := net.Pipe()
	t.Cleanup(func() {
		c.Close()
		peer.Close()
	})
	u, err := realityUConn(c, &utls.Config{ServerName: "example.com"}, name)
	if err != nil {
		if realityFingerprint(name) == nil {
			return nil
		}
		t.Fatalf("%q: %v", name, err)
	}
	return u
}

func sendsALPN(u *utls.UConn) bool {
	for _, extension := range u.Extensions {
		switch extension.(type) {
		case *utls.ALPNExtension, *utls.ApplicationSettingsExtension, *utls.ApplicationSettingsExtensionNew:
			return true
		}
	}
	return len(u.HandshakeState.Hello.AlpnProtocols) > 0
}

// The randomized fingerprints that send no ALPN took the hello of "random" with ALPN, a change of behavior
// that the profile did not ask for. They keep that hello and its key shares, without ALPN (and ALPS, which
// carries the settings of what ALPN agreed on); the other randomized ones send ALPN as "random" does.
func TestRealityRandomizedNoALPNFingerprintsSendNoALPN(t *testing.T) {
	for name, noALPN := range map[string]bool{
		"randomized": false, "hellorandomized": false, "hellorandomizedalpn": false,
		"randomizednoalpn": true, "hellorandomizednoalpn": true,
	} {
		u := builtHello(t, name)
		if u == nil {
			t.Errorf("%q: no fingerprint", name)
			continue
		}
		if sends := sendsALPN(u); sends == noALPN {
			t.Errorf("%q sends ALPN: %v, want %v", name, sends, !noALPN)
		}
		mlkem := false
		for _, share := range u.HandshakeState.Hello.KeyShares {
			mlkem = mlkem || share.Group == utls.X25519MLKEM768
		}
		if !mlkem {
			t.Errorf("%q sends no X25519MLKEM768 key share", name)
		}
	}
}

// serverPeerKey is the X25519 public key that a REALITY server takes from the key shares of a hello to make its
// auth key, as github.com/xtls/reality reads them: the X25519 share that follows X25519MLKEM768, else the
// X25519 half of X25519MLKEM768. It is nil for a hello that the server hands to its cover target.
func serverPeerKey(shares []utls.KeyShare) []byte {
	var peer, hybrid []byte
	for _, share := range shares {
		if share.Group == utls.X25519MLKEM768 && len(share.Data) == mlkem.EncapsulationKeySize768+32 {
			if hybrid != nil {
				return nil
			}
			hybrid = share.Data[mlkem.EncapsulationKeySize768:]
			continue
		}
		if share.Group == utls.X25519 && len(share.Data) == 32 {
			peer = share.Data
			break
		}
	}
	if hybrid == nil {
		return nil
	}
	if peer == nil {
		return hybrid
	}
	return peer
}

// REALITY makes its auth key with the private key of an X25519 share, which the server finds by the shares of
// the hello. uTLS keeps the key of the first classical share in Ecdhe, and a hello whose first classical share
// were P-256 would fail with a curve mismatch: realityKey takes the X25519 one. For every fingerprint name,
// the hello is one that a REALITY server takes, and the key is the one the server reads.
func TestRealityKeyIsTheX25519KeyTheServerReadsForEveryFingerprint(t *testing.T) {
	names := []string{""}
	for _, fingerprints := range []map[string]*utls.ClientHelloID{tls.PresetFingerprints, tls.ModernFingerprints, tls.OtherFingerprints} {
		for name := range fingerprints {
			names = append(names, name)
		}
	}
	for _, name := range names {
		u := builtHello(t, name)
		if u == nil {
			continue
		}
		peer := serverPeerKey(u.HandshakeState.Hello.KeyShares)
		if peer == nil {
			t.Errorf("%q -> %s: a REALITY server takes no key share of its hello", name, u.ClientHelloID.Str())
			continue
		}
		key := realityKey(u.HandshakeState.State13.KeyShareKeys)
		if key == nil {
			t.Errorf("%q -> %s: no X25519 key", name, u.ClientHelloID.Str())
			continue
		}
		if !bytes.Equal(key.PublicKey().Bytes(), peer) {
			t.Errorf("%q -> %s: the auth key is made with another key than the server reads", name, u.ClientHelloID.Str())
		}
	}
}

// A hello whose first classical share is P-256 keeps that key in Ecdhe; the X25519 key is one of the others.
func TestRealityKeySkipsAClassicalKeyOfAnotherCurve(t *testing.T) {
	p256, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	x25519, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hybrid, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		keys *utls.KeySharePrivateKeys
		want *ecdh.PrivateKey
	}{
		{"X25519 first", &utls.KeySharePrivateKeys{Ecdhe: x25519, AdditionalEcdhe: []*ecdh.PrivateKey{p256}, MlkemEcdhe: hybrid}, x25519},
		{"P-256 first", &utls.KeySharePrivateKeys{Ecdhe: p256, AdditionalEcdhe: []*ecdh.PrivateKey{x25519}, MlkemEcdhe: hybrid}, x25519},
		{"P-256 alone", &utls.KeySharePrivateKeys{Ecdhe: p256, MlkemEcdhe: hybrid}, hybrid},
		{"hybrid alone", &utls.KeySharePrivateKeys{MlkemEcdhe: hybrid}, hybrid},
		{"no X25519", &utls.KeySharePrivateKeys{Ecdhe: p256}, nil},
	} {
		if got := realityKey(test.keys); got != test.want {
			t.Errorf("%s: another key", test.name)
		}
	}
}
