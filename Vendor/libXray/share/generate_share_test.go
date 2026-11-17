package share

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/finalmask/salamander"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	"github.com/xtls/xray-core/transport/internet/tls"
	"google.golang.org/protobuf/proto"
)

// Xray JSON keeps its own spelling when it is imported: the vnext and servers lists, REALITY's publicKey and
// the long transport names. "Share" must still write a link that imports again as the same server.
func TestShareLinksOfClassicXrayJSONImportAgain(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	for _, test := range []struct {
		name, outbound string
		want           []string
	}{
		{"vless vnext with a REALITY publicKey",
			`{"protocol":"vless","settings":{"vnext":[{"address":"203.0.113.7","port":8443,"users":[{"id":"` + id + `","flow":"xtls-rprx-vision","encryption":"none"}]}]},
			  "streamSettings":{"network":"tcp","security":"reality","realitySettings":{"serverName":"example.com","fingerprint":"chrome","publicKey":"` + key + `","shortId":"0123abcd"}}}`,
			[]string{"vless://" + id + "@203.0.113.7:8443?", "flow=xtls-rprx-vision", "pbk=" + key, "sid=0123abcd"}},
		{"vless vnext over IPv6",
			`{"protocol":"vless","settings":{"vnext":[{"address":"2001:db8::7","port":443,"users":[{"id":"` + id + `","encryption":"none"}]}]},
			  "streamSettings":{"network":"raw","security":"tls","tlsSettings":{"serverName":"example.com"}}}`,
			[]string{"vless://" + id + "@[2001:db8::7]:443?"}},
		{"vmess vnext over websocket",
			`{"protocol":"vmess","settings":{"vnext":[{"address":"vmess.example.com","port":443,"users":[{"id":"` + id + `","security":"aes-128-gcm"}]}]},
			  "streamSettings":{"network":"websocket","security":"tls","tlsSettings":{"serverName":"vmess.example.com"},"wsSettings":{"path":"/socket","host":"cdn.example.com"}}}`,
			[]string{"vmess://" + id + "@vmess.example.com:443?", "encryption=aes-128-gcm", "type=ws", "path=%2Fsocket", "host=cdn.example.com"}},
		{"trojan servers with an HTTP header on tcp",
			`{"protocol":"trojan","settings":{"servers":[{"address":"203.0.113.8","port":443,"password":"secret"}]},
			  "streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"example.com"},
			    "tcpSettings":{"header":{"type":"http","request":{"path":["/video"],"headers":{"Host":["cdn.example.com"]}}}}}}`,
			[]string{"trojan://secret@203.0.113.8:443?", "headerType=http", "path=%2Fvideo", "host=cdn.example.com"}},
		{"shadowsocks servers",
			`{"protocol":"shadowsocks","settings":{"servers":[{"address":"203.0.113.9","port":8388,"method":"aes-256-gcm","password":"secret"}]}}`,
			[]string{"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:secret")) + "@203.0.113.9:8388"}},
		{"socks servers with a user",
			`{"protocol":"socks","settings":{"servers":[{"address":"203.0.113.10","port":1080,"users":[{"user":"name","pass":"secret"}]}]}}`,
			[]string{"socks://" + base64.StdEncoding.EncodeToString([]byte("name:secret")) + "@203.0.113.10:1080"}},
		{"vless over splithttp",
			`{"protocol":"vless","settings":{"address":"203.0.113.11","port":443,"id":"` + id + `","encryption":"none"},
			  "streamSettings":{"network":"splithttp","security":"tls","tlsSettings":{"serverName":"example.com"},
			    "splithttpSettings":{"host":"cdn.example.com","path":"/x","mode":"packet-up"}}}`,
			[]string{"type=xhttp", "host=cdn.example.com", "path=%2Fx", "mode=packet-up"}},
		{"vless over mkcp",
			`{"protocol":"vless","settings":{"address":"10.0.0.12","port":443,"id":"` + id + `","encryption":"none"},
			  "streamSettings":{"network":"mkcp","kcpSettings":{"seed":"fixture-seed"}}}`,
			[]string{"type=kcp", "seed=fixture-seed"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			imported, err := ImportClientProfiles(`{"outbounds":[` + test.outbound + `]}`)
			if err != nil {
				t.Fatal(err)
			}
			links, err := ConvertXrayJsonToShareLinks(imported.Profiles[0].Config)
			if err != nil || strings.HasPrefix(links, "{") {
				t.Fatalf("exported %q (%v)", links, err)
			}
			for _, part := range test.want {
				if !strings.Contains(links, part) {
					t.Errorf("link lacks %q: %s", part, links)
				}
			}
			again, err := ImportClientProfiles(links)
			if err != nil {
				t.Fatalf("the exported link does not import: %v\n%s", err, links)
			}
			want, got := builtOutbound(t, imported.Profiles[0].Config), builtOutbound(t, again.Profiles[0].Config)
			if !proto.Equal(want.proxy, got.proxy) {
				t.Errorf("server changed:\n%v\nwas\n%v\nlink %s", got.proxy, want.proxy, links)
			}
			if !proto.Equal(want.stream, got.stream) {
				t.Errorf("transport changed:\n%v\nwas\n%v\nlink %s", got.stream, want.stream, links)
			}
		})
	}
}

