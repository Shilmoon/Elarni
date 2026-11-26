package share

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xtls/xray-core/infra/conf"
)

// FixWindowsReturn normalizes CRLF to LF (v2rayN exports on Windows).
func FixWindowsReturn(text string) string {
	return strings.ReplaceAll(text, "\r\n", "\n")
}

// decodeBase64Text decodes standard or URL-safe base64 (with optional missing padding).
func decodeBase64Text(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", base64.CorruptInputError(0)
	}
	if b, err := base64.StdEncoding.DecodeString(text); err == nil {
		return string(b), nil
	}
	// URL-safe alphabet, with padding
	if b, err := base64.URLEncoding.DecodeString(text); err == nil {
		return string(b), nil
	}
	// URL-safe raw + manual padding (legacy v2rayN); the padding skips line
	// breaks, as the decoder does
	s := strings.NewReplacer("-", "+", "_", "/", "\r", "", "\n", "").Replace(text)
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// https://github.com/XTLS/Xray-core/discussions/716

// ConvertShareLinksToXrayJson parses:
//   - a single Xray JSON object (starts with '{')
//   - plain v2rayN-style lines (vless/vmess/ss/socks/trojan/hy2…)
//   - one base64 blob that decodes to Xray JSON, share lines, or Clash YAML
//   - Clash / Clash.Meta YAML (proxies:)
func ConvertShareLinksToXrayJson(links string) (*conf.Config, error) {
	config, err := convertShareLinksToXrayJson(links, true)
	if err != nil {
		return nil, err
	}
	return filterBuildableOutbounds(config)
}

func convertShareLinksToXrayJson(links string, allowBase64 bool) (*conf.Config, error) {
	text := strings.TrimSpace(FixWindowsReturn(links))
	if text == "" {
		return nil, fmt.Errorf("unsupported share format")
	}
	if strings.HasPrefix(text, "{") {
		return parseXrayJSONConfig(text)
	}
	if hasShareSchemeLine(text) {
		return parsePlainShareLines(text)
	}
	if allowBase64 {
		decoded, err := decodeBase64Text(text)
		if err == nil {
			return convertShareLinksToXrayJson(decoded, false)
		}
	}
	if hasTopLevelClashProxiesKey(text) {
		return tryToParseClashYaml(text)
	}
	return nil, fmt.Errorf("unsupported share format")
}

func parseXrayJSONConfig(text string) (*conf.Config, error) {
	var xray *conf.Config
	if err := json.Unmarshal([]byte(text), &xray); err != nil {
		return nil, err
	}
	if len(xray.OutboundConfigs) == 0 {
		return nil, fmt.Errorf("no valid outbounds")
	}
	return xray, nil
}

var shareSchemes = []string{
	"vless://", "vmess://", "socks://", "socks5://", "http://", "https://", "ss://", "trojan://",
	"hysteria2://", "hy2://", "tuic://", "sq://", "shadowquic://",
}

