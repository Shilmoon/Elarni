package share

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/kcp"
)

// Xray refuses a VLESS or Trojan server without TLS, REALITY or VLESS encryption unless its
// address is private. The import said only "unsupported", which sent the user to look for
// another format; it names the reason.
func TestImportNamesAnUnencryptedPublicServer(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	for _, link := range []string{
		"vless://" + id + "@8.8.8.8:443?encryption=none&type=tcp&security=none#Plain",
		"vless://" + id + "@dns.google:443?encryption=none#Plain",
		"trojan://pw@8.8.8.8:443?security=none#Plain",
	} {
		if _, err := ImportClientProfiles(link); !errors.Is(err, ErrUnencryptedServer) || err.Error() != "unencrypted_server" {
			t.Errorf("%s: %v", link, err)
		}
	}
	// The same server as an Xray profile.
	profile := `{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"8.8.8.8","port":443,"users":[{"id":"` + id + `","encryption":"none"}]}]}}]}`
	if _, err := ImportClientProfiles(profile); !errors.Is(err, ErrUnencryptedServer) {
		t.Errorf("Xray profile: %v", err)
	}
	// The same server in a Clash file, which named only "invalid Clash client settings".
	if _, err := ImportClientProfiles("proxies:\n  - {name: Plain, type: vless, server: 8.8.8.8, port: 443, uuid: " + id + "}\n"); !errors.Is(err, ErrUnencryptedServer) {
		t.Errorf("Clash profile: %v", err)
	}
	// A private address, and a public one with TLS, import as before.
	for _, link := range []string{
		"vless://" + id + "@10.0.0.1:443?encryption=none&security=none#Lan",
		"vless://" + id + "@8.8.8.8:443?encryption=none&security=tls&sni=plain.example#Tls",
	} {
		if result, err := ImportClientProfiles(link); err != nil || len(result.Profiles) != 1 {
			t.Errorf("%s: %v", link, err)
		}
	}
	// The reason is the import's only when it is every member's: other broken lines are not servers without encryption.
	plain := "vless://" + id + "@8.8.8.8:443?encryption=none&type=tcp&security=none#Plain"
	if _, err := ImportClientProfiles(plain + "\n" + strings.Replace(plain, "8.8.8.8", "8.8.4.4", 1)); !errors.Is(err, ErrUnencryptedServer) {
		t.Errorf("two unencrypted servers: %v", err)
	}
	if _, err := ImportClientProfiles(plain + "\nvless://broken\nnot a link"); err == nil || errors.Is(err, ErrUnencryptedServer) {
		t.Errorf("an unencrypted server among broken lines: %v", err)
	}
	// After one, another member's error is the file's: a Clash file's names Clash, which macOS matches on.
	if _, err := ImportClientProfiles("proxies:\n  - {name: Plain, type: vless, server: 8.8.8.8, port: 443, uuid: " + id + "}\n  - {name: Bad, type: snell, server: 127.0.0.1, port: 1080}\n"); err == nil || errors.Is(err, ErrUnencryptedServer) || err.Error() != "unsupported Clash proxy type snell" {
		t.Errorf("an unencrypted server before a refused Clash proxy: %v", err)
	}
	// Among servers that import it is skipped like any other.
	result, err := ImportClientProfiles("trojan://pw@127.0.0.1:443#Kept\ntrojan://pw@8.8.8.8:443?security=none#Plain")
	if err != nil || len(result.Profiles) != 1 || result.SkippedCount != 1 {
		t.Fatalf("mixed feed: %+v %v", result, err)
	}
}

// One unsupported line used to cost the user every server of the subscription: a member that
// cannot be imported is now skipped and reported, and the rest import.
func TestImportSkipsMembersItCannotImport(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	feed := strings.Join([]string{
		"vless://" + id + "@127.0.0.1:443?encryption=none#Kept",
		"vless://broken#Broken%20one",
		"unknown://broken",
		"VLESS://" + id + "@127.0.0.1:0?encryption=none#" + strings.Repeat("n", 129),
		"NOT A LINK",
		"wireguard://short@127.0.0.1:51820#wg",
		"x_y://broken#a%0Ab",
		"trojan://pw@127.0.0.1:443#Second",
	}, "\n")
	for _, input := range []string{feed, base64.StdEncoding.EncodeToString([]byte(feed))} {
		result, err := ImportClientProfiles(input)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, profile := range result.Profiles {
			names = append(names, profile.Name)
		}
		if !slices.Equal(names, []string{"Kept", "Second"}) {
			t.Fatalf("imported %v", names)
		}
		want := []SkippedProfile{{Name: "Broken one", Kind: "vless"}, {Kind: "unknown"}, {Kind: "vless"}, {Kind: "link"},
			{Name: "wg", Kind: "wireguard"}, {Kind: "link"}}
		if !reflect.DeepEqual(result.Skipped, want) || result.SkippedCount != len(want) {
			t.Fatalf("skipped %+v (%d), want %+v", result.Skipped, result.SkippedCount, want)
		}
		encoded, err := json.Marshal(result)
		if err != nil || !bytes.Contains(encoded, []byte(`"skipped":[{"name":"Broken one","kind":"vless"},{"kind":"unknown"}`)) ||
			!bytes.Contains(encoded, []byte(`"skippedCount":6`)) {
			t.Fatalf("result JSON %s (%v)", encoded, err)
		}
	}
	// A feed with nothing to skip reports nothing.
	result, err := ImportClientProfiles("trojan://pw@127.0.0.1:443#One")
	if encoded, _ := json.Marshal(result); err != nil || bytes.Contains(encoded, []byte("skipped")) {
		t.Fatalf("clean feed reported skips: %s (%v)", encoded, err)
	}
}

// The report lists the first 32 skipped members and counts them all.
func TestImportReportsAtMost32SkippedMembers(t *testing.T) {
	lines := []string{"trojan://pw@127.0.0.1:443#Kept"}
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("vless://broken#bad%d", i))
	}
	result, err := ImportClientProfiles(strings.Join(lines, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Profiles) != 1 || len(result.Skipped) != 32 || result.SkippedCount != 40 || result.Skipped[31].Name != "bad31" {
		t.Fatalf("%d profiles, %d listed, %d counted", len(result.Profiles), len(result.Skipped), result.SkippedCount)
	}
}

// With nothing left to import, the first skipped member's error is the import's error, so a single
// bad link keeps the error it always had.
func TestImportWithNothingLeftKeepsTheFirstError(t *testing.T) {
	for input, want := range map[string]string{
		"vless://broken":                         "invalid or unsupported client link on line 1",
		"vless://%zz":                            "invalid client link on line 1",
		"# comment\nvless://broken\nunknown://x": "invalid or unsupported client link on line 2",
		`[null]`:                                 "profile 1 is not an object",
		`[null, {"outbounds":[{"protocol":"vless","settings":{"vnext":[null]}}]}]`: "profile 1 is not an object",
	} {
		if result, err := ImportClientProfiles(input); err == nil || err.Error() != want {
			t.Errorf("%q: %v (%+v), want %q", input, err, result, want)
		}
	}
	if _, err := ImportClientProfiles("wireguard://short@127.0.0.1:51820"); err == nil || !strings.HasPrefix(err.Error(), "invalid WireGuard or AmneziaWG link on line 1: ") {
		t.Errorf("WireGuard link: %v", err)
	}
	for _, input := range []string{"", "# only a comment", "[]"} {
		if _, err := ImportClientProfiles(input); err == nil {
			t.Errorf("%q imported", input)
		}
	}
}