// An outbound without a server writes no link rather than one to "<nil>:0".
func TestShareLinksSkipAnOutboundWithoutAServer(t *testing.T) {
	for _, outbound := range []string{
		`{"protocol":"vless","settings":{"vnext":[]}}`,
		`{"protocol":"vless","settings":{"vnext":[{"address":"203.0.113.7","port":443,"users":[]}]}}`,
		`{"protocol":"vmess","settings":{}}`,
		`{"protocol":"trojan","settings":{"servers":[{"port":443,"password":"secret"}]}}`,
		`{"protocol":"shadowsocks","settings":{"servers":[]}}`,
		`{"protocol":"socks","settings":{}}`,
		`{"protocol":"hysteria","settings":{"version":2}}`,
		`{"protocol":"shadowquic","settings":{"username":"user","password":"pass","serverName":"cdn.example.com"}}`,
	} {
		links, err := ConvertXrayJsonToShareLinks([]byte(`{"outbounds":[` + outbound + `]}`))
		if err == nil {
			t.Errorf("%s exported %q", outbound, links)
		}
	}
}

// XHTTP settings beyond host, path and mode travel in the link's "extra" parameter, wherever the profile
// keeps them: in its own "extra" (links, Clash) or beside the path (Xray JSON).
func TestShareLinksKeepXHTTPSettings(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	for _, test := range []struct{ name, source string }{
		{"a link with extra", "vless://" + id + "@up.example.com:443?encryption=none&security=tls&sni=a.example.com&type=xhttp&path=%2Fup&mode=auto&extra=" +
			`%7B%22xmux%22%3A%7B%22maxConcurrency%22%3A%222-4%22%7D%2C%22noGRPCHeader%22%3Atrue%2C%22xPaddingBytes%22%3A%2220-40%22%7D#x`},
		{"Xray JSON with the settings beside the path", `{"outbounds":[{"protocol":"vless","settings":{"address":"203.0.113.11","port":443,"id":"` + id + `","encryption":"none"},
			"streamSettings":{"network":"xhttp","security":"tls","tlsSettings":{"serverName":"example.com"},
			  "xhttpSettings":{"host":"cdn.example.com","path":"/x","mode":"packet-up","noGRPCHeader":true,"xPaddingBytes":"20-40","headers":{"X-Fixture":""},
			    "xmux":{"maxConcurrency":"2-4","hKeepAlivePeriod":20},
			    "downloadSettings":{"address":"down.example.com","port":8443,"network":"xhttp","security":"tls","tlsSettings":{"serverName":"d.example.com"},"xhttpSettings":{"path":"/down"}}}}}]}`},
		{"Clash with a separate download server", `proxies:
 - name: fixture
   type: vless
   server: upload.example.com
   port: 443
   uuid: ` + id + `
   tls: true
   servername: front.example.com
   network: xhttp
   xhttp-opts:
     path: /up
     mode: auto
     session-id-placement: header
     session-id-key: X-Session
     x-padding-bytes: 20-40
     reuse-settings: {max-concurrency: 2-4, h-keep-alive-period: 20}
     download-settings:
       server: download.example.com
       port: 8443
       tls: true
       servername: download-front.example.com
       path: /down
`},
	} {
		t.Run(test.name, func(t *testing.T) {
			imported, err := ImportClientProfiles(test.source)
			if err != nil {
				t.Fatal(err)
			}
			links, err := ConvertXrayJsonToShareLinks(imported.Profiles[0].Config)
			if err != nil || strings.HasPrefix(links, "{") {
				t.Fatalf("exported %q (%v)", links, err)
			}
			again, err := ImportClientProfiles(links)
			if err != nil {
				t.Fatalf("the exported link does not import: %v\n%s", err, links)
			}
			want, got := builtOutbound(t, imported.Profiles[0].Config), builtOutbound(t, again.Profiles[0].Config)
			if !proto.Equal(want.stream, got.stream) {
				t.Errorf("transport changed:\n%v\nwas\n%v\nlink %s", got.stream, want.stream, links)
			}
			// A link is also shown as a QR code: it names only what the server sets.
			if link, err := url.Parse(links); err != nil || strings.Contains(link.Query().Get("extra"), "seqKey") {
				t.Errorf("extra carries settings the server leaves unset: %s (%v)", links, err)
			}
			// The name after "#" is the profile's, not the server's.
			second, err := ConvertXrayJsonToShareLinks(again.Profiles[0].Config)
			second, _, _ = strings.Cut(second, "#")
			if links, _, _ := strings.Cut(links, "#"); err != nil || second != links {
				t.Errorf("the imported link exports as\n%s\nnot\n%s (%v)", second, links, err)
			}
		})
	}
}

