package internet

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport/internet/finalmask"
	"github.com/xtls/xray-core/transport/internet/finalmask/noise"
	"github.com/xtls/xray-core/transport/internet/finalmask/salamander"
	"github.com/xtls/xray-core/transport/internet/finalmask/udphop"
	"github.com/xtls/xray-core/transport/internet/finalmask/xicmp"
)

// processResolver stands in for the process resolver: it answers names with ips, in that order.
func processResolver(t *testing.T, ips ...string) *[]string {
	t.Helper()
	var asked []string
	previous := lookupProcessIPs
	lookupProcessIPs = func(_ context.Context, host string) ([]net.IP, error) {
		asked = append(asked, host)
		if len(ips) == 0 {
			return nil, errors.New("no such host")
		}
		answer := make([]net.IP, 0, len(ips))
		for _, ip := range ips {
			answer = append(answer, net.ParseIP(ip))
		}
		return answer, nil
	}
	t.Cleanup(func() { lookupProcessIPs = previous })
	return &asked
}

// coreResolver is the core's DNS: it answers every name with ips, in that order.
type coreResolver struct {
	ips   []string
	asked []dns.IPOption
}

func (*coreResolver) Type() interface{} { return dns.ClientType() }
func (*coreResolver) Start() error      { return nil }
func (*coreResolver) Close() error      { return nil }
func (r *coreResolver) LookupIP(_ string, option dns.IPOption) ([]net.IP, uint32, error) {
	r.asked = append(r.asked, option)
	var answer []net.IP
	for _, ip := range r.ips {
		parsed := net.ParseIP(ip)
		if parsed.To4() != nil && option.IPv4Enable || parsed.To4() == nil && option.IPv6Enable {
			answer = append(answer, parsed)
		}
	}
	if len(answer) == 0 {
		return nil, 0, dns.ErrEmptyResponse
	}
	return answer, 60, nil
}

func withCoreResolver(r *coreResolver) context.Context {
	return ContextWithSystemDialerFeatures(context.Background(), func() (dns.Client, outbound.Manager) { return r, nil })
}

func addresses(candidates []net.Destination) string {
	var all []string
	for _, candidate := range candidates {
		all = append(all, candidate.NetAddr())
	}
	return strings.Join(all, " ")
}

func candidatesOf(t *testing.T, ctx context.Context, dest net.Destination, sockopt *SocketConfig) string {
	t.Helper()
	candidates, err := UDPCandidates(ctx, dest, sockopt, nil)
	if err != nil {
		t.Fatal(err)
	}
	return addresses(candidates)
}

// families is the family of each candidate, 4 or 6, in their order.
func families(candidates []net.Destination) string {
	var all []string
	for _, candidate := range candidates {
		if candidate.Address.Family().IsIPv4() {
			all = append(all, "4")
		} else {
			all = append(all, "6")
		}
	}
	return strings.Join(all, " ")
}

// A server given by its address is dialed exactly as before: no lookup, one candidate.
func TestUDPCandidatesOfAnAddressAreTheAddressAlone(t *testing.T) {
	asked := processResolver(t, "192.0.2.1")
	for _, address := range []string{"192.0.2.7", "2001:db8::7"} {
		dest := net.UDPDestination(net.ParseAddress(address), 443)
		candidates, err := UDPCandidates(context.Background(), dest, &SocketConfig{DomainStrategy: DomainStrategy_USE_IP}, nil)
		if err != nil || len(candidates) != 1 || candidates[0] != dest {
			t.Fatalf("%s: candidates %v, %v", address, candidates, err)
		}
	}
	if len(*asked) != 0 {
		t.Fatalf("an address was looked up: %v", *asked)
	}
}

