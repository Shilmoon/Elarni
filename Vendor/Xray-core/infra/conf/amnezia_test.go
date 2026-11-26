package conf

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn/bindtest"
	awg "github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/tuntest"
	"github.com/xtls/xray-core/proxy/wireguard"
)

// The engine allocates junk and I-packets on every handshake and reads transport
// packets at offset 16+S4 into a MaxSegmentSize buffer, trusting these values.
func TestAmneziaSettingsTheEngineCannotRunAreRejected(t *testing.T) {
	s4 := awg.MaxSegmentSize - awg.MessageTransportSize
	for _, settings := range []map[string]string{
		{"jc": "2", "jmin": "40", "jmax": "39"},
		{"jc": "1", "jmax": "4294967295"},
		{"jc": "4294967295"},
		{"jc": "4", "jmin": "1281", "jmax": "1300"},
		{"i1": "<r -1>"},
		{"i2": "<rc 70000>"},
		{"i3": strings.Repeat("<rd 65535>", 2)},
		{"s4": strconv.Itoa(s4 + 1)},
	} {
		if validateAmneziaSettings(settings) == nil {
			t.Errorf("%v accepted", settings)
		}
	}
	for _, settings := range []map[string]string{
		{"jc": "128", "jmin": "1280", "jmax": "1280", "s4": strconv.Itoa(s4), "i1": "<r 65535>"},
		{"jmin": "40"},
		{"jc": "2", "jmin": "40"}, // a missing Jmax is Jmin
	} {
		if err := validateAmneziaSettings(settings); err != nil {
			t.Errorf("%v rejected: %v", settings, err)
		}
	}
}

// AWG 2.0 headers are ranges, and the engine sorts each packet by the range its
// type falls in; an unset header keeps WireGuard's type 1-4. Overlapping ranges
// used to import and then fail only at connect.
func TestOverlappingAmneziaHeaderRangesAreRejected(t *testing.T) {
	for _, settings := range []map[string]string{
		{"h1": "100-200", "h2": "150-300", "h3": "400", "h4": "500"},
		{"h1": "1-5"},
		{"h4": "3"},
	} {
		if validateAmneziaSettings(settings) == nil {
			t.Errorf("%v accepted", settings)
		}
	}
	for _, settings := range []map[string]string{
		{"h1": "100000-100100", "h2": "200000-200100", "h3": "300000-300100", "h4": "400000-400100"},
		{"h1": "5-10"},
		{"h1": "1234567", "h2": "7654321", "h3": "3", "h4": "4"},
	} {
		if err := validateAmneziaSettings(settings); err != nil {
			t.Errorf("%v rejected: %v", settings, err)
		}
	}
}

// The engine refuses Jmin above Jmax at connect; a profile that names both that way stays refused
// at import, with or without junk packets.
func TestAmneziaJminAboveJmaxIsRejectedAtBuild(t *testing.T) {
	for _, amnezia := range []string{`{"jmin": "80", "jmax": "40"}`, `{"jc": "0", "jmin": "80", "jmax": "40"}`, `{"jc": "4", "jmin": "80", "jmax": "40"}`} {
		var config WireGuardConfig
		settings := `{"secretKey": "YAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=", "address": ["10.0.0.2/32"],
			"peers": [{"publicKey": "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=", "endpoint": "192.0.2.1:51820"}], "amnezia": ` + amnezia + `}`
		if err := json.Unmarshal([]byte(settings), &config); err != nil {
			t.Fatal(err)
		}
		config.IsClient, config.IsAmnezia, config.NoKernelTun = true, true, true
		if _, err := config.Build(); err == nil || !strings.Contains(err.Error(), "Jmin exceeds Jmax") {
			t.Errorf("%s: built with error %v, want Jmin exceeds Jmax", amnezia, err)
		}
	}
}

// Regression: amneziawg-go v3.1.20261116 counts a missing Jmax as 0 and refuses Jmin above it. A
// profile that named only Jmin was refused at import when it had junk packets; without them it was
// stored and connected without Jmax, and only the connect patched one in.
func TestAmneziaJminWithoutJmaxBuildsWithJmaxEqualToJmin(t *testing.T) {
	for _, test := range []struct{ amnezia, jmax string }{
		{`{"jmin": "40"}`, "40"},
		{`{"jc": "0", "jmin": "40"}`, "40"},
		{`{"jc": "4", "jmin": "40"}`, "40"},
		{`{"jc": "4", "jmin": "10", "jmax": "20"}`, "20"}, // a named Jmax is kept
		{`{"jc": "4", "jmax": "20"}`, "20"},
	} {
		var config WireGuardConfig
		settings := `{"secretKey": "YAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=", "address": ["10.0.0.2/32"],
			"peers": [{"publicKey": "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=", "endpoint": "192.0.2.1:51820"}], "amnezia": ` + test.amnezia + `}`
		if err := json.Unmarshal([]byte(settings), &config); err != nil {
			t.Fatal(err)
		}
		config.IsClient, config.IsAmnezia, config.NoKernelTun = true, true, true
		built, err := config.Build()
		if err != nil {
			t.Errorf("%s refused: %v", test.amnezia, err)
			continue
		}
		// The handler sends the built settings; an imported profile stores the ones it was built from.
		device := built.(*wireguard.DeviceConfig).AmneziaSettings
		if device["jmax"] != test.jmax || config.Amnezia["jmax"] != test.jmax {
			t.Errorf("%s: built Jmax %q, stored Jmax %q, want %s", test.amnezia, device["jmax"], config.Amnezia["jmax"], test.jmax)
		}
		engine := awg.NewDevice(tuntest.NewChannelTUN().TUN(), bindtest.NewChannelBinds()[0], awg.NewLogger(awg.LogLevelSilent, ""))
		var uapi strings.Builder
		for key, value := range device {
			uapi.WriteString(key + "=" + value + "\n")
		}
		if err := engine.IpcSet(uapi.String()); err != nil {
			t.Errorf("%s: the engine refused the built settings: %v", test.amnezia, err)
		}
		engine.Close()
	}
}

func TestDefaultMTULeavesRoomForAmneziaPadding(t *testing.T) {
	for _, test := range []struct {
		settings map[string]string
		mtu      int32
	}{
		{nil, 1420},
		{map[string]string{"s1": "64", "s2": "64"}, 1420},
		{map[string]string{"s4": "32"}, 1388},
		{map[string]string{"s4": "140"}, 1280},
		{map[string]string{"s4": "600"}, 1280},
	} {
		if mtu := defaultWireGuardMTU(test.settings); mtu != test.mtu {
			t.Errorf("%v: MTU %d, want %d", test.settings, mtu, test.mtu)
		}
	}
	const keys = `"secretKey": "YAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=", "address": ["10.0.0.2/32"],
		"peers": [{"publicKey": "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=", "endpoint": "192.0.2.1:51820"}]`
	for settings, mtu := range map[string]int32{
		`{` + keys + `, "amnezia": {"s4": "32"}}`:              1388,
		`{` + keys + `, "amnezia": {"s4": "32"}, "mtu": 1400}`: 1400,
	} {
		var config WireGuardConfig
		if err := json.Unmarshal([]byte(settings), &config); err != nil {
			t.Fatal(err)
		}
		config.IsClient, config.IsAmnezia, config.NoKernelTun = true, true, true
		built, err := config.Build()
		if err != nil {
			t.Fatal(err)
		}
		if got := built.(*wireguard.DeviceConfig).Mtu; got != mtu {
			t.Errorf("%s: MTU %d, want %d", settings, got, mtu)
		}
	}
}
