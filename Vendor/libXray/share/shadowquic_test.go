package share

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/shadowquic"
	"google.golang.org/protobuf/proto"
)

// The settings every ShadowQUIC server here has; a case adds what it is about.
const shadowQUICServer = `"address":"sq.example.com","port":443,"username":"user","password":"pass","serverName":"cdn.example.com"`

type importedShadowQUIC struct {
	name, tag string
	config    json.RawMessage
	settings  json.RawMessage
	built     *shadowquic.ClientConfig
}

// importShadowQUIC imports input, which must give one ShadowQUIC server, and checks what was stored: an outbound
// without the fields the Xray parser refuses for this protocol, which passes the import's validation and
// builds, so what an import stores is what the core runs.
func importShadowQUIC(t *testing.T, input string) importedShadowQUIC {
	t.Helper()
	imported, err := ImportClientProfiles(input)
	if err != nil {
		t.Fatalf("%q: %v", input, err)
	}
	if len(imported.Profiles) != 1 || imported.SkippedCount != 0 {
		t.Fatalf("%q: %d profiles, %d skipped", input, len(imported.Profiles), imported.SkippedCount)
	}
	profile := imported.Profiles[0]
	if err := validateClientProfile(profile.Config); err != nil {
		t.Fatalf("%q: the stored profile does not validate: %v\n%s", input, err, profile.Config)
	}
	var root struct {
		Outbounds []map[string]json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(profile.Config, &root); err != nil || len(root.Outbounds) == 0 || string(root.Outbounds[0]["protocol"]) != `"shadowquic"` {
		t.Fatalf("%q: stored %s (%v)", input, profile.Config, err)
	}
	outbound := root.Outbounds[0]
	for key, value := range outbound {
		switch key {
		case "protocol", "tag", "settings":
		case "streamSettings", "mux", "proxySettings", "sendThrough":
			if string(value) != "null" {
				t.Errorf("%q: stored %s %s", input, key, value)
			}
		case "targetStrategy":
			if string(value) != `""` {
				t.Errorf("%q: stored %s %s", input, key, value)
			}
		default:
			t.Errorf("%q: stored outbound field %s", input, key)
		}
	}
	var tag string
	_ = json.Unmarshal(outbound["tag"], &tag)
	built, ok := builtOutbound(t, profile.Config).proxy.(*shadowquic.ClientConfig)
	if !ok {
		t.Fatalf("%q: built %T", input, built)
	}
	return importedShadowQUIC{name: profile.Name, tag: tag, config: profile.Config, settings: outbound["settings"], built: built}
}

func withShadowQUICServer(fields string) string { return "{" + shadowQUICServer + fields + "}" }

// Each link with the settings it is stored as and the name it is shown with.
var shadowQUICLinks = []struct{ about, link, settings, name string }{
	{"every parameter", "sq://user:pass@sq.example.com:8443?sni=cdn.example.com&udp_mode=stream&zero_rtt=1&mtu=1400&alpn=h3,h2&congestion=cubic#Full",
		`{"address":"sq.example.com","port":8443,"username":"user","password":"pass","serverName":"cdn.example.com","alpn":["h3","h2"],"congestionControl":"cubic","zeroRtt":true,"udpOverStream":true,"initialMtu":1400,"minMtu":1400}`, "Full"},
	// Port 443, UDP over streams, no 0-RTT; ALPN, congestion control and MTU are the client's own.
	{"only what a link must say", "sq://user:pass@sq.example.com?sni=cdn.example.com#Minimal", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true`), "Minimal"},
	{"the long scheme", "shadowquic://user:pass@sq.example.com?sni=cdn.example.com#Long", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true`), "Long"},
	{"a slash before the query", "sq://user:pass@sq.example.com:443/?sni=cdn.example.com#Slash", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true`), "Slash"},
	{"no name", "sq://user:pass@sq.example.com?sni=cdn.example.com", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true`), "shadowquic"},
	{"UDP over datagrams", "sq://user:pass@sq.example.com?sni=cdn.example.com&udp_mode=datagram#D", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":false`), "D"},
	{"0-RTT without a value", "sq://user:pass@sq.example.com?sni=cdn.example.com&zero_rtt#Z", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true`), "Z"},
	{"0-RTT with an empty value", "sq://user:pass@sq.example.com?sni=cdn.example.com&zero_rtt=#Z", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true`), "Z"},
	{"0-RTT true", "sq://user:pass@sq.example.com?sni=cdn.example.com&zero_rtt=true#Z", withShadowQUICServer(`,"zeroRtt":true,"udpOverStream":true`), "Z"},
	{"0-RTT with other text", "sq://user:pass@sq.example.com?sni=cdn.example.com&zero_rtt=yes#Z", withShadowQUICServer(`,"zeroRtt":true,"udpOverStream":true`), "Z"},
	{"0-RTT 0", "sq://user:pass@sq.example.com?sni=cdn.example.com&zero_rtt=0#Z", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true`), "Z"},
	{"0-RTT false", "sq://user:pass@sq.example.com?sni=cdn.example.com&zero_rtt=false#Z", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true`), "Z"},
	{"0-RTT FALSE", "sq://user:pass@sq.example.com?sni=cdn.example.com&zero_rtt=FALSE#Z", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true`), "Z"},
	{"QUIC's smallest MTU", "sq://user:pass@sq.example.com?sni=cdn.example.com&mtu=1200#M", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true,"initialMtu":1200,"minMtu":1200`), "M"},
	{"the largest MTU", "sq://user:pass@sq.example.com?sni=cdn.example.com&mtu=65535#M", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true,"initialMtu":65535,"minMtu":65535`), "M"},
	{"an ALPN list", "sq://user:pass@sq.example.com?sni=cdn.example.com&alpn=h3,h2,http%2F1.1#A", withShadowQUICServer(`,"alpn":["h3","h2","http/1.1"],"zeroRtt":false,"udpOverStream":true`), "A"},
	{"a percent-encoded ALPN list", "sq://user:pass@sq.example.com?sni=cdn.example.com&alpn=h3%2Ch2#A", withShadowQUICServer(`,"alpn":["h3","h2"],"zeroRtt":false,"udpOverStream":true`), "A"},
	{"an empty ALPN", "sq://user:pass@sq.example.com?sni=cdn.example.com&alpn=#A", withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":true`), "A"},
	{"BBR", "sq://user:pass@sq.example.com?sni=cdn.example.com&congestion=bbr#C", withShadowQUICServer(`,"congestionControl":"bbr","zeroRtt":false,"udpOverStream":true`), "C"},
	{"CUBIC", "sq://user:pass@sq.example.com?sni=cdn.example.com&congestion=cubic#C", withShadowQUICServer(`,"congestionControl":"cubic","zeroRtt":false,"udpOverStream":true`), "C"},
	{"NewReno", "sq://user:pass@sq.example.com?sni=cdn.example.com&congestion=new_reno#C", withShadowQUICServer(`,"congestionControl":"new-reno","zeroRtt":false,"udpOverStream":true`), "C"},
	{"NewReno with a hyphen", "sq://user:pass@sq.example.com?sni=cdn.example.com&congestion=new-reno#C", withShadowQUICServer(`,"congestionControl":"new-reno","zeroRtt":false,"udpOverStream":true`), "C"},
	{"Reno", "sq://user:pass@sq.example.com?sni=cdn.example.com&congestion=reno#C", withShadowQUICServer(`,"congestionControl":"new-reno","zeroRtt":false,"udpOverStream":true`), "C"},
	// Algorithms other clients have fall back to the client's own default.
	{"BBRv3", "sq://user:pass@sq.example.com?sni=cdn.example.com&congestion=bbrv3#C", withShadowQUICServer(`,"congestionControl":"bbr","zeroRtt":false,"udpOverStream":true`), "C"},
	{"Brutal", "sq://user:pass@sq.example.com?sni=cdn.example.com&congestion=brutal#C", withShadowQUICServer(`,"congestionControl":"bbr","zeroRtt":false,"udpOverStream":true`), "C"},
	{"Copa", "sq://user:pass@sq.example.com?sni=cdn.example.com&congestion=copa#C", withShadowQUICServer(`,"congestionControl":"bbr","zeroRtt":false,"udpOverStream":true`), "C"},
	{"percent-encoded credentials and name", "sq://us%40er:p%40ss%3Aw%2Fd@sq.example.com?sni=cdn.example.com#My%20server%20%E2%9C%93",
		`{"address":"sq.example.com","port":443,"username":"us@er","password":"p@ss:w/d","serverName":"cdn.example.com","zeroRtt":false,"udpOverStream":true}`, "My server ✓"},
	{"an IPv6 server", "sq://user:pass@[2001:db8::10]:443?sni=cdn.example.com#v6",
		`{"address":"2001:db8::10","port":443,"username":"user","password":"pass","serverName":"cdn.example.com","zeroRtt":false,"udpOverStream":true}`, "v6"},
}