// Without happyEyeballs settings the candidates start with IPv4, as net.ResolveUDPAddr picks
// and TCP's Happy Eyeballs orders, and alternate between the families, at most four, each once.
func TestUDPCandidatesOfANameAlternateFamiliesFromIPv4(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("first-ipv4.test"), 443)
	answer := []string{"2001:db8::1", "192.0.2.1", "2001:db8::2", "192.0.2.2", "2001:db8::3", "192.0.2.3"}
	processResolver(t, answer...)
	candidates, err := UDPCandidates(context.Background(), dest, nil, nil)
	if err != nil || families(candidates) != "4 6 4 6" {
		t.Fatalf("candidates %s, %v", addresses(candidates), err)
	}
	for i, candidate := range candidates {
		if !slices.Contains(answer, candidate.Address.IP().String()) || slices.Index(candidates, candidate) != i {
			t.Fatalf("candidates %s from the answer %v", addresses(candidates), answer)
		}
	}
	processResolver(t, "2001:db8::1", "2001:db8::2", "2001:db8::1")
	got := strings.Fields(candidatesOf(t, context.Background(), dest, &SocketConfig{}))
	slices.Sort(got)
	if strings.Join(got, " ") != "[2001:db8::1]:443 [2001:db8::2]:443" {
		t.Fatalf("IPv6-only candidates %v", got)
	}
}

// DialSystem picked one of a name's addresses at random, which spread the dials over a name's
// servers; the candidates shuffle each family for the same.
func TestUDPCandidatesSpreadDialsOverTheAddressesOfAFamily(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("round-robin.test"), 443)
	processResolver(t, "192.0.2.1", "192.0.2.2", "192.0.2.3", "2001:db8::1", "2001:db8::2")
	first := map[string]int{}
	second := map[string]int{}
	for range 100 {
		candidates, err := UDPCandidates(context.Background(), dest, nil, nil)
		if err != nil || len(candidates) != 4 {
			t.Fatalf("candidates %v, %v", candidates, err)
		}
		first[candidates[0].Address.IP().String()]++
		second[candidates[1].Address.IP().String()]++
	}
	for _, address := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		if first[address] == 0 {
			t.Fatalf("%s never came first in 100 dials: %v", address, first)
		}
	}
	for _, address := range []string{"2001:db8::1", "2001:db8::2"} {
		if second[address] == 0 {
			t.Fatalf("%s never came first of its family in 100 dials: %v", address, second)
		}
	}
}

// The candidates are ordered as TCP's Happy Eyeballs orders a name's addresses, with the
// user's prioritizeIPv6 and interleave.
func TestUDPCandidatesFollowTheHappyEyeballsSettings(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("eyeballs.test"), 443)
	processResolver(t, "192.0.2.1", "192.0.2.2", "192.0.2.3", "2001:db8::1", "2001:db8::2", "2001:db8::3")
	for _, c := range []struct {
		settings *HappyEyeballsConfig
		want     string
	}{
		{nil, "4 6 4 6"},
		{&HappyEyeballsConfig{Interleave: 1}, "4 6 4 6"},
		{&HappyEyeballsConfig{PrioritizeIpv6: true, Interleave: 1}, "6 4 6 4"},
		{&HappyEyeballsConfig{Interleave: 2}, "4 4 6 6"},
		{&HappyEyeballsConfig{PrioritizeIpv6: true, Interleave: 2}, "6 6 4 4"},
	} {
		candidates, err := UDPCandidates(context.Background(), dest, &SocketConfig{HappyEyeballs: c.settings}, nil)
		if err != nil || families(candidates) != c.want {
			t.Fatalf("%v: candidates %s, %v, want families %s", c.settings, addresses(candidates), err, c.want)
		}
	}
}