func TestImportSkipsXrayArrayMembersItCannotImport(t *testing.T) {
	const valid = `{"remarks":"Kept","outbounds":[{"protocol":"vless","settings":{"address":"127.0.0.1","port":443,"id":"00000000-0000-4000-8000-000000000001","encryption":"none"}}]}`
	invalid := `{"remarks":"Broken","outbounds":[{"protocol":"vless","settings":{"vnext":[null]}}]}`
	unnamed := `{"outbounds":[{"protocol":"socks","settings":{"address":"127.0.0.1"}}]}`
	result, err := ImportClientProfiles("[" + strings.Join([]string{valid, "null", invalid, "5", unnamed, `{"name":"Named","protocol":"vless"}`}, ",") + "]")
	if err != nil {
		t.Fatal(err)
	}
	want := []SkippedProfile{{Kind: "xray"}, {Name: "Broken", Kind: "xray"}, {Kind: "xray"}, {Kind: "xray"}, {Name: "Named", Kind: "xray"}}
	if len(result.Profiles) != 1 || result.Profiles[0].Name != "Kept" || !reflect.DeepEqual(result.Skipped, want) || result.SkippedCount != 5 {
		t.Fatalf("profiles %+v, skipped %+v (%d)", result.Profiles, result.Skipped, result.SkippedCount)
	}
	// One object is one profile: it still fails as a whole.
	if _, err := ImportClientProfiles(invalid); err == nil {
		t.Fatal("invalid single profile imported")
	}
}

func TestClashImportSkipsProxiesItCannotConvert(t *testing.T) {
	const input = `proxies:
  - {name: one, type: socks5, server: 127.0.0.1, port: 1080}
  - {name: Bad, type: snell, server: 127.0.0.1, port: 1080}
  - {name: spaced, type: ss, server: edge example, port: 8388, cipher: chacha20-ietf-poly1305, password: fixture}
  - {name: weird, type: "Weird Type", server: 127.0.0.1, port: 1080}
  - {name: two, type: http, server: 127.0.0.1, port: 8080}
proxy-groups:
  - {name: PROXY, type: select, proxies: [Bad, one, OnlyBad, two], selected: Bad}
  - {name: OnlyBad, type: select, proxies: [Bad, spaced]}
  - {name: Outer, type: select, proxies: [OnlyBad, PROXY]}
rules:
  - DOMAIN-SUFFIX,example.org,DIRECT
  - MATCH,Outer
`
	result, err := ImportClientProfiles(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []SkippedProfile{{Name: "Bad", Kind: "snell"}, {Name: "spaced", Kind: "ss"}, {Name: "weird", Kind: "clash"}}
	if len(result.Profiles) != 1 || !reflect.DeepEqual(result.Skipped, want) || result.SkippedCount != 3 {
		t.Fatalf("profiles %d, skipped %+v (%d)", len(result.Profiles), result.Skipped, result.SkippedCount)
	}
	var root struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
		Groups []clashGroup `json:"elarniGroups"`
	}
	if err := json.Unmarshal(result.Profiles[0].Config, &root); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, outbound := range root.Outbounds {
		tags = append(tags, outbound.Tag)
	}
	if !slices.Equal(tags, []string{"one", "two", "PROXY", "Outer", "DIRECT", "REJECT", "REJECT-DROP"}) {
		t.Fatalf("outbounds %v", tags)
	}
	// The dropped proxy leaves its groups, a group it leaves empty goes too, and the selection moves on.
	wantGroups := []clashGroup{{Name: "PROXY", Type: "select", Options: []string{"one", "two"}, Selected: "one"},
		{Name: "Outer", Type: "select", Options: []string{"PROXY"}, Selected: "PROXY"}}
	if !reflect.DeepEqual(root.Groups, wantGroups) {
		t.Fatalf("groups %+v", root.Groups)
	}
	// A rule naming what was dropped blocks what it matched, in its place among the rules around it, so
	// that a later rule cannot send that traffic directly. It counts as the IP rule it is, with its
	// no-resolve, in whether the profile resolves domains for IP rules.
	for _, test := range []struct{ target, option, strategy string }{{"Bad", "", "IPOnDemand"}, {"OnlyBad", ",no-resolve", "AsIs"}} {
		target := test.target
		rules := "IP-CIDR,10.0.0.0/8," + target + test.option + "\n  - IP-CIDR,192.168.0.0/16,DIRECT,no-resolve\n  - MATCH,Outer"
		result, err := ImportClientProfiles(strings.Replace(input, "MATCH,Outer", rules, 1))
		if err != nil {
			t.Errorf("rule to %s: %v", target, err)
			continue
		}
		if !reflect.DeepEqual(result.Skipped, want) {
			t.Errorf("rule to %s: skipped %+v", target, result.Skipped)
		}
		var routed struct {
			Routing struct {
				DomainStrategy string `json:"domainStrategy"`
				Rules          []struct {
					Tag     string   `json:"outboundTag"`
					Domain  []string `json:"domain"`
					IP      []string `json:"ip"`
					Network string   `json:"network"`
				} `json:"rules"`
			} `json:"routing"`
		}
		if err := json.Unmarshal(result.Profiles[0].Config, &routed); err != nil {
			t.Fatal(err)
		}
		rest := routed.Routing.Rules
		if len(rest) != 4 || rest[0].Tag != "DIRECT" || !slices.Equal(rest[0].Domain, []string{"domain:example.org"}) ||
			rest[1].Tag != "REJECT" || !slices.Equal(rest[1].IP, []string{"10.0.0.0/8"}) ||
			rest[2].Tag != "DIRECT" || !slices.Equal(rest[2].IP, []string{"192.168.0.0/16"}) ||
			rest[3].Tag != "Outer" || rest[3].Network != "tcp,udp" || routed.Routing.DomainStrategy != test.strategy {
			t.Errorf("rule to %s: routing %+v", target, routed.Routing)
		}
		// The rule for everything else cannot be blocked without a word: the file says why it is refused.
		if _, err := ImportClientProfiles(strings.Replace(input, "MATCH,Outer", "MATCH,"+target, 1)); err == nil || err.Error() != "Clash MATCH rule refers to a skipped proxy" {
			t.Errorf("MATCH to %s: %v", target, err)
		}
	}
	// A rule naming what the file never defined still fails it.
	if _, err := ImportClientProfiles(strings.Replace(input, "MATCH,Outer", "MATCH,Nowhere", 1)); err == nil || err.Error() != "Clash rule has a missing target" {
		t.Errorf("rule to an undefined target: %v", err)
	}
	// With no proxy left, the first one's error is the file's.
	if _, err := ImportClientProfiles("proxies:\n  - {name: Bad, type: snell, server: 127.0.0.1, port: 1080}\n  - {name: weird, type: x, server: 127.0.0.1, port: 1080}\n"); err == nil || err.Error() != "unsupported Clash proxy type snell" {
		t.Errorf("all proxies refused: %v", err)
	}
	// Structural errors still fail the file, also in a rule to a dropped proxy.
	for _, broken := range []string{input + "unknown-top-level: 1\n", strings.Replace(input, "type: select, proxies: [Bad, spaced]", "type: url-test, proxies: [Bad, spaced]", 1), input + "  - [not, a, rule\n",
		strings.Replace(input, "MATCH,Outer", "PROCESS-NAME,curl,Bad\n  - MATCH,Outer", 1)} {
		if _, err := ImportClientProfiles(broken); err == nil {
			t.Errorf("structurally broken file imported:\n%s", broken)
		}
	}
}