func TestShadowQUICLinkImport(t *testing.T) {
	var feed []string
	for _, test := range shadowQUICLinks {
		t.Run(test.about, func(t *testing.T) {
			imported := importShadowQUIC(t, test.link)
			if !bytesEqualJSON(imported.settings, []byte(test.settings)) {
				t.Errorf("stored\n%s\nwant\n%s", imported.settings, test.settings)
			}
			if imported.name != test.name {
				t.Errorf("named %q, want %q", imported.name, test.name)
			}
			// The legacy converter, still exported through Invoke, writes a profile that runs.
			config, err := ConvertShareLinksToXrayJson(test.link)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := config.Build(); err != nil {
				t.Fatalf("the converted profile does not build: %v", err)
			}
			if len(config.OutboundConfigs) != 1 || config.OutboundConfigs[0].SendThrough != nil || !bytesEqualJSON(*config.OutboundConfigs[0].Settings, []byte(test.settings)) {
				t.Fatalf("converted %+v", config.OutboundConfigs)
			}
			// Xray refuses sendThrough, where the converter keeps other links' names, for this protocol.
			if link, _ := url.Parse(test.link); config.OutboundConfigs[0].Tag != link.Fragment {
				t.Errorf("converted with the tag %q, want %q", config.OutboundConfigs[0].Tag, link.Fragment)
			}
		})
		feed = append(feed, test.link)
	}
	// A subscription of all of them, as providers send it.
	imported, err := ImportClientProfiles(base64.StdEncoding.EncodeToString([]byte(strings.Join(feed, "\n"))))
	if err != nil || len(imported.Profiles) != len(feed) || imported.SkippedCount != 0 {
		t.Fatalf("feed imported %+v (%v)", imported, err)
	}
}