// With a domain strategy the core's DNS answers, for the strategy's families, instead of the
// process resolver.
func TestUDPCandidatesWithADomainStrategyAskTheCoresDNS(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("strategy.test"), 443)
	resolver := &coreResolver{ips: []string{"::1", "127.0.0.1", "::2"}}
	ctx := withCoreResolver(resolver)
	asked := processResolver(t, "192.0.2.1")
	got := candidatesOf(t, ctx, dest, &SocketConfig{DomainStrategy: DomainStrategy_USE_IP})
	if got != "127.0.0.1:443 [::1]:443 [::2]:443" && got != "127.0.0.1:443 [::2]:443 [::1]:443" {
		t.Fatalf("candidates %s", got)
	}
	if got := candidatesOf(t, ctx, dest, &SocketConfig{DomainStrategy: DomainStrategy_USE_IP4}); got != "127.0.0.1:443" {
		t.Fatalf("UseIPv4 candidates %s", got)
	}
	if len(*asked) != 0 {
		t.Fatalf("the process resolver answered a name the core's DNS resolves: %v", *asked)
	}
}

// The core's DNS failing leaves a forced strategy without an address and the others with the
// process resolver, as DialSystem does.
func TestUDPCandidatesWhenTheCoresDNSFails(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("unanswered.test"), 443)
	ctx := withCoreResolver(&coreResolver{})
	processResolver(t, "192.0.2.1")
	if _, err := UDPCandidates(ctx, dest, &SocketConfig{DomainStrategy: DomainStrategy_FORCE_IP}, nil); err == nil {
		t.Fatal("a forced strategy without an answer gave candidates")
	}
	if got := candidatesOf(t, ctx, dest, &SocketConfig{DomainStrategy: DomainStrategy_USE_IP}); got != "192.0.2.1:443" {
		t.Fatalf("candidates %s", got)
	}
}

// A dialer proxy without a domain strategy gets the name, as DialSystem hands it on; SRV and
// TXT records choose the server in DialSystem.
func TestUDPCandidatesLeaveNamesToDialSystemWhereItResolvesThem(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("chained.test"), 443)
	asked := processResolver(t, "192.0.2.1")
	for _, sockopt := range []*SocketConfig{{DialerProxy: "chain"}, {AddressPortStrategy: AddressPortStrategy_SrvPortOnly}} {
		candidates, err := UDPCandidates(context.Background(), dest, sockopt, nil)
		if err != nil || len(candidates) != 1 || candidates[0] != dest {
			t.Fatalf("%v: candidates %v, %v", sockopt, candidates, err)
		}
	}
	if len(*asked) != 0 {
		t.Fatalf("looked up %v", *asked)
	}
}

// The address that completed the last handshake of a name and port is tried first, while the
// name still has it, until the network changes. Another port or network of the name may be
// served by another of its hosts, so it keeps its own.
func TestUDPCandidatesStartWithTheWorkingAddressUntilThePathChanges(t *testing.T) {
	t.Cleanup(SocketPathChanged)
	dest := net.UDPDestination(net.DomainAddress("Working.test"), 443)
	processResolver(t, "::1", "127.0.0.1")
	const ipv4FirstOrder, workingOrder = "127.0.0.1:443 [::1]:443", "[::1]:443 127.0.0.1:443"
	if got := candidatesOf(t, context.Background(), dest, nil); got != ipv4FirstOrder {
		t.Fatalf("candidates %s", got)
	}
	udpDialSucceeded(net.UDPDestination(net.DomainAddress("working.test"), 8443), net.ParseIP("::1"), udpGeneration())
	udpDialSucceeded(net.TCPDestination(net.DomainAddress("working.test"), 443), net.ParseIP("::1"), udpGeneration())
	if got := candidatesOf(t, context.Background(), dest, nil); got != ipv4FirstOrder {
		t.Fatalf("the address of another port or network came first: %s", got)
	}
	udpDialSucceeded(net.UDPDestination(net.DomainAddress("working.TEST"), 443), net.ParseIP("::1"), udpGeneration())
	if got := candidatesOf(t, context.Background(), dest, nil); got != workingOrder {
		t.Fatalf("candidates %s", got)
	}
	other := net.UDPDestination(net.DomainAddress("other.test"), 443)
	if got := candidatesOf(t, context.Background(), other, nil); got != ipv4FirstOrder {
		t.Fatalf("another name's candidates %s", got)
	}
	udpDialSucceeded(dest, net.ParseIP("192.0.2.9"), udpGeneration())
	if got := candidatesOf(t, context.Background(), dest, nil); got != ipv4FirstOrder {
		t.Fatalf("an address the name no longer has came first: %s", got)
	}
	udpDialSucceeded(dest, net.ParseIP("::1"), udpGeneration())
	SocketPathChanged()
	if got := candidatesOf(t, context.Background(), dest, nil); got != ipv4FirstOrder {
		t.Fatalf("after a path change the candidates are %s", got)
	}
}

