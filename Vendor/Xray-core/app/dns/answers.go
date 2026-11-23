package dns

import (
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/net"
)

// Elarni: answers remembers which addresses this DNS gave for which names. A name sniffed from
// a connection's first bytes need not be a name of the address the app dialed (a TLS hello can
// name a decoy site); it is one when this DNS gave the app that address for it. Asking the
// resolvers again at that moment would not tell: an answer rotates between queries.
type answers struct {
	mu sync.Mutex
	// Until when each pair is known. A full recent map becomes the older one and the one before
	// it is dropped, so the newest answerLimit pairs are always known and nothing is searched.
	recent, older map[answer]time.Time
	now           func() time.Time
}

// given is what every DNS of this process answered. A core that restarts in its process (a
// network change, another server) starts a new DNS, while the system's cache and the apps keep
// the addresses the last one gave them.
var given = new(answers)

type answer struct {
	name string
	ip   netip.Addr
}

const (
	// The app and the system keep an answer for its lifetime; an app also goes on connecting to
	// an address for a while after that. A pair is known for the longer of the two.
	answerKept  = time.Hour
	answerLimit = 8192
)

func answerOf(name string, ip net.IP) (answer, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	return answer{strings.ToLower(strings.TrimSuffix(name, ".")), addr.Unmap()}, ok
}

func (a *answers) time() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *answers) record(name string, ips []net.IP, ttl uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	until := a.time().Add(max(answerKept, time.Duration(ttl)*time.Second))
	for _, ip := range ips {
		key, ok := answerOf(name, ip)
		if !ok {
			continue
		}
		// A later answer does not unsay an earlier one: the app may still hold the one that
		// lives longer.
		kept, known := a.recent[key]
		if !known {
			kept = a.older[key]
			if len(a.recent) >= answerLimit {
				a.older, a.recent = a.recent, nil
			}
		}
		if a.recent == nil {
			a.recent = make(map[answer]time.Time)
		}
		if kept.Before(until) {
			kept = until
		}
		a.recent[key] = kept
	}
}

func (a *answers) gave(name string, ip net.IP) bool {
	key, ok := answerOf(name, ip)
	if !ok {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	until, known := a.recent[key]
	if !known {
		until, known = a.older[key]
	}
	return known && a.time().Before(until)
}