func TestShadowQUICLinkRefusals(t *testing.T) {
	const link = "sq://user:pass@sq.example.com:443?sni=cdn.example.com&udp_mode=stream&mtu=1400#Bad"
	importShadowQUIC(t, link)
	edit := func(old, replacement string) string {
		if !strings.Contains(link, old) {
			t.Fatalf("the fixture has no %q", old)
		}
		return strings.Replace(link, old, replacement, 1)
	}
	for name, bad := range map[string]string{
		"no SNI":                   edit("sni=cdn.example.com&", ""),
		"an empty SNI":             edit("sni=cdn.example.com", "sni="),
		"an SNI with a space":      edit("sni=cdn.example.com", "sni=cdn%20example.com"),
		"two SNIs":                 edit("sni=cdn.example.com", "sni=cdn.example.com&sni=other.example.com"),
		"no user name":             edit("user:pass@", ":pass@"),
		"an empty password":        edit("user:pass@", "user:@"),
		"no password":              edit("user:pass@", "user@"),
		"no credentials":           edit("user:pass@", ""),
		"another UDP mode":         edit("udp_mode=stream", "udp_mode=quic"),
		"an empty UDP mode":        edit("udp_mode=stream", "udp_mode="),
		"an MTU below QUIC's":      edit("mtu=1400", "mtu=1199"),
		"an MTU in words":          edit("mtu=1400", "mtu=large"),
		"an MTU above 65535":       edit("mtu=1400", "mtu=65536"),
		"an empty MTU":             edit("mtu=1400", "mtu="),
		"an unknown parameter":     edit("#Bad", "&token=x#Bad"),
		"TUIC's congestion name":   edit("#Bad", "&congestion_control=bbr#Bad"),
		"an insecure flag":         edit("#Bad", "&insecure=1#Bad"),
		"an empty ALPN name":       edit("#Bad", "&alpn=h3,,h2#Bad"),
		"an ALPN name too long":    edit("#Bad", "&alpn="+strings.Repeat("a", 256)+"#Bad"),
		"port 0":                   edit(":443?", ":0?"),
		"a port above 65535":       edit(":443?", ":65536?"),
		"IPv6 without brackets":    edit("sq.example.com:443", "2001:db8::10:443"),
		"a path":                   edit(":443?", ":443/path?"),
		"a server name with space": edit("@sq.example.com", "@sq%20example.com"),
	} {
		t.Run(name, func(t *testing.T) {
			if imported, err := ImportClientProfiles(bad); err == nil {
				t.Errorf("%s imported as %s", bad, imported.Profiles[0].Config)
			}
			if config, err := ConvertShareLinksToXrayJson(bad); err == nil {
				t.Errorf("%s converted to %+v", bad, config.OutboundConfigs)
			}
		})
	}
	// In a feed the bad link is skipped, and named, and the good one imports.
	imported, err := ImportClientProfiles(link + "\n" + edit(":443?", ":0?"))
	if err != nil || len(imported.Profiles) != 1 || imported.SkippedCount != 1 || imported.Skipped[0] != (SkippedProfile{Name: "Bad", Kind: "sq"}) {
		t.Fatalf("feed with a bad ShadowQUIC link: %+v (%v)", imported, err)
	}
}