// Remnawave's default Mihomo template sets mode: global and writes packet-encoding: xudp on VLESS.
func TestClashGlobalModeAndPacketEncoding(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	input := `mixed-port: 7890
mode: global
proxies:
  - {name: a, type: vless, server: 127.0.0.1, port: 443, uuid: ` + id + `, packet-encoding: xudp}
  - {name: b, type: vless, server: 127.0.0.1, port: 444, uuid: ` + id + `, packet-encoding: ''}
proxy-groups:
  - {name: '→ Remnawave', type: select, proxies: [a, b]}
rules:
  - RULE-SET,unsupported,DIRECT
  - MATCH,→ Remnawave
`
	result, err := ImportClientProfiles(input)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(result.Profiles[0].Config, &root); err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 0 || len(root["outbounds"].([]any)) != 7 {
		t.Fatalf("global mode: skipped %+v, config %s", result.Skipped, result.Profiles[0].Config)
	}
	// The file's rules are left out. Everything goes to the GLOBAL group, which starts on the file's
	// first group and offers every group, every proxy and DIRECT.
	rules, _ := json.Marshal(root["routing"].(map[string]any)["rules"])
	groups, _ := json.Marshal(root["elarniGroups"])
	if string(rules) != `[{"network":"tcp,udp","outboundTag":"GLOBAL","type":"field"}]` ||
		string(groups) != `[{"name":"GLOBAL","options":["→ Remnawave","a","b","DIRECT"],"selected":"→ Remnawave","type":"select"},`+
			`{"name":"→ Remnawave","options":["a","b"],"selected":"a","type":"select"}]` {
		t.Fatalf("global mode: rules %s, groups %s", rules, groups)
	}
	// The file's own GLOBAL group is the one used, and a proxy by that name keeps it.
	for _, test := range []struct {
		replace      *strings.Replacer
		rule, groups string
	}{
		{strings.NewReplacer("name: '→ Remnawave'", "name: GLOBAL"), "GLOBAL",
			`[{"name":"GLOBAL","options":["a","b"],"selected":"a","type":"select"}]`},
		{strings.NewReplacer("{name: a,", "{name: GLOBAL,", "proxies: [a, b]", "proxies: [GLOBAL, b]"), "GLOBAL_",
			`[{"name":"GLOBAL_","options":["→ Remnawave","GLOBAL","b","DIRECT"],"selected":"→ Remnawave","type":"select"},` +
				`{"name":"→ Remnawave","options":["GLOBAL","b"],"selected":"GLOBAL","type":"select"}]`},
	} {
		result, err := ImportClientProfiles(test.replace.Replace(input))
		if err != nil {
			t.Fatalf("%s: %v", test.rule, err)
		}
		var root map[string]any
		if err := json.Unmarshal(result.Profiles[0].Config, &root); err != nil {
			t.Fatal(err)
		}
		rule := root["routing"].(map[string]any)["rules"].([]any)[0].(map[string]any)["outboundTag"]
		if groups, _ := json.Marshal(root["elarniGroups"]); string(groups) != test.groups || rule != test.rule {
			t.Errorf("rule to %v, want %s; groups %s", rule, test.rule, groups)
		}
	}
	if _, err := ImportClientProfiles(strings.Replace(input, "mode: global", "mode: direct", 1)); err == nil {
		t.Fatal("direct mode imported")
	}
	other := strings.Replace(input, "packet-encoding: ''", "packet-encoding: packetaddr", 1)
	result, err = ImportClientProfiles(other)
	if err != nil || len(result.Skipped) != 1 || result.Skipped[0] != (SkippedProfile{Name: "b", Kind: "vless"}) {
		t.Fatalf("another packet encoding: %+v, %v", result, err)
	}
	if _, err := ImportClientProfiles("proxies:\n  - {name: b, type: vless, server: 127.0.0.1, port: 444, uuid: " + id + ", packet-encoding: packetaddr}\n"); err == nil {
		t.Fatal("packetaddr imported")
	}
}

// amneziaKey writes a key as AmneziaVPN exports one: vpn:// and unpadded base64url of Qt's
// qCompress, a 4-byte big-endian length before a zlib stream.
func amneziaKey(t *testing.T, document any) string {
	t.Helper()
	text, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	_ = binary.Write(&compressed, binary.BigEndian, uint32(len(text)))
	writer, err := zlib.NewWriterLevel(&compressed, 8)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write(text)
	_ = writer.Close()
	return "vpn://" + base64.RawURLEncoding.EncodeToString(compressed.Bytes())
}

func amneziaContainer(t *testing.T, container, protocol, config string) map[string]any {
	t.Helper()
	last, err := json.Marshal(map[string]any{"config": config, "hostName": "127.0.0.1", "port": 51820, "mtu": "1376"})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"container": container, protocol: map[string]any{"last_config": string(last), "port": "51820", "transport_proto": "udp"}}
}

