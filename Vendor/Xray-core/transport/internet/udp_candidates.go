package internet

import (
	"context"
	gonet "net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet/finalmask"
	"github.com/xtls/xray-core/transport/internet/finalmask/salamander"
	"github.com/xtls/xray-core/transport/internet/finalmask/udphop"
)

// Elarni: a UDP transport whose server is a name tries the name's addresses in turn, so a
// handshake that fails on one address (an IPv6 address on a network without IPv6, one server
// of several that is down) moves on to the next within the transport's own time budget.

// udpCandidateLimit is how many addresses of a name one dial tries.
const udpCandidateLimit = 4

// udpAttemptMinimum is the least time one address gets, in nanoseconds: candidates the
// budget cannot give that much are not tried.
var udpAttemptMinimum atomic.Int64

func init() { udpAttemptMinimum.Store(int64(3 * time.Second)) }

// SetUDPAttemptMinimum replaces the least time one address of a name gets, for tests that
// inject short dial budgets. The result restores the previous minimum.
func SetUDPAttemptMinimum(d time.Duration) (restore func()) {
	previous := udpAttemptMinimum.Swap(int64(d))
	return func() { udpAttemptMinimum.Store(previous) }
}

// udpWorking remembers, per name, the address that completed the last handshake. A socket
// path change forgets them all and starts a new generation: a dial that began before it
// records nothing.
var udpWorking struct {
	sync.Mutex
	generation uint64
	ips        map[string]net.IP
}

func udpNameKey(dest net.Destination) string {
	return strings.ToLower(dest.Address.Domain())
}

// UDPDialSucceeded records ip as the address of dest's name that completed a handshake, so the
// next dial of the name tries it first. dest given by its address records nothing.
func UDPDialSucceeded(dest net.Destination, ip net.IP) {
	udpDialSucceeded(dest, ip, udpGeneration())
}

func udpGeneration() uint64 {
	udpWorking.Lock()
	defer udpWorking.Unlock()
	return udpWorking.generation
}

func udpDialSucceeded(dest net.Destination, ip net.IP, generation uint64) {
	if !dest.Address.Family().IsDomain() || ip == nil {
		return
	}
	udpWorking.Lock()
	defer udpWorking.Unlock()
	if udpWorking.generation != generation {
		return
	}
	if udpWorking.ips == nil {
		udpWorking.ips = make(map[string]net.IP)
	}
	udpWorking.ips[udpNameKey(dest)] = ip
}

func udpWorkingIP(dest net.Destination) net.IP {
	udpWorking.Lock()
	defer udpWorking.Unlock()
	return udpWorking.ips[udpNameKey(dest)]
}

// forgetUDPWorking forgets every working address: on another network another one may work.
func forgetUDPWorking() {
	udpWorking.Lock()
	defer udpWorking.Unlock()
	udpWorking.generation++
	udpWorking.ips = nil
}

// lookupProcessIPs resolves host as DefaultSystemDialer does for a UDP destination without a
// domain strategy: net.ResolveUDPAddr asks the process resolver, which an embedder may replace
// (the physical network's DNS on Windows, the network's DNS on Android). Tests replace it.
var lookupProcessIPs = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := gonet.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

// UDPCandidates returns the addresses a UDP transport tries in turn for dest, at most four.
// A name is resolved as DialSystem resolves it: by the core's DNS for the families of a domain
// strategy, else by the process resolver. The address that completed the last handshake of
// the name comes first while the name still has it; the others alternate between IPv4 and
// IPv6, starting with the family of the address DialSystem would have dialed: the first IPv4
// address without a strategy, as net.ResolveUDPAddr picks, else the core DNS's first answer.
// An address, a name that SRV or TXT records replace, a name a dialer proxy resolves, or a
// name under a final mask other than salamander and udphop is dest alone, which the transport
// dials as before.
func UDPCandidates(ctx context.Context, dest net.Destination, sockopt *SocketConfig, mask *finalmask.FinalMask) ([]net.Destination, error) {
	if !dest.Address.Family().IsDomain() || sockopt != nil && sockopt.AddressPortStrategy != AddressPortStrategy_None || maskReadsDestination(mask) {
		return []net.Destination{dest}, nil
	}
	domain := dest.Address.Domain()
	var ips []net.IP
	if sockopt != nil && sockopt.DomainStrategy.HasStrategy() {
		src, strategy := dialSource(ctx, dest, sockopt)
		resolved, err := LookupForIPContext(ctx, domain, strategy, src)
		if err != nil {
			errors.LogErrorInner(ctx, err, "failed to resolve ip")
			if sockopt.DomainStrategy.ForceIP() {
				return nil, err
			}
		}
		ips = resolved
	}
	firstIPv4 := len(ips) == 0
	if firstIPv4 {
		if sockopt != nil && len(sockopt.DialerProxy) > 0 {
			return []net.Destination{dest}, nil
		}
		resolved, err := lookupProcessIPs(ctx, domain)
		if err != nil {
			return nil, err
		}
		if len(resolved) == 0 {
			return nil, errors.New("no address for ", domain)
		}
		ips = resolved
	}
	ordered := orderUDPCandidates(ips, firstIPv4, udpWorkingIP(dest))
	candidates := make([]net.Destination, 0, len(ordered))
	for _, ip := range ordered {
		candidates = append(candidates, net.Destination{Network: dest.Network, Address: net.IPAddress(ip), Port: dest.Port})
	}
	return candidates, nil
}