func shadowQUICProfile(tag, settings string) string {
	return `{"outbounds":[{"protocol":"shadowquic","tag":"` + tag + `","settings":{` + settings + `}}]}`
}

func TestShadowQUICShareLink(t *testing.T) {
	for _, test := range []struct{ about, name, settings, link string }{
		{"every setting", "Every ✓", `"address":"2001:db8::10","port":8443,"username":"us@er","password":"p@ss:w/d","serverName":"cdn.example.com",` +
			`"alpn":["h3","h2"],"congestionControl":"new-reno","zeroRtt":true,"udpOverStream":false,"initialMtu":1300,"minMtu":1300`,
			"sq://us%40er:p%40ss%3Aw%2Fd@[2001:db8::10]:8443?alpn=h3%2Ch2&congestion=new_reno&mtu=1300&sni=cdn.example.com&udp_mode=datagram&zero_rtt=1#Every%20%E2%9C%93"},
		{"only what a server must set", "Plain", shadowQUICServer, "sq://user:pass@sq.example.com:443?sni=cdn.example.com&udp_mode=datagram#Plain"},
		{"UDP over streams", "S", shadowQUICServer + `,"udpOverStream":true,"zeroRtt":false`, "sq://user:pass@sq.example.com:443?sni=cdn.example.com&udp_mode=stream#S"},
		{"BBR", "B", shadowQUICServer + `,"congestionControl":"bbr"`, "sq://user:pass@sq.example.com:443?congestion=bbr&sni=cdn.example.com&udp_mode=datagram#B"},
		{"CUBIC", "C", shadowQUICServer + `,"congestionControl":"cubic"`, "sq://user:pass@sq.example.com:443?congestion=cubic&sni=cdn.example.com&udp_mode=datagram#C"},
		{"an IPv4 server", "4", `"address":"203.0.113.7","port":443,"username":"user","password":"pass","serverName":"cdn.example.com"`,
			"sq://user:pass@203.0.113.7:443?sni=cdn.example.com&udp_mode=datagram#4"},
	} {
		t.Run(test.about, func(t *testing.T) {
			profile := shadowQUICProfile(test.name, test.settings)
			links, err := ConvertXrayJsonToShareLinks([]byte(profile))
			if err != nil || links != test.link {
				t.Fatalf("exported %q (%v), want %q", links, err, test.link)
			}
			again := importShadowQUIC(t, links)
			if want := builtOutbound(t, []byte(profile)).proxy; !proto.Equal(want, again.built) {
				t.Errorf("the link imports as\n%v\nnot\n%v", again.built, want)
			}
			if again.name != test.name {
				t.Errorf("the link imports with the name %q", again.name)
			}
		})
	}
}