func udpDest(address string) net.Destination {
	return net.UDPDestination(net.ParseAddress(address), 443)
}

// attempts records which addresses a dial attempted, when each started and ended, and what was
// discarded.
type attempts struct {
	mu        sync.Mutex
	began     time.Time
	tried     []string
	started   map[string]time.Duration
	deadlines map[string]time.Time
	ended     map[string]bool
	discarded []string
}

func newAttempts() *attempts {
	return &attempts{began: time.Now(), started: map[string]time.Duration{}, deadlines: map[string]time.Time{}, ended: map[string]bool{}}
}

// run wraps behave, which dials one address, with the records.
func (a *attempts) run(behave func(context.Context, net.Destination) (string, error)) func(context.Context, net.Destination) (string, error) {
	return func(ctx context.Context, candidate net.Destination) (string, error) {
		address := candidate.NetAddr()
		deadline, _ := ctx.Deadline()
		a.mu.Lock()
		a.tried = append(a.tried, address)
		a.started[address] = time.Since(a.began)
		a.deadlines[address] = deadline
		a.mu.Unlock()
		result, err := behave(ctx, candidate)
		a.mu.Lock()
		a.ended[address] = true
		a.mu.Unlock()
		return result, err
	}
}

func (a *attempts) discard(result string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.discarded = append(a.discarded, result)
}

// silent is the attempt of an address that never answers.
func silent(ctx context.Context, _ net.Destination) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// An address that stays silent is overtaken once it ran for the stagger: the next address's
// attempt starts beside it, both share the dial's deadline, the one that answers wins and the
// silent one is cancelled before the dial returns. The winner is tried first next time.
func TestDialUDPCandidatesOvertakesASilentAddress(t *testing.T) {
	const stagger, budget = 100 * time.Millisecond, 2 * time.Second
	t.Cleanup(SetUDPAttemptStagger(stagger))
	t.Cleanup(SocketPathChanged)
	dest := net.UDPDestination(net.DomainAddress("overtakes.test"), 443)
	a := newAttempts()
	got, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("127.0.0.1"), udpDest("::1")}, budget, a.run(func(ctx context.Context, candidate net.Destination) (string, error) {
		if candidate.Address.IP().Equal(net.ParseIP("127.0.0.1")) {
			return silent(ctx, candidate)
		}
		return "connected", nil
	}), a.discard, nil)
	returned := time.Now()
	if err != nil || got != "connected" || strings.Join(a.tried, " ") != "127.0.0.1:443 [::1]:443" {
		t.Fatalf("got %q, %v after trying %v", got, err, a.tried)
	}
	if elapsed := returned.Sub(a.began); a.started["[::1]:443"] < stagger || elapsed >= budget {
		t.Fatalf("the second address started at %v (stagger %v), the dial took %v of %v", a.started["[::1]:443"], stagger, elapsed, budget)
	}
	if first, second := a.deadlines["127.0.0.1:443"], a.deadlines["[::1]:443"]; !first.Equal(second) || first.Before(a.began.Add(budget)) || first.After(returned.Add(budget)) {
		t.Fatalf("the attempts' deadlines %v and %v are not the dial's, %v after its start", first, second, budget)
	}
	if !a.ended["127.0.0.1:443"] {
		t.Fatal("the dial returned while the silent attempt ran on")
	}
	processResolver(t, "::1", "127.0.0.1")
	if candidates := candidatesOf(t, context.Background(), dest, nil); candidates != "[::1]:443 127.0.0.1:443" {
		t.Fatalf("next candidates %s", candidates)
	}
}

