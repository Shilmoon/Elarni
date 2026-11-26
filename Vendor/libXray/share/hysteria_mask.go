package share

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/infra/conf"
)

// buildHy2FinalMask builds Hysteria2 bandwidth / port hopping / salamander masks (shared by URI and Clash).
// Gecko is Xray's salamander with a packet size range, which 3x-ui and v2rayN write as minPacketSize and
// maxPacketSize; a size left out is Hysteria's own default (geckoPacketSize), as the official URI and
// Clash files name none.
func buildHy2FinalMask(up, down, ports string, hopInterval conf.Int32Range, obfsType, obfsPassword, minPacketSize, maxPacketSize string) (*conf.FinalMask, error) {
	var quicParams *conf.QuicParamsConfig
	if up != "" || down != "" {
		quicParams = &conf.QuicParamsConfig{Congestion: "brutal", BrutalUp: conf.Bandwidth(up), BrutalDown: conf.Bandwidth(down)}
	}

	var udpMasks []conf.Mask
	if obfsType != "" && ((obfsType != "salamander" && obfsType != "gecko") || obfsPassword == "") {
		return nil, fmt.Errorf("unsupported Hysteria2 obfuscation")
	}
	if obfsType != "" {
		obfs := conf.Mask{Type: "salamander"}
		salamander := &conf.Salamander{Password: obfsPassword}
		if obfsType == "gecko" {
			size, err := geckoPacketSize(minPacketSize, maxPacketSize)
			if err != nil {
				return nil, err
			}
			salamander.PacketSize = size
		}
		salamanderRawMessage, err := convertJsonToRawMessage(salamander)
		if err != nil {
			return nil, err
		}
		obfs.Settings = &salamanderRawMessage
		udpMasks = append(udpMasks, obfs)
	}

	if ports != "" {
		var portList conf.PortList
		portListJSON, err := json.Marshal(ports)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(portListJSON, &portList); err != nil {
			return nil, err
		}
		// Hysteria hops to a new port from a new local socket. The hop mask dials, and
		// Xray applies masks last to first with only the first allowed to dial.
		hop, err := convertJsonToRawMessage(&conf.UDPHop{Mode: "intervalLocal,intervalRemote", Interval: hopInterval, RemotePorts: portList})
		if err != nil {
			return nil, err
		}
		udpMasks = append(udpMasks, conf.Mask{Type: "udphop", Settings: &hop})
	}

	if quicParams == nil && len(udpMasks) == 0 {
		return nil, nil
	}
	return &conf.FinalMask{QuicParams: quicParams, Udp: udpMasks}, nil
}

var (
	errHy2MaskConflict = errors.New("conflicting Hysteria2 FinalMask settings")
	errHy2MaskInvalid  = errors.New("invalid Hysteria2 FinalMask")
)

// mergeHy2Masks adds the mask a link's own parameters say (obfs, ports, up and down) to the one its
// "fm" says. Remnawave and PasarGuard write both for the same server, each spelled its own way, so a
// setting both name must mean the same, and a layer or setting only one of them names is kept.
// unsizedGecko says the link's salamander layer is gecko without packet sizes: "fm" may name them, and
// Hysteria's defaults stand for them only when neither does.
func mergeHy2Masks(explicit map[string]any, legacy *conf.FinalMask, unsizedGecko bool) error {
	if legacy.QuicParams != nil {
		merged, err := mergeMaskSettings(explicit["quicParams"], setJSON(legacy.QuicParams), func() any { return &conf.QuicParamsConfig{} })
		if err != nil {
			return err
		}
		explicit["quicParams"] = merged
	}
	for _, layer := range legacy.Udp {
		if err := mergeUDPMask(explicit, layer, unsizedGecko && !strings.EqualFold(layer.Type, "udphop")); err != nil {
			return err
		}
	}
	return nil
}

// mergeUDPMask merges a salamander or udphop layer into the "fm" layer of its type, or adds it. Xray
// applies the layers last to first and only the first may dial, so a new udphop goes last, others first.
// Plain salamander and gecko share the type but not the wire format, so a link and an "fm" that say one
// each disagree.
func mergeUDPMask(explicit map[string]any, layer conf.Mask, unsizedGecko bool) error {
	var layers []any
	if value := explicit["udp"]; value != nil {
		var ok bool
		if layers, ok = value.([]any); !ok {
			return errHy2MaskInvalid
		}
	}
	settings := func() any {
		if strings.EqualFold(layer.Type, "udphop") {
			return &conf.UDPHop{}
		}
		return &conf.Salamander{}
	}
	var added json.RawMessage
	if layer.Settings != nil {
		added = *layer.Settings
	}
	for _, value := range layers {
		existing, ok := value.(map[string]any)
		if !ok {
			return errHy2MaskInvalid
		}
		if kind, _ := existing["type"].(string); !strings.EqualFold(kind, layer.Type) {
			continue
		}
		if !strings.EqualFold(layer.Type, "udphop") {
			stated, err := geckoLayer(existing["settings"])
			if err != nil {
				return err
			}
			linked, err := geckoLayer(added)
			if err != nil {
				return err
			}
			if stated != (linked || unsizedGecko) {
				return errHy2MaskConflict
			}
		}
		merged, err := mergeMaskSettings(existing["settings"], added, settings)
		if err != nil {
			return err
		}
		existing["settings"] = merged
		return nil
	}
	if unsizedGecko {
		var gecko conf.Salamander
		if len(added) > 0 && json.Unmarshal(added, &gecko) != nil {
			return errHy2MaskInvalid
		}
		gecko.PacketSize, _ = geckoPacketSize("", "")
		sized, err := convertJsonToRawMessage(&gecko)
		if err != nil {
			return err
		}
		added = sized
	}
	merged, err := mergeMaskSettings(nil, added, settings)
	if err != nil {
		return err
	}
	entry := map[string]any{"type": layer.Type, "settings": merged}
	if strings.EqualFold(layer.Type, "udphop") {
		explicit["udp"] = append(layers, entry)
	} else {
		explicit["udp"] = append([]any{entry}, layers...)
	}
	return nil
}