// Import, share and import again give the same stored server.
func TestShadowQUICLinksRoundTrip(t *testing.T) {
	for _, test := range shadowQUICLinks {
		t.Run(test.about, func(t *testing.T) {
			first := importShadowQUIC(t, test.link)
			links, err := ConvertXrayJsonToShareLinks(first.config)
			if err != nil || !strings.HasPrefix(links, "sq://") || strings.Contains(links, "\n") {
				t.Fatalf("exported %q (%v)", links, err)
			}
			second := importShadowQUIC(t, links)
			if !bytesEqualJSON(first.settings, second.settings) {
				t.Errorf("stored\n%s\nafter the link %s, was\n%s", second.settings, links, first.settings)
			}
			if again, err := ConvertXrayJsonToShareLinks(second.config); err != nil || again != links {
				t.Errorf("shared again as %q (%v), was %q", again, err, links)
			}
		})
	}
}

// A link has one MTU for both values and splits ALPN names at commas. Alone, a server that sets more is
// shared as its profile, as a TUIC server with options is; beside other servers it is left out.
func TestShadowQUICLinkThatWouldLoseSettingsIsNotWritten(t *testing.T) {
	const other = `{"protocol":"shadowquic","tag":"Other","settings":{` + shadowQUICServer + `}}`
	for name, fields := range map[string]string{
		"unequal MTUs":      `,"initialMtu":1400,"minMtu":1280`,
		"only initial MTU":  `,"initialMtu":1400`,
		"only smallest MTU": `,"minMtu":1280`,
		"a comma in ALPN":   `,"alpn":["h3,h2"]`,
	} {
		t.Run(name, func(t *testing.T) {
			outbound := `{"protocol":"shadowquic","tag":"Lossy","settings":{` + shadowQUICServer + fields + `}}`
			var parsed conf.OutboundDetourConfig
			if err := json.Unmarshal([]byte(outbound), &parsed); err != nil {
				t.Fatal(err)
			}
			if _, err := parsed.Build(); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			if link, err := shareLink(parsed); err == nil {
				t.Errorf("shared as %s", link)
			}
			alone := `{"outbounds":[` + outbound + `]}`
			if exported, err := ConvertXrayJsonToShareLinks([]byte(alone)); err != nil || exported != alone {
				t.Errorf("alone exported %q (%v)", exported, err)
			}
			both := `{"outbounds":[` + outbound + `,` + other + `]}`
			if exported, err := ConvertXrayJsonToShareLinks([]byte(both)); err != nil || exported != "sq://user:pass@sq.example.com:443?sni=cdn.example.com&udp_mode=datagram#Other" {
				t.Errorf("with another server exported %q (%v)", exported, err)
			}
		})
	}
	// A server that is not one has no link and is no profile to share either.
	for _, settings := range []string{`{}`, `{"address":"sq.example.com","port":443,"username":"user","password":"pass"}`} {
		if exported, err := ConvertXrayJsonToShareLinks([]byte(`{"outbounds":[{"protocol":"shadowquic","settings":` + settings + `}]}`)); err == nil {
			t.Errorf("%s exported %q", settings, exported)
		}
	}
}

const shadowQUICClash = "proxies:\n  - name: Clash SQ\n    type: shadowquic\n    server: sq.example.com\n    port: 443\n    username: user\n    password: pass\n    sni: cdn.example.com\n"

