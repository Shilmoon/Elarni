package conf

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/transport/internet/splithttp"
)

// A post size of 0 or less stopped the process at the first packet-up dial; the builder refuses it
// instead, and 0 alone still takes the default.
func TestXHTTPRefusesPostSizesThatAreNotPositive(t *testing.T) {
	for _, value := range []string{`"0-1000000"`, `-5`, `"-5-100"`, `"-10--5"`} {
		var settings SplitHTTPConfig
		if err := json.Unmarshal([]byte(`{"mode":"packet-up","scMaxEachPostBytes":`+value+`}`), &settings); err != nil {
			t.Fatal(err)
		}
		if _, err := settings.Build(); err == nil || !strings.Contains(err.Error(), "scMaxEachPostBytes") {
			t.Errorf("scMaxEachPostBytes %s: error %v, want one about scMaxEachPostBytes", value, err)
		}
	}
	reversed := SplitHTTPConfig{ScMaxEachPostBytes: Int32Range{From: 2000, To: 1000}}
	if _, err := reversed.Build(); err == nil {
		t.Error("a range that ends before it starts was taken")
	}
	for value, want := range map[string][2]int32{`0`: {0, 0}, `"1000-2000"`: {1000, 2000}, `500000`: {500000, 500000}} {
		var settings SplitHTTPConfig
		if err := json.Unmarshal([]byte(`{"mode":"packet-up","scMaxEachPostBytes":`+value+`}`), &settings); err != nil {
			t.Fatal(err)
		}
		built, err := settings.Build()
		if err != nil {
			t.Errorf("scMaxEachPostBytes %s: %v", value, err)
			continue
		}
		if r := built.(*splithttp.Config).ScMaxEachPostBytes; r.GetFrom() != want[0] || r.GetTo() != want[1] {
			t.Errorf("scMaxEachPostBytes %s built as %d-%d", value, r.GetFrom(), r.GetTo())
		}
	}
}