// maskReadsDestination reports whether mask has a UDP mask other than salamander and udphop,
// which send to whichever address the dial gives them: xicmp and xdns read the destination
// themselves.
func maskReadsDestination(mask *finalmask.FinalMask) bool {
	if mask == nil {
		return false
	}
	for _, m := range mask.UDPMasks() {
		switch m.(type) {
		case *salamander.Config, *salamander.GeckoConfig, *udphop.Config:
		default:
			return true
		}
	}
	return false
}

// orderUDPCandidates puts working first if ips has it, then the others alternating between the
// families, starting with the first IPv4 address when firstIPv4 is set, else with ips[0].
func orderUDPCandidates(ips []net.IP, firstIPv4 bool, working net.IP) []net.IP {
	var v4, v6 []net.IP
	seen := make(map[string]bool, len(ips))
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
		if len(ip) != net.IPv4len && len(ip) != net.IPv6len || seen[string(ip)] {
			continue
		}
		seen[string(ip)] = true
		if len(ip) == net.IPv4len {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ip)
		}
	}
	if len(v4)+len(v6) == 0 {
		return nil
	}
	startIPv4 := len(v4) > 0 && (firstIPv4 || ips[0].To4() != nil)
	ordered := make([]net.IP, 0, udpCandidateLimit)
	if working != nil {
		for _, family := range []*[]net.IP{&v4, &v6} {
			for i, ip := range *family {
				if ip.Equal(working) {
					ordered = append(ordered, ip)
					*family = append((*family)[:i:i], (*family)[i+1:]...)
					break
				}
			}
		}
	}
	first, second := v6, v4
	if startIPv4 {
		first, second = v4, v6
	}
	for len(ordered) < udpCandidateLimit && len(first)+len(second) > 0 {
		if len(first) > 0 {
			ordered = append(ordered, first[0])
			first = first[1:]
		}
		if len(ordered) < udpCandidateLimit && len(second) > 0 {
			ordered = append(ordered, second[0])
			second = second[1:]
		}
	}
	return ordered
}

// UDPServerAnswered marks err as the failure of an attempt whose server answered, for example
// by refusing the client's password: another address of the same server would answer the same,
// so DialUDPCandidates tries no other address and returns err itself.
func UDPServerAnswered(err error) error {
	return &udpAnsweredError{err: err}
}

type udpAnsweredError struct{ err error }

func (e *udpAnsweredError) Error() string { return e.err.Error() }
func (e *udpAnsweredError) Unwrap() error { return e.err }

// DialUDPCandidates runs attempt for each of candidates, the addresses UDPCandidates gave for
// dest, until one succeeds, within budget. It plans as many addresses as the budget gives the
// minimum each and does not try the others; each attempt gets what is left of the budget over
// the planned addresses left, so one that fails early hands its time on. The address that
// succeeded comes first in the next dial of the name. When none succeeds, the error names each
// address tried with its failure and wraps the last failure; a caller that gives up, or an
// attempt that fails with UDPServerAnswered, ends the attempts, the latter with its own error.
// A single candidate that is dest itself is attempted as before: with ctx, its own error and
// nothing remembered.
func DialUDPCandidates[T any](ctx context.Context, dest net.Destination, candidates []net.Destination, budget time.Duration, attempt func(context.Context, net.Destination) (T, error)) (T, error) {
	if len(candidates) == 1 && candidates[0] == dest {
		result, err := attempt(ctx, dest)
		if answered, ok := err.(*udpAnsweredError); ok {
			err = answered.err
		}
		return result, err
	}
	var zero T
	if len(candidates) == 0 {
		return zero, errors.New("no address for ", dest.Address)
	}
	generation := udpGeneration()
	planned := len(candidates)
	for minimum := time.Duration(udpAttemptMinimum.Load()); planned > 1 && budget/time.Duration(planned) < minimum; {
		planned--
	}
	deadline := time.Now().Add(budget)
	failed := &udpCandidatesError{name: dest.Address.String()}
	for i, candidate := range candidates[:planned] {
		share := time.Until(deadline) / time.Duration(planned-i)
		if i > 0 && share <= 0 {
			break
		}
		attemptCtx, cancel := context.WithTimeout(ctx, share)
		result, err := attempt(attemptCtx, candidate)
		cancel()
		if err == nil {
			udpDialSucceeded(dest, candidate.Address.IP(), generation)
			return result, nil
		}
		if answered, ok := err.(*udpAnsweredError); ok {
			return zero, answered.err
		}
		errors.LogInfoInner(ctx, err, "failed to reach ", dest.Address, " at ", candidate.NetAddr())
		failed.addresses = append(failed.addresses, candidate.NetAddr())
		failed.errs = append(failed.errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return zero, failed
}

// udpCandidatesError is the failure of every address of a name a dial tried.
type udpCandidatesError struct {
	name      string
	addresses []string
	errs      []error
}

func (e *udpCandidatesError) Error() string {
	var b strings.Builder
	b.WriteString("each address of ")
	b.WriteString(e.name)
	b.WriteString(" failed: ")
	for i, address := range e.addresses {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(address)
		b.WriteString(": ")
		b.WriteString(e.errs[i].Error())
	}
	return b.String()
}

// Unwrap returns the last address's failure.
func (e *udpCandidatesError) Unwrap() error {
	return e.errs[len(e.errs)-1]
}