func TestShadowQUICClashProxies(t *testing.T) {
	plain := withShadowQUICServer(`,"zeroRtt":false,"udpOverStream":false`)
	for _, test := range []struct{ about, yaml, settings string }{
		{"every key", "proxies:\n  - name: Clash SQ\n    type: shadowquic\n    server: sq.example.com\n    port: 8443\n    username: user\n    password: pass\n    sni: cdn.example.com\n" +
			"    alpn: [h3, h2]\n    udp-over-stream: true\n    zero-rtt: true\n    congestion-controller: new_reno\n",
			`{"address":"sq.example.com","port":8443,"username":"user","password":"pass","serverName":"cdn.example.com","alpn":["h3","h2"],"congestionControl":"new-reno","zeroRtt":true,"udpOverStream":true}`},
		// No 0-RTT and UDP over datagrams, Mihomo's defaults; ALPN and congestion control are the client's own.
		{"only the required keys", shadowQUICClash, plain},
		{"server-name for sni", strings.Replace(shadowQUICClash, "sni:", "server-name:", 1), plain},
		{"server-name and sni alike", shadowQUICClash + "    server-name: cdn.example.com\n", plain},
		{"an IPv6 server", "proxies:\n  - {name: Clash SQ, type: shadowquic, server: '2001:db8::10', port: 443, username: user, password: pass, sni: cdn.example.com}\n",
			`{"address":"2001:db8::10","port":443,"username":"user","password":"pass","serverName":"cdn.example.com","zeroRtt":false,"udpOverStream":false}`},
		{"an empty ALPN list", shadowQUICClash + "    alpn: []\n", plain},
		{"CUBIC", shadowQUICClash + "    congestion-controller: cubic\n", withShadowQUICServer(`,"congestionControl":"cubic","zeroRtt":false,"udpOverStream":false`)},
		{"BBR", shadowQUICClash + "    congestion-controller: bbr\n", withShadowQUICServer(`,"congestionControl":"bbr","zeroRtt":false,"udpOverStream":false`)},
		{"NewReno with a hyphen", shadowQUICClash + "    congestion-controller: new-reno\n", withShadowQUICServer(`,"congestionControl":"new-reno","zeroRtt":false,"udpOverStream":false`)},
		{"Mihomo's BBR variant", shadowQUICClash + "    congestion-controller: bbr_meta_v1\n", withShadowQUICServer(`,"congestionControl":"bbr","zeroRtt":false,"udpOverStream":false`)},
		// Mihomo's tuning has nothing in the ShadowQUIC client to go to.
		{"Mihomo's tuning", shadowQUICClash + "    quic-versions: [v1, v2]\n    keep-alive-interval: 10000\n    up: 100 Mbps\n    down: 200\n    cwnd: 32\n    bbr-profile: standard\n" +
			"    max-datagram-frame-size: 1400\n    max-open-streams: 1024\n    recv-window-conn: 0\n    recv-window: 0\n    disable-mtu-discovery: false\n", plain},
		// What every proxy may say, at the value that asks for nothing.
		{"keys at their default", shadowQUICClash + "    udp: true\n    tls: true\n    skip-cert-verify: false\n    ip-version: dual\n", plain},
	} {
		t.Run(test.about, func(t *testing.T) {
			imported := importShadowQUIC(t, test.yaml)
			if !bytesEqualJSON(imported.settings, []byte(test.settings)) {
				t.Errorf("stored\n%s\nwant\n%s", imported.settings, test.settings)
			}
			if imported.tag != "Clash SQ" {
				t.Errorf("tagged %q", imported.tag)
			}
			// It shares as a link, which imports as the same server.
			links, err := ConvertXrayJsonToShareLinks(imported.config)
			if err != nil || !strings.HasPrefix(links, "sq://") || strings.Contains(links, "\n") {
				t.Fatalf("exported %q (%v)", links, err)
			}
			if again := importShadowQUIC(t, links); !proto.Equal(again.built, imported.built) {
				t.Errorf("the link imports as\n%v\nnot\n%v", again.built, imported.built)
			}
			// The legacy converter reads the file as well.
			config, err := ConvertShareLinksToXrayJson(test.yaml)
			if err != nil || len(config.OutboundConfigs) != 1 || config.OutboundConfigs[0].Tag != "Clash SQ" || !bytesEqualJSON(*config.OutboundConfigs[0].Settings, []byte(test.settings)) {
				t.Fatalf("converted %+v (%v)", config, err)
			}
			if _, err := config.Build(); err != nil {
				t.Fatalf("the converted profile does not build: %v", err)
			}
		})
	}
	for name, bad := range map[string]string{
		"no username":                strings.Replace(shadowQUICClash, "    username: user\n", "", 1),
		"no password":                strings.Replace(shadowQUICClash, "    password: pass\n", "", 1),
		"an empty password":          strings.Replace(shadowQUICClash, "password: pass", "password: ''", 1),
		"a password in numbers":      strings.Replace(shadowQUICClash, "password: pass", "password: 1234", 1),
		"no sni":                     strings.Replace(shadowQUICClash, "    sni: cdn.example.com\n", "", 1),
		"sni and server-name differ": shadowQUICClash + "    server-name: other.example.com\n",
		"servername":                 strings.Replace(shadowQUICClash, "sni:", "servername:", 1),
		"a quoted flag":              shadowQUICClash + "    zero-rtt: 'true'\n",
		"no UDP":                     shadowQUICClash + "    udp: false\n",
		"no TLS":                     shadowQUICClash + "    tls: false\n",
		"skipped verification":       shadowQUICClash + "    skip-cert-verify: true\n",
		"one IP version":             shadowQUICClash + "    ip-version: ipv4\n",
		"a certificate pin":          shadowQUICClash + "    pin-sha256: '" + strings.Repeat("00", 32) + "'\n",
		"a transport":                shadowQUICClash + "    network: ws\n",
		"an unknown key":             shadowQUICClash + "    token: v4\n",
		"TUIC's key":                 shadowQUICClash + "    udp-relay-mode: native\n",
		"a null congestion name":     shadowQUICClash + "    congestion-controller: null\n",
		"an ALPN text":               shadowQUICClash + "    alpn: h3\n",
	} {
		t.Run(name, func(t *testing.T) {
			if imported, err := ImportClientProfiles(bad); err == nil {
				t.Errorf("imported as %s", imported.Profiles[0].Config)
			}
			if config, err := ConvertShareLinksToXrayJson(bad); err == nil {
				t.Errorf("converted to %+v", config.OutboundConfigs)
			}
		})
	}
	// The keys only ShadowQUIC has stay unknown to the other types.
	for _, key := range []string{"zero-rtt: true", "udp-over-stream: true", "server-name: cdn.example.com", "cwnd: 32"} {
		input := "proxies:\n  - {name: V, type: vless, server: 203.0.113.7, port: 443, uuid: 00000000-0000-4000-8000-000000000001, tls: true, " + key + "}\n"
		if imported, err := ImportClientProfiles(input); err == nil {
			t.Errorf("VLESS with %s imported as %s", key, imported.Profiles[0].Config)
		}
	}
}

// The elevated desktop core takes the compiled profile of a ShadowQUIC server.
func TestShadowQUICElevatedTunnelKeepsTheOutbound(t *testing.T) {
	input, err := json.Marshal(map[string]any{
		"inbounds": []any{map[string]any{"protocol": "tun", "settings": map[string]any{}},
			map[string]any{"protocol": "socks", "listen": "127.0.0.1", "port": 10890}},
		"outbounds": []any{map[string]any{"protocol": "shadowquic", "tag": "proxy", "settings": json.RawMessage(withShadowQUICServer(""))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	owned, err := WindowsTunnelConfig(input, "elarni-fixture")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Outbounds []struct {
			Protocol string          `json:"protocol"`
			Settings json.RawMessage `json:"settings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(owned, &config); err != nil || len(config.Outbounds) == 0 || config.Outbounds[0].Protocol != "shadowquic" ||
		!bytesEqualJSON(config.Outbounds[0].Settings, []byte(withShadowQUICServer(""))) {
		t.Fatalf("owned %s (%v)", owned, err)
	}
}