// When no address answers, the error names each one with its failure, wraps the last, and
// comes within the budget.
func TestDialUDPCandidatesNamesEveryAddressTried(t *testing.T) {
	t.Cleanup(SetUDPAttemptStagger(100 * time.Millisecond))
	dest := net.UDPDestination(net.DomainAddress("silent.test"), 443)
	const budget = 400 * time.Millisecond
	started := time.Now()
	_, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("::1"), udpDest("127.0.0.1")}, budget, silent, func(string) {}, nil)
	if elapsed := time.Since(started); elapsed > budget+100*time.Millisecond {
		t.Fatalf("failed after %v, budget %v", elapsed, budget)
	}
	if err == nil || !strings.Contains(err.Error(), "[::1]:443") || !strings.Contains(err.Error(), "127.0.0.1:443") || !strings.Contains(err.Error(), "silent.test") {
		t.Fatalf("error %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the error hides the last failure: %v", err)
	}
	processResolver(t, "::1", "127.0.0.1")
	if candidates := candidatesOf(t, context.Background(), dest, nil); candidates != "127.0.0.1:443 [::1]:443" {
		t.Fatalf("a failed name changed its candidates: %s", candidates)
	}
}

// Addresses that fail at once (no route for IPv6, a port nobody listens on) start the next one at
// once, without waiting for the stagger, until the last address of the four: time a fast failure
// leaves is not lost to the addresses after it.
func TestDialUDPCandidatesTriesEveryAddressAfterFastFailures(t *testing.T) {
	t.Cleanup(SocketPathChanged)
	dest := net.UDPDestination(net.DomainAddress("four.test"), 443)
	candidates := []net.Destination{udpDest("192.0.2.1"), udpDest("2001:db8::1"), udpDest("192.0.2.2"), udpDest("2001:db8::2")}
	a := newAttempts()
	got, err := DialUDPCandidates(context.Background(), dest, candidates, 10*time.Second, a.run(func(_ context.Context, candidate net.Destination) (string, error) {
		if candidate != candidates[3] {
			return "", errors.New("unreachable")
		}
		return "connected", nil
	}), a.discard, nil)
	if err != nil || got != "connected" || len(a.tried) != 4 {
		t.Fatalf("got %q, %v after trying %v", got, err, a.tried)
	}
	if elapsed := time.Since(a.began); elapsed > time.Second {
		t.Fatalf("the fourth address answered after %v: a failure waited for the stagger", elapsed)
	}
}

// An address that answers slowly but well (a lossy, high-latency link) keeps its attempt while
// the next addresses start beside it, and wins after them.
func TestDialUDPCandidatesKeepsASlowAddressRunning(t *testing.T) {
	const stagger, slow = 50 * time.Millisecond, 300 * time.Millisecond
	t.Cleanup(SetUDPAttemptStagger(stagger))
	t.Cleanup(SocketPathChanged)
	dest := net.UDPDestination(net.DomainAddress("slow.test"), 443)
	a := newAttempts()
	got, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("127.0.0.1"), udpDest("::1"), udpDest("127.0.0.2")}, 2*time.Second, a.run(func(ctx context.Context, candidate net.Destination) (string, error) {
		if !candidate.Address.IP().Equal(net.ParseIP("127.0.0.1")) {
			return silent(ctx, candidate)
		}
		select {
		case <-time.After(slow):
			return "slow", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}), a.discard, nil)
	if err != nil || got != "slow" || len(a.tried) != 3 {
		t.Fatalf("got %q, %v after trying %v", got, err, a.tried)
	}
	if a.started["127.0.0.2:443"] < 2*stagger || !a.ended["[::1]:443"] || !a.ended["127.0.0.2:443"] {
		t.Fatalf("the third address started at %v; the others ended: %v", a.started["127.0.0.2:443"], a.ended)
	}
}

