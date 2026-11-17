package share

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/infra/conf"
	"gopkg.in/yaml.v3"
)

// The sq:// link format: https://github.com/RealBikiniBottom/QuicProxy/discussions/2

// shadowQUICSettings is conf.ShadowQUICClientConfig as a profile stores it. What the source leaves out stays out
// and means the ShadowQUIC client's own value: Xray refuses a null, and a zero would be a value of its own.
type shadowQUICSettings struct {
	Address           *conf.Address `json:"address"`
	Port              uint16        `json:"port"`
	Username          string        `json:"username"`
	Password          string        `json:"password"`
	ServerName        string        `json:"serverName"`
	ALPN              []string      `json:"alpn,omitempty"`
	CongestionControl string        `json:"congestionControl,omitempty"`
	ZeroRTT           bool          `json:"zeroRtt"`
	UDPOverStream     bool          `json:"udpOverStream"`
	InitialMTU        uint16        `json:"initialMtu,omitempty"`
	MinMTU            uint16        `json:"minMtu,omitempty"`
}

// errShadowQUICLinkLoses is a server that sets what a link has no parameter for.
var errShadowQUICLinkLoses = errors.New("ShadowQUIC options require sharing the complete profile")

func (proxy xrayShareLink) shadowQUICOutbound() (*conf.OutboundDetourConfig, error) {
	u := proxy.link
	if u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.User == nil {
		return nil, fmt.Errorf("invalid ShadowQUIC URI")
	}
	username := u.User.Username()
	password, _ := u.User.Password()
	if username == "" || password == "" {
		return nil, fmt.Errorf("ShadowQUIC requires a username and a password")
	}
	port, err := clientPort(u)
	if err != nil {
		return nil, fmt.Errorf("invalid ShadowQUIC server port")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid ShadowQUIC URI parameters")
	}
	for key, values := range query {
		switch key {
		case "sni", "udp_mode", "zero_rtt", "mtu", "alpn", "congestion":
		default:
			return nil, fmt.Errorf("unsupported ShadowQUIC URI parameter")
		}
		if len(values) != 1 {
			return nil, fmt.Errorf("duplicate ShadowQUIC URI parameter")
		}
	}
	settings := &shadowQUICSettings{Address: parseAddress(u.Hostname()), Port: port, Username: username, Password: password,
		ServerName: query.Get("sni"), UDPOverStream: true}
	if settings.ServerName == "" {
		return nil, fmt.Errorf("ShadowQUIC requires an SNI")
	}
	if mode, ok := query["udp_mode"]; ok {
		switch mode[0] {
		case "stream":
		case "datagram":
			settings.UDPOverStream = false
		default:
			return nil, fmt.Errorf("invalid ShadowQUIC UDP mode")
		}
	}
	// Any text turns 0-RTT on, as the format says ("left empty is off"); 0 and false are read as what they say.
	if value, ok := query["zero_rtt"]; ok {
		settings.ZeroRTT = value[0] != "" && value[0] != "0" && !strings.EqualFold(value[0], "false")
	}
	// One MTU for both the first and the smallest; QUIC packets are 1200 bytes at least.
	if value, ok := query["mtu"]; ok {
		mtu, err := strconv.ParseUint(value[0], 10, 16)
		if err != nil || mtu < 1200 {
			return nil, fmt.Errorf("invalid ShadowQUIC MTU")
		}
		settings.InitialMTU, settings.MinMTU = uint16(mtu), uint16(mtu)
	}
	if alpn := query.Get("alpn"); alpn != "" {
		settings.ALPN = strings.Split(alpn, ",")
	}
	if congestion, ok := query["congestion"]; ok {
		settings.CongestionControl = shadowQUICCongestion(congestion[0])
	}
	return shadowQUICOutbound(settings, u.Fragment)
}

// shadowQUICCongestion is the ShadowQUIC client's name for a congestion control. Another client's algorithm
// (BBRv3, Brutal, Copa) becomes the client's own default, BBR.
func shadowQUICCongestion(name string) string {
	switch name {
	case "bbr", "cubic":
		return name
	case "new_reno", "new-reno", "reno":
		return "new-reno"
	default:
		return "bbr"
	}
}

func shadowQUICOutbound(settings *shadowQUICSettings, name string) (*conf.OutboundDetourConfig, error) {
	raw, err := convertJsonToRawMessage(settings)
	if err != nil {
		return nil, err
	}
	outbound := &conf.OutboundDetourConfig{Protocol: "shadowquic", Settings: &raw}
	// Through Xray's own parser, which a profile goes through when it runs.
	if _, err := outbound.Build(); err != nil {
		return nil, err
	}
	// As for TUIC, the display name goes in the tag: Xray refuses sendThrough for this protocol.
	outbound.Tag = name
	return outbound, nil
}

// clashShadowQUIC holds the keys only a ShadowQUIC proxy has, which the other types do not read.
type clashShadowQUIC struct {
	ServerName    string  `yaml:"server-name"`
	UDPOverStream bool    `yaml:"udp-over-stream"`
	ZeroRTT       bool    `yaml:"zero-rtt"`
	Congestion    *string `yaml:"congestion-controller"`
}

