package dispatcher

import (
	"sync"
	"time"

	"github.com/xtls/xray-core/common/net"
)

// Elarni: a UDP flow ends after an idle period that is shorter than some QUIC idle timeouts. The
// connection's next packet starts a new flow, but it has a short header and no name, so the flow was
// routed by its address alone and could leave through another outbound than the connection used.
// The name sniffed from the Initial packet is kept for the same pair of sockets.

const (
	quicNameLifetime = 10 * time.Minute
	quicNameLimit    = 1024
)

type quicNameKey struct{ source, target net.Destination }

type quicName struct {
	domain  string
	expires time.Time
}

type quicNames struct {
	sync.Mutex
	names map[quicNameKey]quicName
}

func (q *quicNames) remember(source, target net.Destination, domain string) {
	if !source.IsValid() || target.Network != net.Network_UDP {
		return
	}
	now := time.Now()
	q.Lock()
	defer q.Unlock()
	if q.names == nil {
		q.names = make(map[quicNameKey]quicName)
	}
	if len(q.names) >= quicNameLimit {
		for key, name := range q.names {
			if now.After(name.expires) {
				delete(q.names, key)
			}
		}
		for key := range q.names {
			if len(q.names) < quicNameLimit {
				break
			}
			delete(q.names, key)
		}
	}
	q.names[quicNameKey{source, target}] = quicName{domain, now.Add(quicNameLifetime)}
}

// recall returns the name of the QUIC connection between the two sockets, and keeps it for the
// flow that starts now.
func (q *quicNames) recall(source, target net.Destination) string {
	if !source.IsValid() || target.Network != net.Network_UDP {
		return ""
	}
	now := time.Now()
	key := quicNameKey{source, target}
	q.Lock()
	defer q.Unlock()
	name, found := q.names[key]
	if !found {
		return ""
	}
	if now.After(name.expires) {
		delete(q.names, key)
		return ""
	}
	name.expires = now.Add(quicNameLifetime)
	q.names[key] = name
	return name.domain
}

func (q *quicNames) reset() {
	q.Lock()
	q.names = nil
	q.Unlock()
}