// What a link has a parameter for must come back as it was: a round trip changes neither the server nor
// its transport.
func TestShareLinksKeepWhatALinkCanSay(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	hysteria := func(mask string) string {
		return `{"outbounds":[{"protocol":"hysteria","settings":{"version":2,"address":"203.0.113.11","port":443},"streamSettings":{"network":"hysteria","security":"tls",
			"tlsSettings":{"serverName":"h.example"},"hysteriaSettings":{"version":2,"auth":"pw"},"finalmask":` + mask + `}}]}`
	}
	for _, test := range []struct{ name, source string }{
		{"Hysteria2 congestion settings in fm", "hy2://pw@203.0.113.9:443?sni=h.example&fm=" + url.QueryEscape(`{"quicParams":{"congestion":"bbr","maxIdleTimeout":40}}`) + "#fm"},
		{"Hysteria2 hop interval range", "proxies:\n  - {name: H2, type: hysteria2, server: 203.0.113.23, port: 443, password: pw, sni: h.example, ports: '20000-30000', hop-interval: '30-60'}\n"},
		{"Hysteria2 hop mode", hysteria(`{"udp":[{"type":"udphop","settings":{"mode":"perConnRemote","interval":30,"remotePorts":"20000-30000"}}]}`)},
		{"Hysteria2 hop, obfuscation and brutal", "hy2://pw@203.0.113.9:443?sni=h.example&ports=20000-30000&hop-interval=30&obfs=salamander&obfs-password=secret&up=20mbps&down=100mbps#all"},
		{"Hysteria2 obfuscation with another congestion", hysteria(`{"quicParams":{"congestion":"bbr"},"udp":[{"type":"salamander","settings":{"password":"secret"}}]}`)},
		{"WebSocket behind a front name without an SNI", `{"outbounds":[{"protocol":"vless","settings":{"address":"front.example","port":443,"id":"` + id + `","encryption":"none"},
			"streamSettings":{"network":"ws","security":"tls","wsSettings":{"path":"/w","host":"cdn.example"}}}]}`},
		{"Trojan without TLS", `{"outbounds":[{"protocol":"trojan","settings":{"address":"10.0.0.8","port":443,"password":"pw"}}]}`},
		{"SOCKS user with a colon", `{"outbounds":[{"protocol":"socks","settings":{"address":"10.0.0.10","port":1080,"user":"us:er","pass":"p:w"}}]}`},
		{"SOCKS link user with a colon", "socks5://us%3Aer:pw@10.0.0.10:1080#S"},
		{"Shadowsocks without stream settings", `{"outbounds":[{"protocol":"shadowsocks","settings":{"address":"10.0.0.9","port":8388,"method":"aes-256-gcm","password":"pw"}}]}`},
		{"VMess without stream settings", `{"outbounds":[{"protocol":"vmess","settings":{"address":"10.0.0.9","port":443,"id":"` + id + `"}}]}`},
		{"VLESS without stream settings", `{"outbounds":[{"protocol":"vless","settings":{"address":"10.0.0.9","port":443,"id":"` + id + `","encryption":"none"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			imported, err := ImportClientProfiles(test.source)
			if err != nil {
				t.Fatal(err)
			}
			links, err := ConvertXrayJsonToShareLinks(imported.Profiles[0].Config)
			if err != nil || strings.HasPrefix(links, "{") {
				t.Fatalf("exported %q (%v)", links, err)
			}
			again, err := ImportClientProfiles(links)
			if err != nil {
				t.Fatalf("the exported link does not import: %v\n%s", err, links)
			}
			want, got := builtOutbound(t, imported.Profiles[0].Config), builtOutbound(t, again.Profiles[0].Config)
			if !proto.Equal(want.proxy, got.proxy) {
				t.Errorf("server changed:\n%v\nwas\n%v\nlink %s", got.proxy, want.proxy, links)
			}
			// The name a front is reached by is the SNI Xray sends when the profile names none.
			if stream := want.stream.(*internet.StreamConfig); strings.HasPrefix(test.name, "WebSocket") {
				settings, err := stream.SecuritySettings[0].GetInstance()
				if err != nil {
					t.Fatal(err)
				}
				settings.(*tls.Config).ServerName = "front.example"
				stream.SecuritySettings[0] = serial.ToTypedMessage(settings)
			}
			if !proto.Equal(want.stream, got.stream) {
				t.Errorf("transport changed:\n%v\nwas\n%v\nlink %s", got.stream, want.stream, links)
			}
			second, err := ConvertXrayJsonToShareLinks(again.Profiles[0].Config)
			second, _, _ = strings.Cut(second, "#")
			if links, _, _ := strings.Cut(links, "#"); err != nil || second != links {
				t.Errorf("the imported link exports as\n%s\nnot\n%s (%v)", second, links, err)
			}
		})
	}
}

// A link has no parameter for these settings. A server that sets one is shared as its profile, as a TUIC
// server with options is, instead of as a link that imports as another server; at its default it is
// still a link.
func TestShareLinkThatWouldLoseSettingsIsNotWritten(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	vless := func(top, stream string) string {
		return `{"protocol":"vless",` + top + `"settings":{"address":"203.0.113.7","port":443,"id":"` + id + `","encryption":"none"},"streamSettings":{` + stream + `}}`
	}
	overTLS := func(network, transport, tlsFields string) string {
		return vless("", `"network":"`+network+`",`+transport+`"security":"tls","tlsSettings":{"serverName":"example.com"`+tlsFields+`}`)
	}
	plain := func(network, transport string) string { return overTLS(network, transport, "") }
	user := func(protocol, settings string) string {
		return `{"protocol":"` + protocol + `","settings":{` + settings + `}}`
	}
	vlessUser := func(fields string) string {
		return `"vnext":[{"address":"203.0.113.7","port":443,"users":[{"id":"` + id + `","encryption":"none"` + fields + `}]}]`
	}
	for _, test := range []struct{ name, lossy, kept string }{
		{"mux", vless(`"mux":{"enabled":true,"concurrency":8},`, `"security":"tls"`), vless(`"mux":{"enabled":false,"concurrency":8},`, `"security":"tls"`)},
		{"target strategy", vless(`"targetStrategy":"ForceIPv4",`, `"security":"tls"`), vless(`"targetStrategy":"asIs",`, `"security":"tls"`)},
		{"TLS certificate", overTLS("raw", "", `,"certificates":[{"certificate":["fixture"],"usage":"verify"}]`), overTLS("raw", "", `,"certificates":[]`)},
		{"TLS without system roots", overTLS("raw", "", `,"disableSystemRoot":true`), overTLS("raw", "", `,"disableSystemRoot":false`)},
		{"TLS minimum version", overTLS("raw", "", `,"minVersion":"1.3"`), overTLS("raw", "", `,"minVersion":""`)},
		{"WebSocket header", plain("ws", `"wsSettings":{"path":"/w","host":"cdn.example","headers":{"User-Agent":"fixture"}},`),
			plain("ws", `"wsSettings":{"path":"/w","host":"cdn.example","headers":{"HOST":"cdn.example"}},`)},
		{"HTTPUpgrade header", plain("httpupgrade", `"httpupgradeSettings":{"path":"/u","host":"cdn.example","headers":{"X-Fixture":"1"}},`),
			plain("httpupgrade", `"httpupgradeSettings":{"path":"/u","host":"cdn.example","headers":{}},`)},
		{"gRPC user agent", plain("grpc", `"grpcSettings":{"serviceName":"s","user_agent":"fixture"},`), plain("grpc", `"grpcSettings":{"serviceName":"s","user_agent":""},`)},
		{"gRPC idle timeout", plain("grpc", `"grpcSettings":{"serviceName":"s","idle_timeout":60},`), plain("grpc", `"grpcSettings":{"serviceName":"s","idle_timeout":0},`)},
		{"mKCP MTU", plain("kcp", `"kcpSettings":{"mtu":1200},`), plain("kcp", `"kcpSettings":{"mtu":1350},`)},
		{"mKCP TTI", plain("mkcp", `"kcpSettings":{"tti":20},`), plain("mkcp", `"kcpSettings":{"tti":50},`)},
		{"HTTP header method", plain("raw", `"rawSettings":{"header":{"type":"http","request":{"method":"POST","path":["/"]}}},`),
			plain("raw", `"rawSettings":{"header":{"type":"http","request":{"method":"","path":["/"]}}},`)},
		{"HTTP header version", plain("tcp", `"tcpSettings":{"header":{"type":"http","request":{"version":"1.0"}}},`),
			plain("tcp", `"tcpSettings":{"header":{"type":"http","request":{"path":["/"]}}},`)},
		// Xray's own request line, said in full, is no setting of its own.
		{"HTTP header defaults", plain("raw", `"rawSettings":{"header":{"type":"http","request":{"version":"1.1","method":"POST","path":["/p"],"headers":{"Host":["cdn.example"]}}}},`),
			plain("raw", `"rawSettings":{"header":{"type":"http","request":{"version":"1.1","method":"GET","path":["/p"],"headers":{"Host":["cdn.example"]}}}},`)},
		{"HTTP request header", plain("raw", `"rawSettings":{"header":{"type":"http","request":{"path":["/p"],"headers":{"Host":["cdn.example"],"User-Agent":["curl/8"]}}}},`),
			plain("raw", `"rawSettings":{"header":{"type":"http","request":{"path":["/p"],"headers":{"Host":["cdn.example"]}},"response":{"status":"404"}}},`)},
		{"socket options", vless("", `"security":"tls","sockopt":{"mark":255}`), vless("", `"security":"tls","sockopt":{}`)},
		{"VLESS reverse", user("vless", `"address":"203.0.113.7","port":443,"id":"`+id+`","encryption":"none","reverse":{"tag":"r"}`),
			user("vless", `"address":"203.0.113.7","port":443,"id":"`+id+`","encryption":"none","reverse":null`)},
		{"VLESS user pre-connections", user("vless", vlessUser(`,"testpre":2`)), user("vless", vlessUser(`,"testpre":0`))},
		{"VLESS padding seeds", user("vless", `"address":"203.0.113.7","port":443,"id":"`+id+`","encryption":"none","testseed":[900,500,900,256]`),
			user("vless", `"address":"203.0.113.7","port":443,"id":"`+id+`","encryption":"none","testseed":[]`)},
		{"VMess user experiments", user("vmess", `"vnext":[{"address":"203.0.113.7","port":443,"users":[{"id":"`+id+`","experiments":"AuthenticatedLength"}]}]`),
			user("vmess", `"vnext":[{"address":"203.0.113.7","port":443,"users":[{"id":"`+id+`","experiments":""}]}]`)},
		// A user level or email only picks a local policy and labels statistics: exported Xray JSON carries them.
		{"VLESS level and email", "", user("vless", `"address":"203.0.113.7","port":443,"id":"`+id+`","encryption":"none","level":8,"email":"t@t.tt"`)},
		{"VLESS user level and email", "", user("vless", vlessUser(`,"email":"t@t.tt","level":8`))},
		{"VMess level and email", "", user("vmess", `"address":"203.0.113.7","port":443,"id":"`+id+`","level":8,"email":"t@t.tt"`)},
		{"VMess user level and email", "", user("vmess", `"vnext":[{"address":"203.0.113.7","port":443,"users":[{"id":"`+id+`","level":8,"email":"t@t.tt"}]}]`)},
		{"Trojan level and email", "", user("trojan", `"address":"203.0.113.8","port":443,"password":"pw","level":8,"email":"t@t.tt"`)},
		{"Trojan server level and email", "", user("trojan", `"servers":[{"address":"203.0.113.8","port":443,"password":"pw","level":8,"email":"t@t.tt"}]`)},
		{"Shadowsocks level and email", "", user("shadowsocks", `"address":"203.0.113.9","port":8388,"method":"aes-256-gcm","password":"pw","level":8,"email":"t@t.tt"`)},
		{"Shadowsocks server level and email", "", user("shadowsocks", `"servers":[{"address":"203.0.113.9","port":8388,"method":"aes-256-gcm","password":"pw","level":8,"email":"t@t.tt"}]`)},
		{"SOCKS level and email", "", user("socks", `"address":"203.0.113.10","port":1080,"user":"name","pass":"pw","level":8,"email":"t@t.tt"`)},
		{"SOCKS user level and email", "", user("socks", `"servers":[{"address":"203.0.113.10","port":1080,"users":[{"user":"name","pass":"pw","level":8,"email":"t@t.tt"}]}]`)},
		// The name a parsed link keeps in sendThrough.
		{"name in sendThrough", "", vless(`"sendThrough":"Home",`, `"security":"tls"`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.lossy != "" {
				lossy := `{"outbounds":[` + test.lossy + `]}`
				if !json.Valid([]byte(lossy)) {
					t.Fatalf("invalid fixture %s", lossy)
				}
				if exported, err := ConvertXrayJsonToShareLinks([]byte(lossy)); err != nil || exported != lossy {
					t.Errorf("exported %q (%v)", exported, err)
				}
				// Several outbounds are shared as links each, as before.
				both := `{"outbounds":[` + test.lossy + `,` + plain("raw", "") + `]}`
				if exported, err := ConvertXrayJsonToShareLinks([]byte(both)); err != nil || strings.Count(exported, "://") != 2 {
					t.Errorf("two outbounds exported %q (%v)", exported, err)
				}
			}
			kept := `{"outbounds":[` + test.kept + `]}`
			if !json.Valid([]byte(kept)) {
				t.Fatalf("invalid fixture %s", kept)
			}
			if exported, err := ConvertXrayJsonToShareLinks([]byte(kept)); err != nil || strings.HasPrefix(exported, "{") || !strings.Contains(exported, "://") {
				t.Errorf("the default exported %q (%v)", exported, err)
			}
		})
	}
}

// A link says all of what it imports as, so what it imported always shares as a link again.
func TestImportedLinksShareAsLinks(t *testing.T) {
	links := fixtureStrings(t, func(text string) bool {
		return strings.Contains(text, "://") && !strings.Contains(text, "\n") && !strings.HasPrefix(strings.TrimSpace(text), "{")
	})
	checked, shadowQUIC := 0, 0
	for _, link := range links {
		imported, err := ImportClientProfiles(link)
		if err != nil {
			continue
		}
		for _, profile := range imported.Profiles {
			var config conf.Config
			if profile.Format != "xrayProfile" || json.Unmarshal(profile.Config, &config) != nil || len(config.OutboundConfigs) != 1 {
				continue
			}
			// TUIC keeps its own rule; protocols without a link have none to lose.
			exported, err := ConvertXrayJsonToShareLinks(profile.Config)
			if config.OutboundConfigs[0].Protocol == "tuic" || err != nil {
				continue
			}
			checked++
			if config.OutboundConfigs[0].Protocol == "shadowquic" {
				shadowQUIC++
			}
			if strings.HasPrefix(exported, "{") {
				t.Errorf("%s shares as its profile:\n%s", link, profile.Config)
			}
		}
	}
	if checked < 20 {
		t.Fatalf("only %d links checked", checked)
	}
	// A ShadowQUIC link carries one MTU for both of the client's: what it imported has the two alike.
	if shadowQUIC == 0 {
		t.Fatal("no ShadowQUIC link checked")
	}
}

// Xray takes a WebSocket "Host" header for the host when the settings name none, as older profiles
// do: the link carries it as its host, and as the name a front is reached by when it names no SNI.
func TestShareLinkCarriesAHostHeader(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	for _, test := range []struct{ name, address, stream string }{
		{"without TLS", "10.0.0.7", `"network":"ws","wsSettings":{"path":"/w","headers":{"Host":"cdn.example"}}`},
		{"with an SNI", "front.example", `"network":"ws","security":"tls","tlsSettings":{"serverName":"sni.example"},"wsSettings":{"path":"/w","headers":{"host":"cdn.example"}}`},
		{"behind a front name without an SNI", "front.example", `"network":"websocket","security":"tls","wsSettings":{"path":"/w","headers":{"Host":"cdn.example"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := `{"outbounds":[{"protocol":"vless","settings":{"address":"` + test.address + `","port":443,"id":"` + id + `","encryption":"none"},"streamSettings":{` + test.stream + `}}]}`
			imported, err := ImportClientProfiles(source)
			if err != nil {
				t.Fatal(err)
			}
			links, err := ConvertXrayJsonToShareLinks(imported.Profiles[0].Config)
			if err != nil || strings.HasPrefix(links, "{") || !strings.Contains(links, "host=cdn.example") {
				t.Fatalf("exported %q (%v)", links, err)
			}
			again, err := ImportClientProfiles(links)
			if err != nil {
				t.Fatalf("the exported link does not import: %v\n%s", err, links)
			}
			want, got := builtOutbound(t, imported.Profiles[0].Config), builtOutbound(t, again.Profiles[0].Config)
			if stream := want.stream.(*internet.StreamConfig); strings.HasPrefix(test.name, "behind") {
				settings, err := stream.SecuritySettings[0].GetInstance()
				if err != nil {
					t.Fatal(err)
				}
				settings.(*tls.Config).ServerName = "front.example"
				stream.SecuritySettings[0] = serial.ToTypedMessage(settings)
			}
			if !proto.Equal(want.stream, got.stream) {
				t.Errorf("transport changed:\n%v\nwas\n%v\nlink %s", got.stream, want.stream, links)
			}
		})
	}
	// Xray refuses the header on HTTPUpgrade, where Clash files put the host; it means the host there too.
	links, err := ConvertXrayJsonToShareLinks([]byte(`{"outbounds":[{"protocol":"vless","settings":{"address":"10.0.0.7","port":443,"id":"` + id + `","encryption":"none"},
		"streamSettings":{"network":"httpupgrade","httpupgradeSettings":{"path":"/u","headers":{"Host":"cdn.example"}}}}]}`))
	if err != nil || !strings.Contains(links, "host=cdn.example") {
		t.Errorf("HTTPUpgrade exported %q (%v)", links, err)
	}
}

// A hop interval is seconds in 32 bits; a larger number is a broken link, not a small interval.
func TestHysteriaHopIntervalOutOfRangeIsRefused(t *testing.T) {
	for _, interval := range []string{"4294967326", "30-4294967326", "x"} {
		if imported, err := ImportClientProfiles("hy2://pw@203.0.113.9:443?sni=h.example&ports=20000-30000&hop-interval=" + interval + "#h"); err == nil {
			t.Errorf("hop-interval %s imported as %s", interval, imported.Profiles[0].Config)
		}
	}
}

// Other clients read the hop range and obfuscation from the link's own parameters, so a mask they can
// say stays in them; "fm" is for the rest.
func TestHysteriaLinkPrefersItsOwnParameters(t *testing.T) {
	imported, err := ImportClientProfiles("hy2://pw@203.0.113.9:443?sni=h.example&ports=20000-30000&hop-interval=30&obfs=salamander&obfs-password=secret#all")
	if err != nil {
		t.Fatal(err)
	}
	links, err := ConvertXrayJsonToShareLinks(imported.Profiles[0].Config)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(links)
	if err != nil {
		t.Fatal(err)
	}
	if query := link.Query(); query.Has("fm") || query.Get("ports") != "20000-30000" || query.Get("hop-interval") != "30" || query.Get("obfs-password") != "secret" {
		t.Errorf("link %s", links)
	}
}

// 3x-ui and v2rayN write Xray's gecko mask, salamander with a packet size range, as obfs=gecko with
// minPacketSize and maxPacketSize. Without the sizes the mask cannot be built.
func TestHysteria2GeckoObfuscationImportsAndShares(t *testing.T) {
	const base = "hysteria2://pw@203.0.113.9:443?sni=h.example&obfs=gecko&obfs-password=secret"
	imported, err := ImportClientProfiles(base + "&minPacketSize=512&maxPacketSize=1200#g")
	if err != nil {
		t.Fatal(err)
	}
	want := builtOutbound(t, imported.Profiles[0].Config)
	masks := want.stream.(*internet.StreamConfig).Udpmasks
	if len(masks) != 1 {
		t.Fatalf("%d masks: %s", len(masks), imported.Profiles[0].Config)
	}
	mask, err := masks[0].GetInstance()
	if gecko, ok := mask.(*salamander.GeckoConfig); err != nil || !ok || gecko.Password != "secret" || gecko.MinPacketSize != 512 || gecko.MaxPacketSize != 1200 {
		t.Fatalf("mask %T %v (%v)", mask, mask, err)
	}
	links, err := ConvertXrayJsonToShareLinks(imported.Profiles[0].Config)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(links)
	if err != nil {
		t.Fatal(err)
	}
	if query := link.Query(); query.Has("fm") || query.Get("obfs") != "gecko" || query.Get("obfs-password") != "secret" ||
		query.Get("minPacketSize") != "512" || query.Get("maxPacketSize") != "1200" {
		t.Errorf("link %s", links)
	}
	again, err := ImportClientProfiles(links)
	if err != nil {
		t.Fatalf("the exported link does not import: %v\n%s", err, links)
	}
	if got := builtOutbound(t, again.Profiles[0].Config); !proto.Equal(want.proxy, got.proxy) || !proto.Equal(want.stream, got.stream) {
		t.Errorf("server changed through %s", links)
	}
	for _, sizes := range []string{"", "&minPacketSize=512", "&maxPacketSize=1200", "&minPacketSize=0&maxPacketSize=1200",
		"&minPacketSize=1200&maxPacketSize=512", "&minPacketSize=512&maxPacketSize=2049", "&minPacketSize=a&maxPacketSize=1200"} {
		if result, err := ImportClientProfiles(base + sizes); err == nil {
			t.Errorf("%q imported as %s", sizes, result.Profiles[0].Config)
		}
	}
	if _, err := ImportClientProfiles("hysteria2://pw@203.0.113.9:443?obfs=gecko&minPacketSize=512&maxPacketSize=1200"); err == nil {
		t.Error("gecko without a password imported")
	}
}

// A server may well be named after its address; only a stored bind address is not a name.
func TestALinkNamedLikeAnAddressKeepsItsName(t *testing.T) {
	for _, name := range []string{"10.0.0.1", "origin"} {
		imported, err := ImportClientProfiles("vless://00000000-0000-4000-8000-000000000001@203.0.113.7:443?encryption=none&security=tls&sni=example.com#" + name)
		if err != nil || imported.Profiles[0].Name != name {
			t.Fatalf("imported %+v, %v", imported, err)
		}
	}
}

// Xray JSON names a local bind address in sendThrough. It is not the server's name.
func TestShareLinkIsNotNamedAfterABindAddress(t *testing.T) {
	for _, bind := range []string{"192.0.2.55", "2001:db8::/64", "origin"} {
		links, err := ConvertXrayJsonToShareLinks([]byte(`{"outbounds":[{"protocol":"vless","tag":"Home","sendThrough":"` + bind + `",
			"settings":{"address":"203.0.113.7","port":443,"id":"00000000-0000-4000-8000-000000000001","encryption":"none"}}]}`))
		if err != nil || !strings.HasSuffix(links, "#Home") {
			t.Errorf("sendThrough %s: link %s (%v)", bind, links, err)
		}
	}
}

// A link's "extra" can name a separate download server with XHTTP settings of its own. Stored with the
// unset ones as null, they were read back as an empty "extra" that replaced them.
func TestLinkExtraKeepsTheDownloadServersSettings(t *testing.T) {
	extra := `{"downloadSettings":{"address":"download.example.com","port":8443,"network":"xhttp","security":"tls",` +
		`"xhttpSettings":{"path":"/down","xPaddingBytes":"5-9","noGRPCHeader":true,"xmux":{"maxConcurrency":3}}}}`
	imported, err := ImportClientProfiles("vless://00000000-0000-4000-8000-000000000001@upload.example.com:443?encryption=none&security=tls" +
		"&sni=front.example.com&type=xhttp&path=%2Fup&extra=" + url.QueryEscape(extra) + "#fixture")
	if err != nil {
		t.Fatal(err)
	}
	stream := builtOutbound(t, imported.Profiles[0].Config).stream.(*internet.StreamConfig)
	upload, err := stream.TransportSettings[0].Settings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := upload.(*splithttp.Config).DownloadSettings.TransportSettings[0].Settings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	download := settings.(*splithttp.Config)
	if download.Path != "/down" || download.XPaddingBytes.GetFrom() != 5 || download.XPaddingBytes.GetTo() != 9 ||
		!download.NoGRPCHeader || download.Xmux.GetMaxConcurrency().GetTo() != 3 {
		t.Errorf("download server settings %v", download)
	}
}

type builtEndpoint struct{ proxy, stream proto.Message }

func builtOutbound(t *testing.T, raw []byte) builtEndpoint {
	t.Helper()
	var profile conf.Config
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	restoreNilRawMessages(reflect.ValueOf(&profile))
	built, err := profile.OutboundConfigs[0].Build()
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := built.ProxySettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	sender, err := built.SenderSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	stream := sender.(*proxyman.SenderConfig).StreamSettings
	if stream == nil {
		// A link always names its transport; no stream settings mean plain TCP.
		stream = &internet.StreamConfig{ProtocolName: "tcp"}
	}
	return builtEndpoint{proxy, stream}
}