// mergeMaskSettings merges added, the settings of one layer as setJSON writes them, into current, the
// same layer's settings as "fm" holds them. Each side is read as the type both stand for, so a setting
// at its default, spelled in another case or as a number rather than a string, is the same setting.
func mergeMaskSettings(current any, added json.RawMessage, typed func() any) (map[string]any, error) {
	read := func(raw []byte) (map[string]json.RawMessage, error) {
		value := typed()
		if len(raw) > 0 && json.Unmarshal(raw, value) != nil {
			return nil, errHy2MaskInvalid
		}
		fields := map[string]json.RawMessage{}
		if text := setJSON(value); text != nil && json.Unmarshal(text, &fields) != nil {
			return nil, errHy2MaskInvalid
		}
		return fields, nil
	}
	var currentText []byte
	if current != nil {
		if _, ok := current.(map[string]any); !ok {
			return nil, errHy2MaskInvalid
		}
		currentText, _ = json.Marshal(current)
	}
	fields, err := read(currentText)
	if err != nil {
		return nil, err
	}
	additions, err := read(added)
	if err != nil {
		return nil, err
	}
	for key, value := range additions {
		if old, ok := fields[key]; ok && !bytes.Equal(old, value) {
			return nil, errHy2MaskConflict
		}
		fields[key] = value
	}
	text, _ := json.Marshal(fields)
	var merged map[string]any
	if err := json.Unmarshal(text, &merged); err != nil {
		return nil, errHy2MaskInvalid
	}
	return merged, nil
}

// moveLegacyUDPHop moves the port hopping that older cores read from "fm" quicParams.udpHop, which
// Remnawave and PasarGuard still write, to the udphop layer that replaced it (Xray-core #6327). The core
// ignores the old key, so the server would lose its hopping without a word.
func moveLegacyUDPHop(explicit map[string]any) error {
	params, ok := explicit["quicParams"].(map[string]any)
	if !ok {
		return nil
	}
	for key, value := range params {
		if !strings.EqualFold(key, "udpHop") {
			continue
		}
		delete(params, key)
		if len(params) == 0 {
			delete(explicit, "quicParams")
		}
		if value == nil {
			return nil
		}
		var old struct {
			Ports    conf.PortList   `json:"ports"`
			Interval conf.Int32Range `json:"interval"`
		}
		encoded, _ := json.Marshal(value)
		if _, object := value.(map[string]any); !object || json.Unmarshal(encoded, &old) != nil {
			return errHy2MaskInvalid
		}
		if len(old.Ports.Range) == 0 {
			return nil
		}
		hop, err := convertJsonToRawMessage(&conf.UDPHop{Mode: "intervalLocal,intervalRemote", Interval: old.Interval, RemotePorts: old.Ports})
		if err != nil {
			return err
		}
		return mergeUDPMask(explicit, conf.Mask{Type: "udphop", Settings: &hop}, false)
	}
	return nil
}

// geckoPacketSize reads gecko's packet sizes within the bounds Xray's salamander builds: 0 < min <= max <= 2048.
// A size left out is Hysteria's own client default, 512 or 1200.
func geckoPacketSize(minPacketSize, maxPacketSize string) (conf.Int32Range, error) {
	if minPacketSize == "" {
		minPacketSize = "512"
	}
	if maxPacketSize == "" {
		maxPacketSize = "1200"
	}
	low, lowErr := strconv.ParseUint(minPacketSize, 10, 16)
	high, highErr := strconv.ParseUint(maxPacketSize, 10, 16)
	if lowErr != nil || highErr != nil || low == 0 || low > high || high > 2048 {
		return conf.Int32Range{}, fmt.Errorf("unsupported Hysteria2 obfuscation")
	}
	return conf.Int32Range{Left: int32(low), Right: int32(high), From: int32(low), To: int32(high)}, nil
}

// geckoLayer says whether salamander settings, as "fm" or setJSON writes them, are gecko: Xray builds
// gecko for a packet size range and plain salamander without one.
func geckoLayer(settings any) (bool, error) {
	var text []byte
	switch value := settings.(type) {
	case nil:
	case json.RawMessage:
		text = value
	case map[string]any:
		text, _ = json.Marshal(value)
	default:
		return false, errHy2MaskInvalid
	}
	var layer conf.Salamander
	if len(text) > 0 && json.Unmarshal(text, &layer) != nil {
		return false, errHy2MaskInvalid
	}
	return layer.PacketSize.To > 0, nil
}