func TestAmneziaVPNKeysImport(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	wireguard := "[Interface]\nAddress = 10.8.1.2/32\nDNS = $PRIMARY_DNS, $SECONDARY_DNS\nPrivateKey = " + key + "\n"
	awg := wireguard + "Jc = 4\nJmin = 40\nJmax = 80\nS1 = 16\nS2 = 17\nH1 = 100-110\nH2 = 200-210\nH3 = 300-310\nH4 = 400-410\n"
	peer := "\n[Peer]\nPublicKey = " + key + "\nPresharedKey = " + key + "\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = 127.0.0.1:51820\nPersistentKeepalive = 25\n"
	document := func(defaultContainer string, containers ...map[string]any) map[string]any {
		return map[string]any{"containers": containers, "defaultContainer": defaultContainer, "description": "Fixture server",
			"dns1": "1.1.1.1", "dns2": "1.0.0.1", "hostName": "127.0.0.1"}
	}
	openvpn := map[string]any{"container": "amnezia-openvpn", "openvpn": map[string]any{"last_config": `{"config":"client\ndev tun\n"}`}}
	for name, test := range map[string]struct {
		link, protocol, name string
	}{
		"AmneziaWG":          {amneziaKey(t, document("amnezia-awg", amneziaContainer(t, "amnezia-awg", "awg", awg+peer))), "amneziawg", "Fixture server"},
		"AmneziaWG 2":        {amneziaKey(t, document("amnezia-awg2", amneziaContainer(t, "amnezia-awg2", "awg", awg+peer))), "amneziawg", "Fixture server"},
		"AWG without fields": {amneziaKey(t, document("amnezia-awg", amneziaContainer(t, "amnezia-awg", "awg", wireguard+peer))) + "#Named", "amneziawg", "Named"},
		"WireGuard":          {amneziaKey(t, document("amnezia-wireguard", openvpn, amneziaContainer(t, "amnezia-wireguard", "wireguard", wireguard+peer))), "wireguard", "Fixture server"},
		"no default":         {amneziaKey(t, document("", openvpn, amneziaContainer(t, "amnezia-awg", "awg", awg+peer))), "amneziawg", "Fixture server"},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ImportClientProfiles(test.link)
			if err != nil {
				t.Fatal(err)
			}
			var root struct {
				Outbounds []struct {
					Protocol string `json:"protocol"`
					Settings struct {
						Address []string `json:"address"`
					} `json:"settings"`
				} `json:"outbounds"`
				DNS struct {
					Servers []string `json:"servers"`
				} `json:"dns"`
			}
			if err := json.Unmarshal(result.Profiles[0].Config, &root); err != nil {
				t.Fatal(err)
			}
			if root.Outbounds[0].Protocol != test.protocol || !slices.Equal(root.DNS.Servers, []string{"1.1.1.1", "1.0.0.1"}) ||
				!slices.Equal(root.Outbounds[0].Settings.Address, []string{"10.8.1.2/32"}) || result.Profiles[0].Name != test.name {
				t.Fatalf("%s: %s", result.Profiles[0].Name, result.Profiles[0].Config)
			}
		})
	}
	for name, test := range map[string]struct {
		link, error string
	}{
		"OpenVPN default":  {amneziaKey(t, document("amnezia-openvpn", openvpn, amneziaContainer(t, "amnezia-awg", "awg", awg+peer))), "WireGuard"},
		"Xray only":        {amneziaKey(t, document("amnezia-xray", map[string]any{"container": "amnezia-xray", "xray": map[string]any{"last_config": "{}"}})), "WireGuard"},
		"no client config": {amneziaKey(t, document("amnezia-awg", map[string]any{"container": "amnezia-awg", "awg": map[string]any{"port": "51820"}})), "configuration"},
		"not JSON":         {"vpn://" + base64.RawURLEncoding.EncodeToString(append([]byte{0, 0, 0, 2}, zlibBytes(t, []byte("{]"))...)), "AmneziaVPN"},
		"over 1 MiB":       {"vpn://" + base64.RawURLEncoding.EncodeToString(append([]byte{0, 16, 0, 1}, zlibBytes(t, bytes.Repeat([]byte(" "), 1<<20+1))...)), "AmneziaVPN"},
	} {
		if _, err := ImportClientProfiles(test.link); err == nil || !strings.Contains(err.Error(), test.error) {
			t.Errorf("%s: %v, want an error naming %q", name, err, test.error)
		}
	}
}

func zlibBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write(data)
	_ = writer.Close()
	return compressed.Bytes()
}

// Remnawave's subscription page writes happ://add/{{SUBSCRIPTION_LINK}}.
func TestHappAddLinksUnwrapLikeIncy(t *testing.T) {
	const target = "https://sub.example.com/feed/token?key=a%2Bb"
	for input, want := range map[string]ClientInput{
		"happ://add/" + target:                  {Text: target},
		"happ://add/" + target + "#Happ%20name": {Text: target, Name: "Happ name"},
		"happ://add/" + url.PathEscape(target):  {Text: target},
		"happ://routing/onadd/fixture":          {Text: "happ://routing/onadd/fixture"},
		"happ://crypt3/fixture":                 {Text: "happ://crypt3/fixture"},
	} {
		got, err := NormalizeClientInput(input)
		if err != nil || got != want {
			t.Errorf("%s: %+v, %v; want %+v", input, got, err, want)
		}
	}
}

// Marzban writes "allowInsecure": 1 in the VMess JSON; v2rayN writes "insecure": "1".
func TestVMessQRAllowInsecure(t *testing.T) {
	for fields, want := range map[string]bool{
		`"allowInsecure":1`: true, `"allowInsecure":"1"`: true, `"allowInsecure":true`: true, `"allowInsecure":"true"`: true,
		`"allowInsecure":0`: false, `"allowInsecure":"0"`: false, `"allowInsecure":false`: false, `"allowInsecure":null`: false,
		`"insecure":"1","allowInsecure":0`: true, `"insecure":"0","allowInsecure":1`: true, `"insecure":"1"`: true,
	} {
		qr := `{"v":"2","ps":"m","add":"127.0.0.1","port":"443","id":"00000000-0000-4000-8000-000000000001","net":"tcp","tls":"tls","sni":"example.com",` + fields + `}`
		result, err := ImportClientProfiles("vmess://" + base64.StdEncoding.EncodeToString([]byte(qr)))
		if err != nil {
			t.Fatalf("%s: %v", fields, err)
		}
		if got := strings.Contains(string(result.Profiles[0].Config), `"allowInsecure":true`); got != want {
			t.Errorf("%s: allowInsecure %v, want %v", fields, got, want)
		}
	}
}