// The dial's deadline is the caller's when that comes before the budget's: a lookup may have
// spent part of the caller's time. The addresses start within what is left, not by a plan for
// the whole budget.
func TestDialUDPCandidatesSharesTheCallersDeadline(t *testing.T) {
	t.Cleanup(SetUDPAttemptStagger(100 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	dest := net.UDPDestination(net.DomainAddress("short.test"), 443)
	a := newAttempts()
	_, err := DialUDPCandidates(ctx, dest, []net.Destination{udpDest("::1"), udpDest("127.0.0.1"), udpDest("::2")}, 10*time.Second, a.run(silent), a.discard, nil)
	if elapsed := time.Since(a.began); elapsed > 700*time.Millisecond {
		t.Fatalf("failed after %v, the caller gave 500 ms", elapsed)
	}
	if err == nil || len(a.tried) != 3 {
		t.Fatalf("tried %v within the caller's time: %v", a.tried, err)
	}
	for address, attemptDeadline := range a.deadlines {
		if !attemptDeadline.Equal(deadline) {
			t.Fatalf("%s had the deadline %v, the caller's is %v", address, attemptDeadline, deadline)
		}
	}
}

// An attempt whose dial completes as the winner cancels it is closed: the dial leaks no
// connection. The addresses after the winner do not start.
func TestDialUDPCandidatesClosesTheAttemptsThatLost(t *testing.T) {
	t.Cleanup(SetUDPAttemptStagger(200 * time.Millisecond))
	t.Cleanup(SocketPathChanged)
	dest := net.UDPDestination(net.DomainAddress("lost.test"), 443)
	a := newAttempts()
	got, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("127.0.0.1"), udpDest("::1"), udpDest("127.0.0.2")}, 2*time.Second, a.run(func(ctx context.Context, candidate net.Destination) (string, error) {
		if candidate.Address.IP().Equal(net.ParseIP("127.0.0.1")) {
			<-ctx.Done()
			return "late", nil
		}
		return "first", nil
	}), a.discard, nil)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil || got != "first" || strings.Join(a.tried, " ") != "127.0.0.1:443 [::1]:443" {
		t.Fatalf("got %q, %v after trying %v", got, err, a.tried)
	}
	if strings.Join(a.discarded, " ") != "late" {
		t.Fatalf("discarded %v by the time the dial returned", a.discarded)
	}
}

// A QUIC 0-RTT dial returns before the server answered, so its address is remembered only once
// the handshake completes; a connection that ends first remembers nothing.
func TestDialUDPCandidatesRemembersAnEarlyDialOnceTheServerAnswered(t *testing.T) {
	t.Cleanup(SocketPathChanged)
	type early struct{ answered, ended chan struct{} }
	handshake := func(e early) (<-chan struct{}, <-chan struct{}) { return e.answered, e.ended }
	dial := func(dest net.Destination, e early) {
		t.Helper()
		_, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("::1")}, time.Second, func(context.Context, net.Destination) (early, error) {
			return e, nil
		}, func(early) {}, handshake)
		if err != nil {
			t.Fatal(err)
		}
	}

	dest := net.UDPDestination(net.DomainAddress("early.test"), 443)
	pending := early{make(chan struct{}), make(chan struct{})}
	dial(dest, pending)
	if working := udpWorkingIP(dest); working != nil {
		t.Fatalf("remembered %v before the server answered", working)
	}
	close(pending.answered)
	for wait := time.Now().Add(2 * time.Second); udpWorkingIP(dest) == nil; time.Sleep(time.Millisecond) {
		if time.Now().After(wait) {
			t.Fatal("the address was not remembered once the server answered")
		}
	}

	dead := net.UDPDestination(net.DomainAddress("early-dead.test"), 443)
	unanswered := early{make(chan struct{}), make(chan struct{})}
	close(unanswered.ended)
	dial(dead, unanswered)
	if working := udpWorkingIP(dead); working != nil {
		t.Fatalf("a connection that ended unanswered remembered %v", working)
	}
}

