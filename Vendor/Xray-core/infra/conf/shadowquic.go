package conf

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/proxy/shadowquic"
	"google.golang.org/protobuf/proto"
)

// ShadowQUICClientConfig is the "shadowquic" outbound of Elarni. The connection to the
// server belongs to the ShadowQUIC client, so the outbound takes no stream settings.
type ShadowQUICClientConfig struct {
	Address           *Address `json:"address"`
	Port              uint16   `json:"port"`
	Username          string   `json:"username"`
	Password          string   `json:"password"`
	ServerName        string   `json:"serverName"`
	ALPN              []string `json:"alpn"`
	CongestionControl string   `json:"congestionControl"`
	ZeroRTT           bool     `json:"zeroRtt"`
	UDPOverStream     bool     `json:"udpOverStream"`
	InitialMTU        uint16   `json:"initialMtu"`
	MinMTU            uint16   `json:"minMtu"`
}

// The values the ShadowQUIC client uses for an MTU the settings leave out, and the
// smallest packet size QUIC allows.
const (
	shadowQUICInitialMTU = 1300
	shadowQUICMinMTU     = 1290
	quicSmallestMTU      = 1200
)

func (c *ShadowQUICClientConfig) UnmarshalJSON(data []byte) error {
	if uniqueTUICJSON(json.NewDecoder(bytes.NewReader(data))) != nil {
		return errors.New("invalid ShadowQUIC settings")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return errors.New("invalid ShadowQUIC settings")
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("ShadowQUIC settings require explicit typed values")
		}
	}
	type config ShadowQUICClientConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode((*config)(c)); err != nil {
		return errors.New("unsupported or invalid ShadowQUIC setting")
	}
	return nil
}

func (c *ShadowQUICClientConfig) Build() (proto.Message, error) {
	if c.Address == nil || c.Address.Address == nil || c.Address.String() == "" || c.Port == 0 {
		return nil, errors.New("ShadowQUIC requires a server address and port")
	}
	if c.Address.Family().IsDomain() && !validShadowQUICName(c.Address.Domain()) {
		return nil, errors.New("invalid ShadowQUIC server address")
	}
	if c.Username == "" || c.Password == "" {
		return nil, errors.New("ShadowQUIC requires a username and a password")
	}
	if !validShadowQUICName(c.ServerName) {
		return nil, errors.New("ShadowQUIC requires a server name")
	}
	for _, alpn := range c.ALPN {
		if len(alpn) == 0 || len(alpn) > 255 {
			return nil, errors.New("invalid ShadowQUIC ALPN")
		}
	}
	switch c.CongestionControl {
	case "", "bbr", "cubic", "new-reno":
	default:
		return nil, errors.New("unsupported ShadowQUIC congestion control")
	}
	initial, smallest := c.InitialMTU, c.MinMTU
	if initial == 0 {
		initial = shadowQUICInitialMTU
	}
	if smallest == 0 {
		smallest = shadowQUICMinMTU
	}
	if smallest < quicSmallestMTU || initial < smallest {
		return nil, errors.New("invalid ShadowQUIC MTU")
	}
	return &shadowquic.ClientConfig{Address: c.Address.Build(), Port: uint32(c.Port), Username: c.Username, Password: c.Password,
		ServerName: c.ServerName, Alpn: c.ALPN, CongestionControl: c.CongestionControl, ZeroRtt: c.ZeroRTT,
		UdpOverStream: c.UDPOverStream, InitialMtu: uint32(c.InitialMTU), MinMtu: uint32(c.MinMTU)}, nil
}

func validShadowQUICName(name string) bool {
	return name != "" && len(name) <= 253 && !strings.ContainsAny(name, " /\\:@?#\t\r\n")
}

// The ShadowQUIC client opens its own socket to the server: nothing of Xray's transport,
// TLS, multiplexing or proxy chaining can apply to it.
func validateShadowQUICOutbound(c *OutboundDetourConfig) error {
	if c.StreamSetting != nil || c.ProxySettings != nil || c.SendThrough != nil || c.TargetStrategy != "" {
		return errors.New("ShadowQUIC takes no stream, proxy or source address settings")
	}
	if c.MuxSettings != nil && c.MuxSettings.Enabled {
		return errors.New("ShadowQUIC uses native multiplexing; Xray mux is unsupported")
	}
	return nil
}
