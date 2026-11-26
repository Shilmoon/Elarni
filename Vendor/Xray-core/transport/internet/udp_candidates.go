package internet

import (
	"context"
	"math/rand/v2"
	gonet "net"
	"slices"
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

// Elarni: a UDP transport whose server is a name races the name's addresses, as Happy Eyeballs
// does for TCP, so a handshake that fails or goes unanswered on one address (an IPv6 address on
// a network without IPv6, one server of several that is down) is overtaken by the next address
// within the transport's own time budget.

// udpCandidateLimit is how many addresses of a name one dial tries.
const udpCandidateLimit = 4

// udpAttemptStagger is how long, in nanoseconds, the attempt last started runs before the next
// address's attempt starts beside it; a failed attempt starts the next one at once.
var udpAttemptStagger atomic.Int64

func init() { udpAttemptStagger.Store(int64(2 * time.Second)) }

// SetUDPAttemptStagger replaces the time an address's attempt runs before the next address's
// starts, for tests that inject short dial budgets. The result restores the previous stagger.
func SetUDPAttemptStagger(d time.Duration) (restore func()) {
	previous := udpAttemptStagger.Swap(int64(d))
	return func() { udpAttemptStagger.Store(previous) }
}

// udpWorking remembers, per name, port and network, the address that completed the last
// handshake. A socket path change forgets them all and starts a new generation: a dial that
// began before it records nothing.
var udpWorking struct {
	sync.Mutex
	generation uint64
	ips        map[string]net.IP
}

// udpWorkingKey is dest's name, network and port: another port of the name may be served by
// another of its hosts.
func udpWorkingKey(dest net.Destination) string {
	return strings.ToLower(dest.Address.Domain()) + " " + dest.Network.SystemString() + " " + dest.Port.String()
}

func udpGeneration() uint64 {
	udpWorking.Lock()
	defer udpWorking.Unlock()
	return udpWorking.generation
}

// udpDialSucceeded records ip as the address of dest's name that completed a handshake, so the
// next dial of the name and port tries it first, unless the socket path changed since
// generation. dest given by its address records nothing.
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
	udpWorking.ips[udpWorkingKey(dest)] = ip
}

// udpDialAnswered records ip as udpDialSucceeded does once answered closes: a QUIC 0-RTT dial
// returns before the server answered, and an address whose connection ends first (ended) was
// never heard from.
func udpDialAnswered(dest net.Destination, ip net.IP, generation uint64, answered, ended <-chan struct{}) {
	record := func() {
		select {
		case <-answered: // also when the connection ended after the server answered
			udpDialSucceeded(dest, ip, generation)
		default:
		}
	}
	select {
	case <-answered:
	case <-ended:
	default:
		go func() {
			select {
			case <-answered:
			case <-ended:
			}
			record()
		}()
		return
	}
	record()
}