// A server that answered and refused the client ends the attempts at once with its own error:
// another address of the same server would refuse it too. Nothing is remembered for the name.
func TestDialUDPCandidatesStopsAtAServerThatAnswered(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("refusing.test"), 443)
	refused := errors.New("auth failed")
	a := newAttempts()
	_, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("127.0.0.1"), udpDest("::1")}, 10*time.Second, a.run(func(context.Context, net.Destination) (string, error) {
		return "", UDPServerAnswered(refused)
	}), a.discard, nil)
	if err != refused || strings.Join(a.tried, " ") != "127.0.0.1:443" {
		t.Fatalf("tried %v, error %v", a.tried, err)
	}
	if working := udpWorkingIP(dest); working != nil {
		t.Fatalf("a refused dial remembered %v", working)
	}

	t.Cleanup(SetUDPAttemptStagger(50 * time.Millisecond))
	a = newAttempts()
	_, err = DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("::1"), udpDest("127.0.0.1")}, 10*time.Second, a.run(func(ctx context.Context, candidate net.Destination) (string, error) {
		if candidate.Address.IP().Equal(net.ParseIP("::1")) {
			return silent(ctx, candidate)
		}
		return "", UDPServerAnswered(refused)
	}), a.discard, nil)
	if err != refused || !a.ended["[::1]:443"] {
		t.Fatalf("a refusal beside a silent address gave %v; the silent one ended: %v", err, a.ended)
	}

	_, err = DialUDPCandidates(context.Background(), dest, []net.Destination{dest}, 10*time.Second, func(context.Context, net.Destination) (int, error) {
		return 0, UDPServerAnswered(refused)
	}, func(int) {}, nil)
	if err != refused {
		t.Fatalf("the one-address dial returned %v", err)
	}
}

func finalMaskOf(masks ...finalmask.UDPMask) *finalmask.FinalMask {
	return finalmask.NewFinalMask(nil, masks, nil, nil, nil, nil)
}

// Salamander and udphop send to the address the dial gives them, so a name's addresses are
// tried under them as without a mask. Any other final mask keeps the name as the one
// destination, as before: xicmp and xdns read the destination themselves.
func TestUDPCandidatesUnderAFinalMask(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("masked.test"), 443)
	resolver := &coreResolver{ips: []string{"192.0.2.1", "2001:db8::1"}}
	ctx := withCoreResolver(resolver)
	sockopt := &SocketConfig{DomainStrategy: DomainStrategy_USE_IP}
	for name, mask := range map[string]*finalmask.FinalMask{
		"salamander":         finalMaskOf(&salamander.Config{Password: "p"}),
		"salamander packets": finalMaskOf(&salamander.GeckoConfig{Password: "p", MinPacketSize: 100, MaxPacketSize: 200}),
		"udphop":             finalMaskOf(&udphop.Config{}),
		"udphop, salamander": finalMaskOf(&udphop.Config{}, &salamander.Config{Password: "p"}),
	} {
		candidates, err := UDPCandidates(ctx, dest, sockopt, mask)
		if got := addresses(candidates); err != nil || got != "192.0.2.1:443 [2001:db8::1]:443" {
			t.Fatalf("%s: candidates %s, %v", name, got, err)
		}
	}
	resolver.asked = nil
	for name, mask := range map[string]*finalmask.FinalMask{
		"xicmp":              finalMaskOf(&xicmp.Config{}),
		"noise":              finalMaskOf(&noise.Config{}),
		"salamander, xicmp":  finalMaskOf(&salamander.Config{Password: "p"}, &xicmp.Config{}),
		"udphop, noise":      finalMaskOf(&udphop.Config{}, &noise.Config{}),
		"no mask of its own": finalMaskOf(&otherUDPMask{}),
	} {
		candidates, err := UDPCandidates(ctx, dest, sockopt, mask)
		if err != nil || len(candidates) != 1 || candidates[0] != dest {
			t.Fatalf("%s: candidates %v, %v", name, candidates, err)
		}
	}
	if len(resolver.asked) != 0 {
		t.Fatalf("a name under a mask that reads it was looked up: %v", resolver.asked)
	}
}