// wg-quick adds up repeated Address and DNS lines and wg repeated AllowedIPs; old wgcf writes one
// Address line per family. ListenPort means nothing to a client.
func TestWireGuardRepeatedListsAccumulate(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	config := "[Interface]\nPrivateKey = " + key + "\nAddress = 172.16.0.2/32\nAddress = fd01::2/128\nDNS = 1.1.1.1\nDNS = 1.0.0.1, 2606:4700:4700::1111\nListenPort = 51820\n" +
		"[Peer]\nPublicKey = " + key + "\nAllowedIPs = 0.0.0.0/0\nAllowedIPs = ::/0\nEndpoint = 127.0.0.1:2408\n"
	result, err := ImportClientProfiles(config)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Outbounds []struct {
			Settings struct {
				Address []string `json:"address"`
				Peers   []struct {
					AllowedIPs []string `json:"allowedIPs"`
				} `json:"peers"`
			} `json:"settings"`
		} `json:"outbounds"`
		DNS struct {
			Servers []string `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(result.Profiles[0].Config, &root); err != nil {
		t.Fatal(err)
	}
	settings := root.Outbounds[0].Settings
	if !slices.Equal(settings.Address, []string{"172.16.0.2/32", "fd01::2/128"}) || !slices.Equal(root.DNS.Servers, []string{"1.1.1.1", "1.0.0.1", "2606:4700:4700::1111"}) ||
		len(settings.Peers) != 1 || !slices.Equal(settings.Peers[0].AllowedIPs, []string{"0.0.0.0/0", "::/0"}) {
		t.Fatalf("lists lost: %s", result.Profiles[0].Config)
	}
	for _, duplicate := range []string{"PrivateKey = " + key, "MTU = 1280\nMTU = 1280", "ListenPort = 1", "Table = off"} {
		input := strings.Replace(config, "ListenPort = 51820", "ListenPort = 51820\n"+duplicate, 1)
		if _, err := ImportClientProfiles(input); err == nil {
			t.Errorf("accepted %q", duplicate)
		}
	}
	if _, err := ImportClientProfiles(strings.Replace(config, "Endpoint", "PublicKey = "+key+"\nEndpoint", 1)); err == nil {
		t.Error("accepted a repeated peer PublicKey")
	}
}

// Hiddify writes "/" before a WireGuard link's query and repeats the private key there, beside its
// noise packets as ifp; 3x-ui and v2rayN carry the server's FinalMask as fm (probe N10-04).
func TestWireGuardLinkVariantsFromPanels(t *testing.T) {
	const key = "xZirRKTxA325fjhUvhxuztg6m%2F7tMLYe80RV0DcxqsU%3D"
	const other = "%2BcdBAlUKFi4b5U9jac58v0fK1kNFW1w%2FTILhJsV%2B1V4%3D"
	const peer = "publickey=TgNJhyP2BguxwtnHSwW0jLnqtqMMXQFgDPolsdOAjv8%3D&address=10.0.0.2/32&mtu=1380&reserved=0,0,0"
	fm := func(mask string) string { return "&fm=" + url.QueryEscape(mask) }
	for _, test := range []struct {
		link, name string
		masks      int
		err        string
	}{
		{link: "wireguard://" + key + "@example.com:51820/?" + peer + "#wg-slash", name: "wg-slash"},
		{link: "wireguard://" + key + "@example.com:51820/?" + peer + "&presharedkey=obtmjnnvEuzlJqNw%2F%2By7Hu73jATWN9Qu0u%2Fj1V5c9OE%3D&privatekey=" + key + "&ifp=0-0#wg-hiddify-keys", name: "wg-hiddify-keys"},
		{link: "wireguard://@example.com:51820?" + peer + "&privatekey=" + key + "&ifp=0", name: "Wireguard"},
		{link: "wireguard://" + key + "@example.com:51820?" + peer + fm(`{"udp":[{"type":"mkcp-legacy","settings":{"header":"wireguard"}}]}`) + "#wg-fm", name: "wg-fm", masks: 1},
		{link: "wireguard://" + key + "@example.com:51820?" + peer + fm(`{}`) + "#DE-1?serverDescription=U3ViIHRpdGxl", name: "DE-1"},
		{link: "wireguard://" + key + "@example.com:51820?" + peer + "&privatekey=" + other, err: "conflicting WireGuard private keys"},
		{link: "wireguard://" + key + "@example.com:51820?" + peer + "&ifp=5-10", err: "WireGuard noise packets (ifp) are not supported"},
		{link: "wireguard://" + key + "@example.com:51820?" + peer + fm(`{"tcp":[{"type":"fragment"}]}`), err: "WireGuard FinalMask takes only UDP masks"},
		{link: "wireguard://" + key + "@example.com:51820?" + peer + fm(`{"quicParams":{"congestion":"bbr"}}`), err: "WireGuard FinalMask takes only UDP masks"},
		{link: "wireguard://" + key + "@example.com:51820?" + peer + fm(`{"udp":[{"type":"header-wireguard"}]}`), err: "unsupported WireGuard FinalMask"},
		{link: "wireguard://" + key + "@example.com:51820?" + peer + fm(`[]`), err: "invalid WireGuard FinalMask"},
		{link: "wireguard://" + key + "@example.com:51820/x?" + peer, err: "invalid WireGuard credentials or path"},
		{link: "wireguard://@example.com:51820?" + peer, err: "invalid WireGuard credentials or path"},
	} {
		profile, err := importWireGuardLink(test.link)
		if test.err != "" {
			if err == nil || !strings.HasPrefix(err.Error(), test.err) {
				t.Errorf("%s: got %v, want %q", test.link, err, test.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", test.link, err)
			continue
		}
		imported, err := ImportClientProfiles(test.link)
		if err != nil || len(imported.Profiles) != 1 || imported.Profiles[0].Name != test.name {
			t.Errorf("%s: imported %+v, %v", test.link, imported, err)
			continue
		}
		if !bytes.Equal(imported.Profiles[0].Config, profile.Config) {
			t.Errorf("%s: the import and the link differ", test.link)
		}
		var root struct {
			Outbounds []struct {
				Settings struct {
					SecretKey string `json:"secretKey"`
					MTU       int    `json:"mtu"`
				} `json:"settings"`
			} `json:"outbounds"`
		}
		if json.Unmarshal(profile.Config, &root) != nil || root.Outbounds[0].Settings.SecretKey != "xZirRKTxA325fjhUvhxuztg6m/7tMLYe80RV0DcxqsU=" || root.Outbounds[0].Settings.MTU != 1380 {
			t.Errorf("%s: imported as %s", test.link, profile.Config)
		}
		if masks := builtOutbound(t, profile.Config).stream.(*internet.StreamConfig).Udpmasks; len(masks) != test.masks {
			t.Errorf("%s: %d masks, want %d: %s", test.link, len(masks), test.masks, profile.Config)
		}
	}
}

// 3x-ui writes an AmneziaWG server's IPv6 Endpoint without brackets (probe N10-05). The port follows
// the last colon only when what is before it is an IPv6 address.
func TestWireGuardUnbracketedIPv6Endpoint(t *testing.T) {
	const config = "[Interface]\nPrivateKey = xZirRKTxA325fjhUvhxuztg6m/7tMLYe80RV0DcxqsU=\nAddress = 10.0.0.2/32\n" +
		"[Peer]\nPublicKey = TgNJhyP2BguxwtnHSwW0jLnqtqMMXQFgDPolsdOAjv8=\nAllowedIPs = 0.0.0.0/0\nEndpoint = "
	for endpoint, want := range map[string]string{
		"2001:db8::1:51820":   "[2001:db8::1]:51820",
		"[2001:db8::1]:51820": "[2001:db8::1]:51820",
		"::ffff:192.0.2.1:80": "[::ffff:192.0.2.1]:80",
		"192.0.2.1:51820":     "192.0.2.1:51820",
		"example.com:51820":   "example.com:51820",
		// Each of these is no endpoint, which the core refuses.
		"2001:db8::1":          "",
		"2001:db8::1:0":        "",
		"2001:db8::1:65536":    "",
		"fe80::1%eth0:51820":   "",
		"192.0.2.1:2001:51820": "",
	} {
		result, err := ImportClientProfiles(config + endpoint)
		if want == "" {
			if err == nil {
				t.Errorf("%s imported as %s", endpoint, result.Profiles[0].Config)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", endpoint, err)
			continue
		}
		var root struct {
			Outbounds []struct {
				Settings struct {
					Peers []struct {
						Endpoint string `json:"endpoint"`
					} `json:"peers"`
				} `json:"settings"`
			} `json:"outbounds"`
		}
		if json.Unmarshal(result.Profiles[0].Config, &root) != nil || root.Outbounds[0].Settings.Peers[0].Endpoint != want {
			t.Errorf("%s imported as %s, want %s", endpoint, result.Profiles[0].Config, want)
		}
	}
}

// 3x-ui v3.11.0 appends a server's description to its name as "?serverDescription=" and Base64, which
// Happ shows apart (probe N10-09). Other text after a "?" is the name's own.
func TestLinkNamesLeaveOutTheServerDescription(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	const vless = "vless://" + uuid + "@example.com:443?type=ws&security=tls&sni=example.com&path=%2F#"
	description := "?serverDescription=" + base64.StdEncoding.EncodeToString([]byte("Sub title 🇩🇪"))
	vmess := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"VMess`+description+
		`","add":"example.com","port":"443","id":"`+uuid+`","net":"tcp","tls":"tls","sni":"example.com"}`))
	legacy := "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:pw@192.0.2.1:8388")) + "#SS" + description
	for link, want := range map[string]string{
		vless + "DE-1?serverDescription=U3ViIHRpdGxl":       "DE-1",
		vless + "DE%201" + description:                      "DE 1",
		vless + "DE%201%3FserverDescription%3DU3ViIHRpdGxl": "DE 1",
		vless + "What?": "What?",
		vless + "Q?serverDescription=not%20base64":                             "Q?serverDescription=not base64",
		vless + "Q?serverDescription=":                                         "Q?serverDescription=",
		"trojan://secret@example.com:443?sni=example.com#Trojan" + description: "Trojan",
		vmess:  "VMess",
		legacy: "SS",
	} {
		imported, err := ImportClientProfiles(link)
		if err != nil || len(imported.Profiles) != 1 || imported.Profiles[0].Name != want {
			t.Errorf("%s: imported %+v, %v; want the name %q", link, imported, err, want)
		}
	}
	imported, err := ImportClientProfiles(vless + "Good\nvless://broken#Bad" + description)
	if err != nil || imported.SkippedCount != 1 || imported.Skipped[0].Name != "Bad" {
		t.Errorf("skipped %+v, %v", imported, err)
	}
}

