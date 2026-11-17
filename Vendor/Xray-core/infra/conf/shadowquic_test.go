package conf

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/proxy/shadowquic"
)

func buildShadowQUIC(outbound string) (*shadowquic.ClientConfig, error) {
	var detour OutboundDetourConfig
	if err := json.Unmarshal([]byte(outbound), &detour); err != nil {
		return nil, err
	}
	handler, err := detour.Build()
	if err != nil {
		return nil, err
	}
	message, err := handler.ProxySettings.GetInstance()
	if err != nil {
		return nil, err
	}
	return message.(*shadowquic.ClientConfig), nil
}

const shadowQUICSettings = `"address":"example.com","port":443,"username":"user","password":"pass","serverName":"cdn.example.com"`

func TestShadowQUICOutboundBuildsEverySetting(t *testing.T) {
	config, err := buildShadowQUIC(`{"tag":"proxy","protocol":"shadowquic","settings":{` + shadowQUICSettings +
		`,"alpn":["h3","h2"],"congestionControl":"new-reno","zeroRtt":true,"udpOverStream":true,"initialMtu":1400,"minMtu":1280}}`)
	if err != nil {
		t.Fatal(err)
	}
	if config.Address.AsAddress().Domain() != "example.com" || config.Port != 443 || config.Username != "user" || config.Password != "pass" ||
		config.ServerName != "cdn.example.com" || strings.Join(config.Alpn, ",") != "h3,h2" || config.CongestionControl != "new-reno" ||
		!config.ZeroRtt || !config.UdpOverStream || config.InitialMtu != 1400 || config.MinMtu != 1280 {
		t.Fatalf("built %v", config)
	}
	minimal, err := buildShadowQUIC(`{"protocol":"shadowquic","settings":{` + shadowQUICSettings + `}}`)
	if err != nil {
		t.Fatal(err)
	}
	// Left out means the ShadowQUIC client's own value, which zero and empty stand for.
	if len(minimal.Alpn) != 0 || minimal.CongestionControl != "" || minimal.ZeroRtt || minimal.UdpOverStream || minimal.InitialMtu != 0 || minimal.MinMtu != 0 {
		t.Fatalf("built %v", minimal)
	}
}

func TestShadowQUICOutboundRefusesInvalidSettings(t *testing.T) {
	settings := func(change string) string {
		return `{"protocol":"shadowquic","settings":{` + shadowQUICSettings + change + `}}`
	}
	for name, outbound := range map[string]string{
		"no address":             `{"protocol":"shadowquic","settings":{"port":443,"username":"u","password":"p","serverName":"example.com"}}`,
		"no port":                `{"protocol":"shadowquic","settings":{"address":"example.com","username":"u","password":"p","serverName":"example.com"}}`,
		"no username":            `{"protocol":"shadowquic","settings":{"address":"example.com","port":443,"password":"p","serverName":"example.com"}}`,
		"no password":            `{"protocol":"shadowquic","settings":{"address":"example.com","port":443,"username":"u","serverName":"example.com"}}`,
		"no server name":         `{"protocol":"shadowquic","settings":{"address":"example.com","port":443,"username":"u","password":"p"}}`,
		"server name with space": `{"protocol":"shadowquic","settings":{"address":"example.com","port":443,"username":"u","password":"p","serverName":"a b"}}`,
		"unknown setting":        settings(`,"uuid":"x"`),
		"duplicate setting":      settings(`,"port":444`),
		"null setting":           settings(`,"alpn":null`),
		"empty ALPN name":        settings(`,"alpn":[""]`),
		"unknown congestion":     settings(`,"congestionControl":"copa"`),
		"small MTU":              settings(`,"minMtu":1100`),
		"initial below minimum":  settings(`,"initialMtu":1280`),
		"stream settings":        `{"protocol":"shadowquic","settings":{` + shadowQUICSettings + `},"streamSettings":{"security":"tls"}}`,
		"socket options":         `{"protocol":"shadowquic","settings":{` + shadowQUICSettings + `},"streamSettings":{"sockopt":{"dialerProxy":"other"}}}`,
		"mux":                    `{"protocol":"shadowquic","settings":{` + shadowQUICSettings + `},"mux":{"enabled":true}}`,
		"source address":         `{"protocol":"shadowquic","settings":{` + shadowQUICSettings + `},"sendThrough":"192.0.2.1"}`,
		"unknown outbound field": `{"protocol":"shadowquic","settings":{` + shadowQUICSettings + `},"extra":1}`,
	} {
		if config, err := buildShadowQUIC(outbound); err == nil {
			t.Errorf("%s was accepted: %v", name, config)
		}
	}
}

// Another outbound may dial through a ShadowQUIC outbound, which carries TCP and UDP.
func TestTUICMayChainThroughShadowQUIC(t *testing.T) {
	config := &Config{OutboundConfigs: []OutboundDetourConfig{
		{Tag: "tuic", Protocol: "tuic", StreamSetting: &StreamConfig{SocketSettings: &SocketConfig{DialerProxy: "sq"}}},
		{Tag: "sq", Protocol: "shadowquic"},
	}}
	if err := config.ValidateTUICChains(); err != nil {
		t.Fatal(err)
	}
}
