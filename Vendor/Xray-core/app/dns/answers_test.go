package dns

import (
	"fmt"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
)

func TestAnswersKnowTheNamesAnAddressWasGivenFor(t *testing.T) {
	now := time.Unix(1000, 0)
	given := &answers{now: func() time.Time { return now }}
	given.record("Site.Example.", []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("192.0.2.1")}, 300)
	given.record("other.example", []net.IP{net.ParseIP("2001:db8::2")}, 300)
	for _, pair := range []struct {
		name, ip string
		want     bool
	}{
		{"site.example", "2001:db8::1", true},
		{"SITE.example", "192.0.2.1", true}, // an app's hello need not spell the name as its query did
		{"site.example", "2001:db8::2", false},
		{"other.example", "2001:db8::1", false},
		{"decoy.example", "2001:db8::1", false},
	} {
		if got := given.gave(pair.name, net.ParseIP(pair.ip)); got != pair.want {
			t.Errorf("gave(%s, %s) = %v", pair.name, pair.ip, got)
		}
	}
	// A later answer for the name does not unsay an earlier one: the app may hold either.
	given.record("site.example", []net.IP{net.ParseIP("2001:db8::3")}, 300)
	if !given.gave("site.example", net.ParseIP("2001:db8::1")) || !given.gave("site.example", net.ParseIP("2001:db8::3")) {
		t.Error("a rotated answer replaced the one before it")
	}
	now = now.Add(answerKept)
	if given.gave("site.example", net.ParseIP("2001:db8::1")) {
		t.Error("an answer is still known after the time it is kept")
	}
}

// The app and the system keep an answer for its lifetime and do not ask again within it: an
// address given with a lifetime of a day is used all day.
func TestAnswersAreKnownAsLongAsTheAnswerLives(t *testing.T) {
	now := time.Unix(1000, 0)
	given := &answers{now: func() time.Time { return now }}
	given.record("day.example", []net.IP{net.ParseIP("2001:db8::1")}, 86400)
	now = now.Add(23 * time.Hour)
	if !given.gave("day.example", net.ParseIP("2001:db8::1")) {
		t.Fatal("an answer that lives a day is forgotten within it")
	}
	now = now.Add(time.Hour)
	if given.gave("day.example", net.ParseIP("2001:db8::1")) {
		t.Fatal("an answer is still known after its lifetime")
	}
}

// Past the limit the oldest answers go, not all of them: apps are using the recent ones.
func TestAnswersStayBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	given := &answers{now: func() time.Time { return now }}
	for i := range 3*answerLimit + 100 {
		given.record(fmt.Sprintf("host%d.example", i), []net.IP{net.ParseIP("2001:db8::1")}, 300)
	}
	if n := len(given.recent) + len(given.older); n > 2*answerLimit {
		t.Fatalf("%d answers kept", n)
	}
	for _, i := range []int{3*answerLimit + 99, 2*answerLimit + 100} {
		if !given.gave(fmt.Sprintf("host%d.example", i), net.ParseIP("2001:db8::1")) {
			t.Fatalf("answer %d, one of the newest %d, is not known", i, answerLimit)
		}
	}
	if given.gave("host0.example", net.ParseIP("2001:db8::1")) {
		t.Fatal("the oldest answer is still known")
	}
}