// otherUDPMask is a mask this package knows nothing about.
type otherUDPMask struct{}

func (*otherUDPMask) WrapPacketConnClient(conn net.PacketConn, _ *net.Destination, _ *finalmask.Dialer) (net.PacketConn, error) {
	return conn, nil
}

func (*otherUDPMask) WrapPacketConnServer(conn net.PacketConn, _ net.Addr, _ *finalmask.ListenConfig) (net.PacketConn, error) {
	return conn, nil
}

// A server given by its address, or a name DialSystem resolves itself, is one attempt as
// before: the caller's context, the attempt's own error, nothing remembered.
func TestDialUDPCandidatesOfOneUnresolvedDestinationIsTheDialOfOld(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "caller")
	failed := errors.New("refused")
	for _, dest := range []net.Destination{udpDest("192.0.2.1"), net.UDPDestination(net.DomainAddress("chained.test"), 443)} {
		calls := 0
		_, err := DialUDPCandidates(ctx, dest, []net.Destination{dest}, 10*time.Second, func(attemptCtx context.Context, candidate net.Destination) (int, error) {
			calls++
			if attemptCtx != ctx || candidate != dest {
				t.Errorf("%s: dialed %s with another context", dest, candidate)
			}
			return 0, failed
		}, func(int) {}, nil)
		if calls != 1 || err != failed {
			t.Fatalf("%s: %d attempts, error %v", dest, calls, err)
		}
	}
}

// A caller that gives up ends the attempts: the next address is not tried.
func TestDialUDPCandidatesStopsWhenTheCallerGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dest := net.UDPDestination(net.DomainAddress("given-up.test"), 443)
	a := newAttempts()
	_, err := DialUDPCandidates(ctx, dest, []net.Destination{udpDest("::1"), udpDest("127.0.0.1")}, 10*time.Second, a.run(func(ctx context.Context, _ net.Destination) (string, error) {
		cancel()
		return "", ctx.Err()
	}), a.discard, nil)
	if len(a.tried) != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("%d attempts after the caller gave up: %v", len(a.tried), err)
	}
}

// A handshake that began before the network changed does not tell the new network which
// address works.
func TestDialUDPCandidatesStartedBeforeAPathChangeRemembersNothing(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("old-path.test"), 443)
	_, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("127.0.0.1"), udpDest("::1")}, 10*time.Second, func(_ context.Context, candidate net.Destination) (int, error) {
		if candidate.Address.IP().Equal(net.ParseIP("127.0.0.1")) {
			return 0, errors.New("refused")
		}
		SocketPathChanged()
		return 1, nil
	}, func(int) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withCoreResolver(&coreResolver{ips: []string{"::1", "127.0.0.1"}})
	if got := candidatesOf(t, ctx, dest, &SocketConfig{DomainStrategy: DomainStrategy_USE_IP}); got != "127.0.0.1:443 [::1]:443" {
		t.Fatalf("candidates %s after a path change during the dial", got)
	}
}

// The gateway of the outbound narrows the family the core's DNS is asked for, as in DialSystem.
func TestUDPCandidatesAskTheCoresDNSForTheGatewaysFamily(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("gateway.test"), 443)
	resolver := &coreResolver{ips: []string{"::1", "127.0.0.1"}}
	ctx := session.ContextWithOutbounds(withCoreResolver(resolver), []*session.Outbound{{Gateway: net.ParseAddress("192.0.2.10")}})
	if got := candidatesOf(t, ctx, dest, &SocketConfig{DomainStrategy: DomainStrategy_USE_IP}); got != "127.0.0.1:443" {
		t.Fatalf("candidates %s for an IPv4 gateway", got)
	}
}