// shadowQUICClashFields are the keys a ShadowQUIC proxy may have. Mihomo's tuning, from quic-versions on, has
// nothing in the ShadowQUIC client to go to and is left out.
var shadowQUICClashFields = map[string]bool{"name": true, "type": true, "server": true, "port": true, "username": true, "password": true,
	"sni": true, "server-name": true, "alpn": true, "udp-over-stream": true, "zero-rtt": true, "congestion-controller": true,
	"udp": true, "tls": true, "skip-cert-verify": true, "ip-version": true,
	"quic-versions": true, "keep-alive-interval": true, "up": true, "down": true, "cwnd": true, "bbr-profile": true, "max-datagram-frame-size": true,
	"max-open-streams": true, "recv-window-conn": true, "recv-window": true, "disable-mtu-discovery": true}

// ShadowQUIC is as strict as TUIC, in the legacy converter too: a key it cannot carry refuses the proxy.
func (proxy *ClashProxy) decodeShadowQUIC(node *yaml.Node) error {
	var fields map[string]any
	if err := node.Decode(&fields); err != nil {
		return err
	}
	for key := range fields {
		if !shadowQUICClashFields[key] {
			return fmt.Errorf("unsupported ShadowQUIC Clash field")
		}
	}
	for _, key := range []string{"username", "password"} {
		if _, ok := fields[key].(string); !ok {
			return fmt.Errorf("ShadowQUIC requires text username and password")
		}
	}
	for _, key := range []string{"name", "server", "sni", "server-name", "congestion-controller", "ip-version"} {
		if value, exists := fields[key]; exists {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("invalid ShadowQUIC text field")
			}
		}
	}
	for _, key := range []string{"udp-over-stream", "zero-rtt", "udp", "tls", "skip-cert-verify"} {
		if value, exists := fields[key]; exists {
			flag, ok := value.(bool)
			// The client always relays UDP over TLS and has no switch to skip verification: the common keys
			// only at the values that ask for nothing else, as for TUIC.
			if !ok || ((key == "udp" || key == "tls") && !flag) || (key == "skip-cert-verify" && flag) {
				return fmt.Errorf("unsupported ShadowQUIC boolean field")
			}
		}
	}
	// Xray refuses a target strategy for this protocol; dual stack is what it does without one.
	if version, exists := fields["ip-version"]; exists && version != "dual" {
		return fmt.Errorf("unsupported ShadowQUIC IP version")
	}
	var options clashShadowQUIC
	if err := node.Decode(&options); err != nil {
		return err
	}
	if proxy.Sni != "" && options.ServerName != "" && proxy.Sni != options.ServerName {
		return fmt.Errorf("conflicting ShadowQUIC server names")
	}
	if proxy.Sni == "" && options.ServerName == "" {
		return fmt.Errorf("ShadowQUIC requires an SNI")
	}
	proxy.ShadowQUIC = &options
	return nil
}

func (proxy ClashProxy) shadowQUICOutbound() (*conf.OutboundDetourConfig, error) {
	options := proxy.ShadowQUIC
	if options == nil {
		return nil, fmt.Errorf("invalid ShadowQUIC Clash proxy")
	}
	settings := &shadowQUICSettings{Address: parseAddress(proxy.Server), Port: proxy.Port, Username: proxy.Username, Password: proxy.Password,
		ServerName: proxy.Sni, ALPN: proxy.Alpn, ZeroRTT: options.ZeroRTT, UDPOverStream: options.UDPOverStream}
	if settings.ServerName == "" {
		settings.ServerName = options.ServerName
	}
	if options.Congestion != nil {
		settings.CongestionControl = shadowQUICCongestion(*options.Congestion)
	}
	return shadowQUICOutbound(settings, proxy.Name)
}

func shadowQUICLink(proxy conf.OutboundDetourConfig, link *url.URL) error {
	settings, err := decodeOutboundSettings[conf.ShadowQUICClientConfig](proxy)
	if err != nil {
		return err
	}
	if _, err := proxy.Build(); err != nil {
		return err
	}
	if settings.InitialMTU != settings.MinMTU {
		return errShadowQUICLinkLoses
	}
	for _, alpn := range settings.ALPN {
		if strings.Contains(alpn, ",") {
			return errShadowQUICLinkLoses
		}
	}
	var host string
	if settings.Address.Family().IsIP() {
		host = settings.Address.IP().String()
	} else {
		host = settings.Address.Domain()
	}
	link.Scheme, link.Host, link.User, link.Fragment = "sq", net.JoinHostPort(host, strconv.Itoa(int(settings.Port))), url.UserPassword(settings.Username, settings.Password), shareName(proxy)
	query := url.Values{}
	query.Set("sni", settings.ServerName)
	if settings.UDPOverStream {
		query.Set("udp_mode", "stream")
	} else {
		query.Set("udp_mode", "datagram")
	}
	if settings.ZeroRTT {
		query.Set("zero_rtt", "1")
	}
	if settings.InitialMTU != 0 {
		query.Set("mtu", strconv.Itoa(int(settings.InitialMTU)))
	}
	if len(settings.ALPN) > 0 {
		query.Set("alpn", strings.Join(settings.ALPN, ","))
	}
	switch settings.CongestionControl {
	case "":
	case "new-reno":
		query.Set("congestion", "new_reno")
	default:
		query.Set("congestion", settings.CongestionControl)
	}
	link.RawQuery = query.Encode()
	return nil
}