func udpWorkingIP(dest net.Destination) net.IP {
	udpWorking.Lock()
	defer udpWorking.Unlock()
	return udpWorking.ips[udpWorkingKey(dest)]
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

// UDPCandidates returns the addresses a UDP transport races for dest, at most four. A name is
// resolved as DialSystem resolves it: by the core's DNS for the families of a domain strategy,
// else by the process resolver. The addresses are ordered as TCP's Happy Eyeballs orders them
// (sortIPs with sockopt's happyEyeballs settings: IPv4 first and the families alternating one by
// one without them), each family shuffled first, as DialSystem's random pick spread dials over
// a name's servers. The address that completed the last handshake of the name and port comes
// first while the name still has it. An address, a name that SRV or TXT records replace, a name
// a dialer proxy resolves, or a name under a final mask other than salamander and udphop is dest
// alone, which the transport dials as before.
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
	if len(ips) == 0 {
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
	ordered := orderUDPCandidates(ips, sockopt.GetHappyEyeballs(), udpWorkingIP(dest))
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

// orderUDPCandidates returns at most udpCandidateLimit of ips, each once: each family shuffled,
// then ordered by sortIPs with happyEyeballs' prioritizeIPv6 and interleave (false and 1, as the
// configuration's defaults, without it), and working moved first if ips has it.
func orderUDPCandidates(ips []net.IP, happyEyeballs *HappyEyeballsConfig, working net.IP) []net.IP {
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
	for _, family := range [][]net.IP{v4, v6} {
		rand.Shuffle(len(family), func(i, j int) { family[i], family[j] = family[j], family[i] })
	}
	prioritizeIPv6, interleave := false, uint32(1)
	if happyEyeballs != nil {
		prioritizeIPv6, interleave = happyEyeballs.PrioritizeIpv6, happyEyeballs.Interleave
	}
	ordered := sortIPs(append(v4, v6...), prioritizeIPv6, interleave)
	if working != nil {
		if i := slices.IndexFunc(ordered, working.Equal); i > 0 {
			first := ordered[i]
			copy(ordered[1:i+1], ordered[:i])
			ordered[0] = first
		}
	}
	return ordered[:min(len(ordered), udpCandidateLimit)]
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

// DialUDPCandidates races attempt over candidates, the addresses UDPCandidates gave for dest, in
// their order. The next address's attempt starts once an attempt fails or the one started last
// has run for the stagger (2 s), and every attempt runs until one succeeds or the dial's
// deadline: budget from now, or ctx's deadline if that comes first (a lookup may have spent part
// of ctx's time). The first success cancels the others; a result that succeeds all the same is
// given to discard, and DialUDPCandidates returns once every attempt has ended. The address that
// succeeded comes first in the next dial of the name and port; handshake, if set, returns for a
// result a channel that closes once the server answered (a QUIC 0-RTT dial returns before) and
// one that closes when the connection ends, and the address is remembered only after the first.
// When none succeeds, the error names each address tried with its failure and wraps the last; a
// caller that gives up, or an attempt that fails with UDPServerAnswered, ends the attempts, the
// latter with its own error. A single candidate that is dest itself is attempted as before: with
// ctx, its own error and nothing remembered.
func DialUDPCandidates[T any](ctx context.Context, dest net.Destination, candidates []net.Destination, budget time.Duration,
	attempt func(context.Context, net.Destination) (T, error), discard func(T), handshake func(T) (answered, ended <-chan struct{})) (T, error) {
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
	raceCtx, cancel := context.WithTimeout(ctx, budget) // capped at ctx's own deadline
	defer cancel()
	type outcome struct {
		index  int
		result T
		err    error
	}
	outcomes := make(chan outcome, len(candidates))
	stagger := time.Duration(udpAttemptStagger.Load())
	timer := time.NewTimer(stagger)
	defer timer.Stop()
	started, running := 0, 0
	start := func() {
		index := started
		started++
		running++
		timer.Reset(stagger)
		go func() {
			result, err := attempt(raceCtx, candidates[index])
			outcomes <- outcome{index: index, result: result, err: err}
		}()
	}
	errs := make([]error, len(candidates))
	won := -1
	var result T
	var refused error
	start()
	for running > 0 {
		decided := won >= 0 || refused != nil
		var next <-chan time.Time
		if !decided && started < len(candidates) {
			next = timer.C
		}
		select {
		case <-next:
			if raceCtx.Err() == nil {
				start()
			}
		case o := <-outcomes:
			running--
			answered, isAnswered := o.err.(*udpAnsweredError)
			switch {
			case o.err == nil && decided:
				discard(o.result) // its dial completed as it was cancelled
			case o.err == nil:
				won, result = o.index, o.result
				cancel()
			case decided:
				// cancelled by the success or the refusal
			case isAnswered:
				refused = answered.err
				cancel()
			default:
				errs[o.index] = o.err
				errors.LogInfoInner(ctx, o.err, "failed to reach ", dest.Address, " at ", candidates[o.index].NetAddr())
				if started < len(candidates) && raceCtx.Err() == nil {
					start()
				}
			}
		}
	}
	if won >= 0 {
		ip := candidates[won].Address.IP()
		if handshake == nil {
			udpDialSucceeded(dest, ip, generation)
		} else {
			answered, ended := handshake(result)
			udpDialAnswered(dest, ip, generation, answered, ended)
		}
		return result, nil
	}
	if refused != nil {
		return zero, refused
	}
	failed := &udpCandidatesError{name: dest.Address.String()}
	for i, err := range errs[:started] {
		failed.addresses = append(failed.addresses, candidates[i].NetAddr())
		failed.errs = append(failed.errs, err)
	}
	return zero, failed
}

// udpCandidatesError is the failure of every address of a name a dial tried, in the order
// they started.
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

// Unwrap returns the failure of the address started last.
func (e *udpCandidatesError) Unwrap() error {
	return e.errs[len(e.errs)-1]
}