func hasShareSchemeLine(text string) bool {
	found := false
	forEachLine(text, func(raw string) bool {
		line := strings.TrimSpace(raw)
		for _, p := range shareSchemes {
			if strings.HasPrefix(line, p) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func hasTopLevelClashProxiesKey(text string) bool {
	found := false
	forEachLine(text, func(raw string) bool {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "---" || strings.HasPrefix(trimmed, "#") {
			return true
		}
		if strings.HasPrefix(line, "proxies:") {
			found = true
			return false
		}
		return true
	})
	return found
}

func forEachLine(text string, visit func(string) bool) {
	for {
		line, rest, ok := strings.Cut(text, "\n")
		if !visit(line) {
			return
		}
		if !ok {
			return
		}
		text = rest
	}
}

func parsePlainShareLines(text string) (*conf.Config, error) {
	outbounds := make([]conf.OutboundDetourConfig, 0)
	forEachLine(text, func(raw string) bool {
		line := strings.TrimSpace(raw)
		if line == "" {
			return true
		}
		u, err := parseClientLink(line)
		if err != nil {
			return true
		}
		sl := xrayShareLink{link: u, rawText: line}
		ob, err := sl.outbound()
		if err != nil {
			return true
		}
		outbounds = append(outbounds, *ob)
		return true
	})
	if len(outbounds) == 0 {
		return nil, fmt.Errorf("no valid outbound found")
	}
	return &conf.Config{OutboundConfigs: outbounds}, nil
}

type xrayShareLink struct {
	link    *url.URL
	rawText string
}

func (proxy xrayShareLink) outbound() (*conf.OutboundDetourConfig, error) {
	switch proxy.link.Scheme {
	case "ss":
		return proxy.shadowsocksOutbound()
	case "vmess":
		return proxy.vmessOutbound()
	case "vless":
		return proxy.vlessOutbound()
	case "socks", "socks5":
		return proxy.socksOutbound()
	case "http", "https":
		return proxy.httpOutbound()
	case "trojan":
		return proxy.trojanOutbound()
	case "hysteria2", "hy2":
		return proxy.hysteriaOutbound()
	case "tuic":
		return proxy.tuicOutbound()
	case "sq", "shadowquic":
		return proxy.shadowQUICOutbound()
	default:
		return nil, fmt.Errorf("unsupported client link scheme")
	}
}

func (proxy xrayShareLink) shadowsocksOutbound() (*conf.OutboundDetourConfig, error) {
	outbound := &conf.OutboundDetourConfig{}
	outbound.Protocol = "shadowsocks"
	setOutboundName(outbound, proxy.link.Fragment)

	settings := &conf.ShadowsocksClientConfig{}
	settings.Address = parseAddress(proxy.link.Hostname())
	port, err := clientPort(proxy.link)
	if err != nil {
		return nil, err
	}
	settings.Port = port

	cipher, password, err := parseShadowsocksUserInfo(proxy.link.User)
	if err != nil {
		return nil, err
	}
	settings.Cipher = cipher
	settings.Password = password

	settingsRawMessage, err := convertJsonToRawMessage(settings)
	if err != nil {
		return nil, err
	}
	outbound.Settings = &settingsRawMessage

	if plugin := proxy.link.Query().Get("plugin"); plugin != "" {
		stream, err := shadowsocksPluginStream(plugin)
		if err != nil {
			return nil, err
		}
		outbound.StreamSetting = stream
		return outbound, nil
	}
	streamSettings, err := proxy.streamSettings(proxy.link)
	if err != nil {
		return nil, err
	}
	outbound.StreamSetting = streamSettings
	return outbound, nil
}

// shadowsocksPluginStream maps SIP002 "name;key=value;flag" options. Of v2ray-plugin only the
// websocket mode exists in Xray, as on the Clash import path, and of simple-obfs only the HTTP mode.
func shadowsocksPluginStream(plugin string) (*conf.StreamConfig, error) {
	options := strings.Split(plugin, ";")
	if options[0] == "obfs-local" || options[0] == "simple-obfs" {
		return simpleObfsStream(options[1:])
	}
	if options[0] != "v2ray-plugin" {
		return nil, fmt.Errorf("unsupported shadowsocks plugin")
	}
	ws := &conf.WebSocketConfig{}
	stream := &conf.StreamConfig{Network: new(conf.TransportProtocol("websocket")), Security: "none", WSSettings: ws}
	for _, option := range options[1:] {
		key, value, _ := strings.Cut(option, "=")
		switch key {
		case "mode":
			if value != "websocket" {
				return nil, fmt.Errorf("unsupported shadowsocks plugin mode")
			}
		case "host":
			ws.Host = value
		case "path":
			ws.Path = value
		case "tls":
			stream.Security = "tls"
		case "mux", "loglevel", "fastOpen":
			// Client-side tuning; v2ray-plugin servers accept plain websocket streams.
		default:
			return nil, fmt.Errorf("unsupported shadowsocks plugin option")
		}
	}
	if stream.Security == "tls" {
		stream.TLSSettings = &conf.TLSConfig{ServerName: ws.Host}
	}
	return stream, nil
}

// simpleObfsStream maps simple-obfs options. Its HTTP mode is a raw stream behind one HTTP request
// header, which is what 3x-ui writes for a Shadowsocks inbound with Xray's HTTP header. Its TLS mode
// fakes a TLS session Xray has no transport for.
func simpleObfsStream(options []string) (*conf.StreamConfig, error) {
	fields := shareTransportFields{Network: "raw"}
	for _, option := range options {
		key, value, _ := strings.Cut(option, "=")
		switch key {
		case "obfs":
			switch value {
			case "http":
				fields.HeaderType = "http"
			case "tls":
				return nil, fmt.Errorf("simple-obfs TLS mode has no Xray equivalent")
			default:
				return nil, fmt.Errorf("unsupported shadowsocks plugin mode")
			}
		case "obfs-host":
			fields.Host = value
		case "obfs-uri":
			fields.Path = value
		default:
			return nil, fmt.Errorf("unsupported shadowsocks plugin option")
		}
	}
	if fields.HeaderType == "" {
		return nil, fmt.Errorf("unsupported shadowsocks plugin mode")
	}
	stream, err := buildStreamFromTransportFields(fields)
	if err != nil {
		return nil, err
	}
	stream.Security = "none"
	return stream, nil
}

func parseShadowsocksUserInfo(user *url.Userinfo) (string, string, error) {
	if user == nil {
		return "", "", fmt.Errorf("missing shadowsocks user info")
	}

	// SIP002 plain user info uses method:password. net/url decodes the
	// percent-encoded method and password independently.
	if password, ok := user.Password(); ok {
		cipher := user.Username()
		if cipher == "" {
			return "", "", fmt.Errorf("missing shadowsocks cipher")
		}
		return cipher, password, nil
	}

	// Username is the percent-decoded text; standard base64 escapes '/' and '='.
	decoded, err := decodeBase64Text(user.Username())
	if err != nil {
		return "", "", err
	}
	cipher, password, ok := strings.Cut(decoded, ":")
	if !ok || cipher == "" {
		return "", "", fmt.Errorf("unsupported shadowsocks user info")
	}
	return cipher, password, nil
}

func (proxy xrayShareLink) vmessOutbound() (*conf.OutboundDetourConfig, error) {
	text := strings.ReplaceAll(proxy.rawText, "vmess://", "")
	if base64Text, err := decodeBase64Text(text); err == nil {
		return parseVMessQrCode(base64Text)
	}

	outbound := &conf.OutboundDetourConfig{}
	outbound.Protocol = "vmess"
	setOutboundName(outbound, proxy.link.Fragment)
	query := proxy.link.Query()

	settings := conf.VMessOutboundConfig{}
	settings.Address = parseAddress(proxy.link.Hostname())
	port, err := clientPort(proxy.link)
	if err != nil {
		return nil, err
	}
	settings.Port = port

	id, err := url.PathUnescape(proxy.link.User.String())
	if err != nil {
		return nil, err
	}
	settings.ID = id
	if security := query.Get("encryption"); security != "" {
		settings.Security = security
	}

	settingsRawMessage, err := convertJsonToRawMessage(settings)
	if err != nil {
		return nil, err
	}
	outbound.Settings = &settingsRawMessage

	streamSettings, err := proxy.streamSettings(proxy.link)
	if err != nil {
		return nil, err
	}
	outbound.StreamSetting = streamSettings
	return outbound, nil
}

func (proxy xrayShareLink) vlessOutbound() (*conf.OutboundDetourConfig, error) {
	outbound := &conf.OutboundDetourConfig{}
	outbound.Protocol = "vless"
	setOutboundName(outbound, proxy.link.Fragment)
	query := proxy.link.Query()

	settings := &conf.VLessOutboundConfig{}
	settings.Address = parseAddress(proxy.link.Hostname())
	port, err := clientPort(proxy.link)
	if err != nil {
		return nil, err
	}
	settings.Port = port

	id, err := url.PathUnescape(proxy.link.User.String())
	if err != nil {
		return nil, err
	}
	settings.Id = id
	if flow := query.Get("flow"); flow != "" {
		settings.Flow = flow
	}
	if enc := query.Get("encryption"); enc != "" {
		settings.Encryption = enc
	} else {
		settings.Encryption = "none"
	}

	settingsRawMessage, err := convertJsonToRawMessage(settings)
	if err != nil {
		return nil, err
	}
	outbound.Settings = &settingsRawMessage

	streamSettings, err := proxy.streamSettings(proxy.link)
	if err != nil {
		return nil, err
	}
	outbound.StreamSetting = streamSettings
	return outbound, nil
}

func (proxy xrayShareLink) socksOutbound() (*conf.OutboundDetourConfig, error) {
	outbound := &conf.OutboundDetourConfig{}
	outbound.Protocol = "socks"
	setOutboundName(outbound, proxy.link.Fragment)

	settings := &conf.SocksClientConfig{}
	settings.Address = parseAddress(proxy.link.Hostname())
	port, err := clientPort(proxy.link)
	if err != nil {
		return nil, err
	}
	settings.Port = port

	settings.Username, settings.Password = proxyCredentials(proxy.link)

	settingsRawMessage, err := convertJsonToRawMessage(settings)
	if err != nil {
		return nil, err
	}
	outbound.Settings = &settingsRawMessage

	streamSettings, err := proxy.streamSettings(proxy.link)
	if err != nil {
		return nil, err
	}
	outbound.StreamSetting = streamSettings
	return outbound, nil
}

func (proxy xrayShareLink) trojanOutbound() (*conf.OutboundDetourConfig, error) {
	outbound := &conf.OutboundDetourConfig{}
	outbound.Protocol = "trojan"
	setOutboundName(outbound, proxy.link.Fragment)

	settings := &conf.TrojanClientConfig{}
	settings.Address = parseAddress(proxy.link.Hostname())
	port, err := clientPort(proxy.link)
	if err != nil {
		return nil, err
	}
	settings.Port = port

	password, err := url.PathUnescape(proxy.link.User.String())
	if err != nil {
		return nil, err
	}
	settings.Password = password

	settingsRawMessage, err := convertJsonToRawMessage(settings)
	if err != nil {
		return nil, err
	}
	outbound.Settings = &settingsRawMessage

	streamSettings, err := proxy.streamSettings(proxy.link)
	if err != nil {
		return nil, err
	}
	outbound.StreamSetting = streamSettings
	return outbound, nil
}

func (proxy xrayShareLink) hysteriaOutbound() (*conf.OutboundDetourConfig, error) {
	outbound := &conf.OutboundDetourConfig{}
	outbound.Protocol = "hysteria"
	setOutboundName(outbound, proxy.link.Fragment)

	settings := &conf.HysteriaClientConfig{}
	settings.Version = 2
	settings.Address = parseAddress(proxy.link.Hostname())
	port, err := clientPort(proxy.link)
	if err != nil {
		return nil, err
	}
	settings.Port = port

	settingsRawMessage, err := convertJsonToRawMessage(settings)
	if err != nil {
		return nil, err
	}
	outbound.Settings = &settingsRawMessage

	streamSettings := &conf.StreamConfig{}
	streamSettings.Network = new(conf.TransportProtocol("hysteria"))

	hysteriaSettings := &conf.HysteriaConfig{}
	hysteriaSettings.Version = 2
	auth, err := url.PathUnescape(proxy.link.User.String())
	if err != nil {
		return nil, err
	}
	hysteriaSettings.Auth = auth
	streamSettings.HysteriaSettings = hysteriaSettings

	query := proxy.link.Query()
	// v2rayN names the hop range and interval mport and mportHopInt, PasarGuard the range mports.
	for key, aliases := range map[string][]string{"ports": {"mport", "mports"}, "hop-interval": {"mportHopInt"}} {
		for _, alias := range aliases {
			if !query.Has(key) && query.Has(alias) {
				query.Set(key, query.Get(alias))
			}
		}
	}
	// The official URI names no gecko packet sizes: Hysteria's own client defaults stand for them.
	if query.Get("obfs") == "gecko" {
		for key, value := range map[string]string{"minPacketSize": "512", "maxPacketSize": "1200"} {
			if !query.Has(key) {
				query.Set(key, value)
			}
		}
	}
	var hopInterval conf.Int32Range
	if hopStr := query.Get("hop-interval"); hopStr != "" {
		// One number of seconds, or a range of them as Clash writes it.
		left, right, pErr := conf.ParseRangeString(hopStr)
		if pErr != nil {
			return nil, pErr
		}
		if int(int32(left)) != left || int(int32(right)) != right {
			return nil, fmt.Errorf("hop-interval out of range")
		}
		hopInterval = conf.Int32Range{Left: int32(left), Right: int32(right), From: int32(min(left, right)), To: int32(max(left, right))}
	}
	finalMask, mErr := buildHy2FinalMask(
		query.Get("up"), query.Get("down"), query.Get("ports"),
		hopInterval, query.Get("obfs"), query.Get("obfs-password"),
		query.Get("minPacketSize"), query.Get("maxPacketSize"),
	)
	if mErr != nil {
		return nil, mErr
	}
	streamSettings.FinalMask = finalMask
	if rawMask := query.Get("fm"); rawMask != "" {
		var explicit map[string]any
		if err := json.Unmarshal([]byte(rawMask), &explicit); err != nil || explicit == nil {
			return nil, errHy2MaskInvalid
		}
		if err := moveLegacyUDPHop(explicit); err != nil {
			return nil, err
		}
		if finalMask != nil {
			if err := mergeHy2Masks(explicit, finalMask); err != nil {
				return nil, err
			}
		}
		encoded, _ := json.Marshal(explicit)
		if err := json.Unmarshal(encoded, &streamSettings.FinalMask); err != nil {
			return nil, errHy2MaskInvalid
		}
	}

	if err := proxy.parseSecurityFromURL(proxy.link, streamSettings); err != nil {
		return nil, err
	}
	outbound.StreamSetting = streamSettings
	return outbound, nil
}

func proxyCredentials(link *url.URL) (string, string) {
	if link.User == nil {
		return "", ""
	}
	if password, ok := link.User.Password(); ok {
		return link.User.Username(), password
	}
	if decoded, err := decodeBase64Text(link.User.Username()); err == nil {
		if username, password, ok := strings.Cut(decoded, ":"); ok {
			return username, password
		}
	}
	return link.User.Username(), ""
}

func (proxy xrayShareLink) httpOutbound() (*conf.OutboundDetourConfig, error) {
	// An HTTP proxy has no path, so a link with one is a web page, not a server.
	if proxy.link.Path != "" && proxy.link.Path != "/" {
		return nil, fmt.Errorf("web page link is not an HTTP proxy")
	}
	port, err := clientPort(proxy.link)
	if err != nil {
		return nil, err
	}
	username, password := proxyCredentials(proxy.link)
	settings, err := convertJsonToRawMessage(&conf.HTTPClientConfig{
		Address: parseAddress(proxy.link.Hostname()), Port: port, Username: username, Password: password,
	})
	if err != nil {
		return nil, err
	}
	stream, err := proxy.streamSettings(proxy.link)
	if err != nil {
		return nil, err
	}
	outbound := &conf.OutboundDetourConfig{Protocol: "http", Settings: &settings, StreamSetting: stream}
	setOutboundName(outbound, proxy.link.Fragment)
	return outbound, nil
}

func parseClientLink(line string) (*url.URL, error) {
	if link, ok := legacyShadowsocksLink(line); ok {
		return link, nil
	}
	link, err := parseShareURL(line)
	if err != nil {
		return nil, err
	}
	if name := withoutServerDescription(link.Fragment); name != link.Fragment {
		link.Fragment, link.RawFragment = name, ""
	}
	return link, nil
}

// withoutServerDescription is a link's name without the server description 3x-ui appends to it
// as "?serverDescription=" and Base64, which Happ shows apart and other clients do not read.
func withoutServerDescription(name string) string {
	const marker = "?serverDescription="
	if index := strings.LastIndex(name, marker); index >= 0 {
		if text, err := decodeBase64Text(name[index+len(marker):]); err == nil && utf8.ValidString(text) {
			return name[:index]
		}
	}
	return name
}

// The Hysteria 2 URI may list hopping ports in the authority (host:443,5000-6000),
// which net/url refuses as a port; dial the first one and hop over the list.
func parseShareURL(line string) (*url.URL, error) {
	link, err := url.Parse(line)
	scheme, rest, _ := strings.Cut(line, "://")
	if err == nil || !(strings.EqualFold(scheme, "hy2") || strings.EqualFold(scheme, "hysteria2")) {
		return keepSemicolons(link), err
	}
	end := strings.IndexAny(rest, "/?#")
	if end == -1 {
		end = len(rest)
	}
	colon := strings.LastIndexByte(rest[:end], ':')
	if colon == -1 || !strings.ContainsAny(rest[colon+1:end], ",-") {
		return nil, err
	}
	ports := rest[colon+1 : end]
	first, _, _ := strings.Cut(strings.Split(ports, ",")[0], "-")
	if link, err = url.Parse(scheme + "://" + rest[:colon+1] + first + rest[end:]); err != nil {
		return nil, err
	}
	if query := keepSemicolons(link).Query(); query.Get("ports") == "" {
		query.Set("ports", ports)
		link.RawQuery = query.Encode()
	}
	return link, nil
}

// legacyShadowsocksLink reads the legacy Shadowsocks URI, which encodes the whole authority:
// ss://base64(method:password@host:port)#name. A SIP002 link has its "@" outside the base64.
func legacyShadowsocksLink(line string) (*url.URL, bool) {
	if len(line) < 5 || !strings.EqualFold(line[:5], "ss://") {
		return nil, false
	}
	payload, fragment, _ := strings.Cut(line[5:], "#")
	if strings.ContainsAny(payload, "@?") {
		return nil, false
	}
	decoded, err := decodeBase64Text(payload)
	if err != nil || !utf8.ValidString(decoded) || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
		return nil, false
	}
	// The password may hold "@" and ":"; the server follows the last "@", the method has none.
	at := strings.LastIndexByte(decoded, '@')
	if at < 0 {
		return nil, false
	}
	method, password, ok := strings.Cut(decoded[:at], ":")
	host, port, err := net.SplitHostPort(decoded[at+1:])
	if !ok || method == "" || err != nil || host == "" || port == "" {
		return nil, false
	}
	name, err := url.PathUnescape(fragment)
	if err != nil {
		return nil, false
	}
	return &url.URL{Scheme: "ss", User: url.UserPassword(method, password), Host: net.JoinHostPort(host, port), Fragment: withoutServerDescription(name)}, true
}

// Many clients leave ';' bare in SIP002 plugin options and paths, and net/url drops
// every query parameter that holds one.
func keepSemicolons(link *url.URL) *url.URL {
	if link != nil {
		link.RawQuery = strings.ReplaceAll(link.RawQuery, ";", "%3B")
	}
	return link
}

func clientPort(link *url.URL) (uint16, error) {
	// An unbracketed IPv6 host would split at its last colon into another host and port.
	if link.Hostname() == "" || len(link.Hostname()) > 253 || (strings.Contains(link.Hostname(), ":") && !strings.HasPrefix(link.Host, "[")) {
		return 0, fmt.Errorf("invalid endpoint host")
	}
	text := link.Port()
	if text == "" {
		switch link.Scheme {
		case "trojan", "hy2", "hysteria2", "https", "sq", "shadowquic":
			text = "443"
		case "http":
			text = "80"
		default:
			return 0, fmt.Errorf("missing endpoint port")
		}
	}
	port, err := strconv.ParseUint(text, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("invalid endpoint port")
	}
	return uint16(port), nil
}