// Links name a browser fingerprint with its version, as "chrome_120", which Xray does not know by that
// name; the browser's preset stands for it (probe N10-14). A name that is no browser stays refused.
func TestVersionedFingerprintNamesTakeTheBrowsersPreset(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	const reality = "&security=reality&pbk=FCNsCTRerqst97Y-_Pc32LnnBu0g_hw-LhJYlaeiEVs&sid=c2d3"
	for _, test := range []struct{ fp, want string }{
		{"chrome_120", "chrome"}, {"Firefox_148", "firefox"}, {"safari_26_3", "safari"}, {"ios_14", "ios"}, {"edge_106", "edge"},
		{"360_11_0", "360"}, {"qq_11_1", "qq"}, {"android_11_okhttp", "android"},
		{"chrome", "chrome"}, {"randomized", "randomized"}, {"hellochrome_120", "hellochrome_120"}, {"HelloFirefox_120", "HelloFirefox_120"},
		{"chrome_x", ""}, {"opera_100", ""}, {"chrome_", ""},
	} {
		for _, security := range []string{"&security=tls", reality} {
			link := "vless://" + uuid + "@example.com:443?type=tcp&sni=example.com&fp=" + test.fp + security + "#fp-versioned"
			imported, err := ImportClientProfiles(link)
			if test.want == "" {
				if err == nil {
					t.Errorf("%s imported as %s", link, imported.Profiles[0].Config)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s: %v", link, err)
				continue
			}
			var profile conf.Config
			if err := json.Unmarshal(imported.Profiles[0].Config, &profile); err != nil {
				t.Fatal(err)
			}
			stream := profile.OutboundConfigs[0].StreamSetting
			got := ""
			if stream.TLSSettings != nil {
				got = stream.TLSSettings.Fingerprint
			} else if stream.REALITYSettings != nil {
				got = stream.REALITYSettings.Fingerprint
			}
			if got != test.want {
				t.Errorf("%s: fingerprint %q, want %q", link, got, test.want)
			}
		}
	}
	qr := `{"v":"2","ps":"m","add":"example.com","port":"443","id":"` + uuid + `","net":"tcp","tls":"tls","sni":"example.com","fp":"chrome_120"}`
	imported, err := ImportClientProfiles("vmess://" + base64.StdEncoding.EncodeToString([]byte(qr)))
	if err != nil || !strings.Contains(string(imported.Profiles[0].Config), `"fingerprint":"chrome"`) {
		t.Errorf("VMess QR fp: %+v, %v", imported, err)
	}
}

// Remnawave writes mKCP's mtu and tti in links (probe N10-14); the core checks their range.
func TestMKCPLinkMTUAndTTI(t *testing.T) {
	const link = "ss://YWVzLTI1Ni1nY206cHc@example.com:8388?type=kcp&headerType=wireguard&seed=fixture"
	for _, test := range []struct {
		query    string
		mtu, tti uint32
		refused  bool
	}{
		{query: "&mtu=1350&tti=20", mtu: 1350, tti: 20},
		{query: "&mtu=1200", mtu: 1200, tti: 50},
		{query: "&tti=100", mtu: 1350, tti: 100},
		{query: "", mtu: 1350, tti: 50},
		{query: "&mtu=abc", refused: true},
		{query: "&tti=-1", refused: true},
		{query: "&tti=5", refused: true},
		{query: "&mtu=20", refused: true},
	} {
		imported, err := ImportClientProfiles(link + test.query + "#kcp")
		if test.refused {
			if err == nil {
				t.Errorf("%q imported as %s", test.query, imported.Profiles[0].Config)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", test.query, err)
			continue
		}
		stream := builtOutbound(t, imported.Profiles[0].Config).stream.(*internet.StreamConfig)
		settings, err := stream.TransportSettings[0].Settings.GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		if config := settings.(*kcp.Config); config.GetMtu() != test.mtu || config.GetTti() != test.tti {
			t.Errorf("%q: mtu %d, tti %d", test.query, config.GetMtu(), config.GetTti())
		}
	}
}

// Marzban writes a VMess REALITY server as "tls": "reality" in the VMess JSON, with pbk, sid and spx
// (probe N10-14). Without the public key no handshake can be made.
func TestVMessQRWithREALITY(t *testing.T) {
	const fields = `"v":"2","ps":"r","add":"example.com","port":"443","id":"11111111-2222-3333-4444-555555555555","net":"tcp","tls":"reality","sni":"example.com","fp":"chrome"`
	imported, err := ImportClientProfiles("vmess://" + base64.StdEncoding.EncodeToString([]byte(
		`{`+fields+`,"pbk":"FCNsCTRerqst97Y-_Pc32LnnBu0g_hw-LhJYlaeiEVs","sid":"c2d3","spx":"/fixture"}`)))
	if err != nil {
		t.Fatal(err)
	}
	var profile conf.Config
	if err := json.Unmarshal(imported.Profiles[0].Config, &profile); err != nil {
		t.Fatal(err)
	}
	stream := profile.OutboundConfigs[0].StreamSetting
	if reality := stream.REALITYSettings; stream.Security != "reality" || stream.TLSSettings != nil || reality == nil || reality.Fingerprint != "chrome" ||
		reality.ServerName != "example.com" || reality.PublicKey != "FCNsCTRerqst97Y-_Pc32LnnBu0g_hw-LhJYlaeiEVs" || reality.ShortId != "c2d3" || reality.SpiderX != "/fixture" {
		t.Fatalf("imported as %s", imported.Profiles[0].Config)
	}
	builtOutbound(t, imported.Profiles[0].Config)
	if _, err := parseVMessQrCode(`{` + fields + `,"sid":"c2d3"}`); err == nil || err.Error() != "VMess REALITY server without a public key" {
		t.Errorf("without a public key: %v", err)
	}
}

// The legacy Shadowsocks URI encodes the whole authority: ss://base64(method:password@host:port)#name.
func TestLegacyShadowsocksLinks(t *testing.T) {
	for link, want := range map[string][4]string{
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:p@ss:w/rd@127.0.0.1:8388")) + "#Legacy%20name": {"aes-256-gcm", "p@ss:w/rd", "127.0.0.1", "Legacy name"},
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:secret@legacy.example.com:443")): {"chacha20-ietf-poly1305", "secret", "legacy.example.com", ""},
		"ss://" + base64.RawStdEncoding.EncodeToString([]byte("aes-128-gcm:pw@[2001:db8::1]:8388")) + "#v6":            {"aes-128-gcm", "pw", "2001:db8::1", "v6"},
	} {
		result, err := ImportClientProfiles(link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		var root struct {
			Outbounds []struct {
				Settings map[string]any `json:"settings"`
			} `json:"outbounds"`
		}
		if err := json.Unmarshal(result.Profiles[0].Config, &root); err != nil {
			t.Fatal(err)
		}
		settings := root.Outbounds[0].Settings
		got := [4]string{fmt.Sprint(settings["method"]), fmt.Sprint(settings["password"]), fmt.Sprint(settings["address"]), result.Profiles[0].Name}
		if want[3] == "" {
			want[3] = "shadowsocks"
		}
		if got != want || settings["port"] == nil {
			t.Errorf("%s: %v, want %v", link, got, want)
		}
	}
	for _, link := range []string{
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:pw@2001:db8::1:8388")),
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:pw@127.0.0.1")),
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm@127.0.0.1:8388")),
		"ss://" + base64.StdEncoding.EncodeToString([]byte(":pw@127.0.0.1:8388")),
	} {
		if result, err := ImportClientProfiles(link); err == nil {
			t.Errorf("%s imported as %s", link, result.Profiles[0].Config)
		}
	}
}

// Xray writes an IPv6 address in brackets; profiles store the bare literal, as Xray JSON writes it.
func TestIPv6ServerAddressIsStoredWithoutBrackets(t *testing.T) {
	const id = "b831381d-6324-4d53-ad4f-8cda48b30811"
	qr := `{"v":"2","ps":"q","add":"2001:db8::10","port":"443","id":"` + id + `","net":"tcp"}`
	for _, link := range []string{
		"vless://" + id + "@[2001:db8::10]:443?encryption=none&security=tls&sni=v6.example.com&type=tcp#v6",
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(qr)),
		"vmess://" + id + "@[2001:db8::10]:443#v6",
		"trojan://pw@[2001:db8::10]:443#v6",
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw")) + "@[2001:db8::10]:8388#v6",
		"socks://[2001:db8::10]:1080#v6",
		"http://[2001:db8::10]:8080#v6",
		"hy2://pw@[2001:db8::10]:443?sni=v6.example.com#v6",
		"tuic://" + id + ":pw@[2001:db8::10]:443?sni=v6.example.com#v6",
		"sq://user:pw@[2001:db8::10]:443?sni=v6.example.com#v6",
		"proxies:\n  - {name: v6, type: trojan, server: '2001:db8::10', port: 443, password: pw}\n",
		"proxies:\n  - {name: v6, type: shadowquic, server: '2001:db8::10', port: 443, username: user, password: pw, sni: v6.example.com}\n",
	} {
		result, err := ImportClientProfiles(link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		var root struct {
			Outbounds []struct {
				Settings struct {
					Address string `json:"address"`
				} `json:"settings"`
			} `json:"outbounds"`
		}
		if err := json.Unmarshal(result.Profiles[0].Config, &root); err != nil {
			t.Fatal(err)
		}
		if got := root.Outbounds[0].Settings.Address; got != "2001:db8::10" {
			t.Errorf("%s: address %q", link, got)
		}
		if strings.HasPrefix(link, "http://") {
			continue // Share writes no HTTP proxy links
		}
		links, err := ConvertXrayJsonToShareLinks(result.Profiles[0].Config)
		if err != nil || !strings.Contains(links, "[2001:db8::10]:") {
			t.Errorf("%s shares as %q (%v)", link, links, err)
		}
	}
}

// Mihomo's own resolver settings (fake-ip answers, bootstrap servers) describe Clash's DNS listener,
// which this app does not run; Remnawave's default template carries them.
func TestClashDNSIgnoresMihomoResolverSettings(t *testing.T) {
	input := `mode: rule
dns:
  enable: true
  use-hosts: true
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  default-nameserver: [1.1.1.1, 8.8.8.8]
  nameserver: [1.1.1.1, 8.8.8.8]
  fake-ip-filter: ['*.lan']
proxies:
  - {name: a, type: vless, server: 127.0.0.1, port: 443, uuid: 00000000-0000-4000-8000-000000000001}
`
	result, err := ImportClientProfiles(input)
	if err != nil || len(result.Profiles) != 1 || result.SkippedCount != 0 {
		t.Fatalf("%+v, %v", result, err)
	}
	if !strings.Contains(string(result.Profiles[0].Config), `"servers":["1.1.1.1","8.8.8.8"]`) {
		t.Fatalf("nameservers lost: %s", result.Profiles[0].Config)
	}
	if _, err := ImportClientProfiles(strings.Replace(input, "use-hosts: true", "fallback: [9.9.9.9]", 1)); err == nil {
		t.Fatal("an unknown DNS field imported")
	}
}

// A feed's text line names no server kind: only a scheme before "://" does.
func TestSkippedTextLinesAreNotNamedAsSchemes(t *testing.T) {
	result, err := ImportClientProfiles("status: 12 GB left\nssh://user@127.0.0.1:22\ntrojan://pw@127.0.0.1:443#One")
	want := []SkippedProfile{{Kind: "link"}, {Kind: "ssh"}}
	if err != nil || len(result.Profiles) != 1 || !reflect.DeepEqual(result.Skipped, want) {
		t.Fatalf("%+v, %v; want skipped %+v", result, err, want)
	}
}

// Providers write a message into the remark of a line whose server cannot be theirs: a loopback,
// unspecified, documentation or well-known resolver address, or port 0 or 1. Such a profile is
// marked, so a subscription can show it as a notice instead of a server.
func TestProviderMessageLinesAreMarkedAsNotices(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	notice := func(text string) bool {
		t.Helper()
		result, err := ImportClientProfiles(text)
		if err != nil || len(result.Profiles) != 1 {
			t.Fatalf("%s: %+v, %v", text, result, err)
		}
		encoded, err := json.Marshal(result.Profiles[0])
		if err != nil {
			t.Fatal(err)
		}
		if marked := strings.Contains(string(encoded), `"notice":true`); marked != result.Profiles[0].Notice {
			t.Errorf("%s: encoded as %s", text, encoded)
		}
		if !result.Profiles[0].Notice && strings.Contains(string(encoded), `"notice"`) {
			t.Errorf("%s: an unmarked profile carries the field: %s", text, encoded)
		}
		return result.Profiles[0].Notice
	}
	// A Trojan server at 1.1.1.1 with TLS that names no serverName, and the given transport.
	cdn := func(stream string) string {
		return `{"outbounds":[{"protocol":"trojan","settings":{"servers":[{"address":"1.1.1.1","port":443,"password":"pw"}]},` +
			`"streamSettings":{"security":"tls",` + stream + `}}]}`
	}
	for _, line := range []string{
		// The three lines of the report.
		"socks://127.0.0.1:1080#📊 10GB left",
		"socks://192.0.2.1:1#Device limit reached",
		"trojan://1@1.1.1.1#✖ Package Ended",
		"vless://" + id + "@0.0.0.0:443?encryption=none&security=tls&sni=a.example#Expires 2026-12-01",
		"vless://" + id + "@[::1]:443?encryption=none&security=none#Renew",
		"vless://" + id + "@[2001:db8::7]:443?encryption=none&security=tls&sni=a.example#Renew",
		"vless://" + id + "@198.51.100.9:443?encryption=none&security=tls&sni=a.example#Renew",
		"vless://" + id + "@203.0.113.9:443?encryption=none&security=tls&sni=a.example#Renew",
		"trojan://pw@server.example:1?security=tls&sni=a.example#Renew",
		// A resolver address that names no host, or only itself.
		"socks://8.8.8.8:1080#Notice",
		"trojan://pw@1.0.0.1:443?security=tls#Renew",
		"trojan://pw@8.8.4.4:443?security=tls&sni=8.8.4.4#Renew",
		"vless://" + id + "@1.1.1.1:443?encryption=none&security=tls&type=ws#Renew",
		`{"outbounds":[{"protocol":"socks","settings":{"servers":[{"address":"::ffff:127.0.0.1","port":1080}]}}]}`,
		// localhost is loopback in any case.
		"socks://localhost:1080#📊 10GB left",
		"socks://LocalHost:1080#📊 10GB left",
	} {
		if !notice(line) {
			t.Errorf("%s: not marked", line)
		}
	}
	for _, line := range []string{
		"vless://" + id + "@server.example:443?encryption=none&security=reality&type=tcp&fp=chrome" +
			"&pbk=FCNsCTRerqst97Y-_Pc32LnnBu0g_hw-LhJYlaeiEVs&sni=www.example.com&sid=0a1b#DE-1 9.52GB",
		"vless://" + id + "@10.0.0.1:443?encryption=none&security=none#Lan",
		"trojan://pw@1.1.1.2:443?security=tls&sni=a.example#Near a resolver",
		"vless://" + id + "@192.0.3.1:443?encryption=none&security=tls&sni=a.example#Past the range",
		"socks://127.0.0.1.example:1080#A name",
		"socks://localhost.example:1080#A name",
		// 1.1.1.1 and 1.0.0.1 are also Cloudflare's edge: a server behind it names its own host.
		"trojan://pw@1.0.0.1:443?security=tls&sni=a.example#Renew",
		"trojan://pw@8.8.8.8:443?security=tls&sni=a.example#Renew",
		"trojan://pw@8.8.4.4:443?security=tls&sni=a.example#Renew",
		"vless://" + id + "@1.1.1.1:443?encryption=none&security=tls&type=ws&host=cdn.example&sni=cdn.example#CDN",
		"vless://" + id + "@1.1.1.1:443?encryption=none&security=tls&type=ws&host=cdn.example#CDN",
		"vless://" + id + "@1.1.1.1:443?encryption=none&security=reality&type=tcp&fp=chrome" +
			"&pbk=FCNsCTRerqst97Y-_Pc32LnnBu0g_hw-LhJYlaeiEVs&sni=cdn.example&sid=0a1b#CDN",
		// The host header alone, without TLS.
		`{"outbounds":[{"protocol":"vmess","settings":{"vnext":[{"address":"1.1.1.1","port":80,"users":[{"id":"` + id + `"}]}]},` +
			`"streamSettings":{"network":"ws","wsSettings":{"host":"cdn.example"}}}]}`,
		cdn(`"network":"ws","wsSettings":{"headers":{"Host":"cdn.example"}}`),
		cdn(`"network":"httpupgrade","httpupgradeSettings":{"host":"cdn.example"}`),
		cdn(`"network":"xhttp","xhttpSettings":{"host":"cdn.example"}`),
		cdn(`"network":"splithttp","splithttpSettings":{"host":"cdn.example"}`),
		cdn(`"network":"grpc","grpcSettings":{"authority":"cdn.example","serviceName":"s"}`),
		cdn(`"network":"tcp","tcpSettings":{"header":{"type":"http","request":{"headers":{"Host":["cdn.example"]}}}}`),
		cdn(`"network":"raw","rawSettings":{"header":{"type":"http","request":{"headers":{"host":"cdn.example"}}}}`),
		// One real server among them makes the profile a server.
		`{"outbounds":[{"protocol":"socks","settings":{"servers":[{"address":"127.0.0.1","port":1080}]}},` +
			`{"protocol":"socks","settings":{"servers":[{"address":"server.example","port":1080}]}}]}`,
		// No server at all is no notice either.
		`{"outbounds":[{"protocol":"freedom"}]}`,
	} {
		if notice(line) {
			t.Errorf("%s: marked as a notice", line)
		}
	}
}

// AmneziaVPN fills the DNS placeholders from its own settings (1.1.1.1 and 1.0.0.1 by default)
// when a key names no usable dns1 or dns2 (NativeServerConfig::getDnsPair).
func TestAmneziaVPNKeyWithoutDNSUsesAmneziaDefaults(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	config := "[Interface]\nAddress = 10.8.1.2/32\nDNS = $PRIMARY_DNS, $SECONDARY_DNS\nPrivateKey = " + key + "\n" +
		"\n[Peer]\nPublicKey = " + key + "\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = 127.0.0.1:51820\n"
	for name, document := range map[string]map[string]any{
		"absent":   {"containers": []any{amneziaContainer(t, "amnezia-wireguard", "wireguard", config)}, "defaultContainer": "amnezia-wireguard"},
		"not IPv4": {"containers": []any{amneziaContainer(t, "amnezia-wireguard", "wireguard", config)}, "defaultContainer": "amnezia-wireguard", "dns1": "dns.example", "dns2": ""},
	} {
		result, err := ImportClientProfiles(amneziaKey(t, document))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var root struct {
			DNS struct {
				Servers []string `json:"servers"`
			} `json:"dns"`
		}
		if err := json.Unmarshal(result.Profiles[0].Config, &root); err != nil || !slices.Equal(root.DNS.Servers, []string{"1.1.1.1", "1.0.0.1"}) {
			t.Fatalf("%s: %s (%v)", name, result.Profiles[0].Config, err)
		}
	}
}
