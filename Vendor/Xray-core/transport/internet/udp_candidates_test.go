package internet

import (
	"context"
	"errors"
	"slices"
	"strings"
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

// Without a domain strategy the first address is the first IPv4 one, as net.ResolveUDPAddr
// picks; the others follow with the families alternating, at most four.
func TestUDPCandidatesOfANameAlternateFamiliesFromTheFirstIPv4(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("first-ipv4.test"), 443)
	processResolver(t, "2001:db8::1", "192.0.2.1", "2001:db8::2", "192.0.2.2", "2001:db8::3", "192.0.2.3")
	want := "192.0.2.1:443 [2001:db8::1]:443 192.0.2.2:443 [2001:db8::2]:443"
	if got := candidatesOf(t, context.Background(), dest, nil); got != want {
		t.Fatalf("candidates %s, want %s", got, want)
	}
	processResolver(t, "2001:db8::1", "2001:db8::2", "2001:db8::1")
	if got := candidatesOf(t, context.Background(), dest, &SocketConfig{}); got != "[2001:db8::1]:443 [2001:db8::2]:443" {
		t.Fatalf("IPv6-only candidates %s", got)
	}
}

// With a domain strategy the core's DNS answers in the strategy's order, which the candidates
// keep from the first address on, instead of the random pick of DialSystem.
func TestUDPCandidatesWithADomainStrategyFollowItsOrder(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("strategy.test"), 443)
	resolver := &coreResolver{ips: []string{"::1", "127.0.0.1", "::2"}}
	ctx := withCoreResolver(resolver)
	asked := processResolver(t, "192.0.2.1")
	for range 20 {
		if got := candidatesOf(t, ctx, dest, &SocketConfig{DomainStrategy: DomainStrategy_USE_IP}); got != "[::1]:443 127.0.0.1:443 [::2]:443" {
			t.Fatalf("candidates %s", got)
		}
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

// The address that completed the last handshake is tried first, while the name still has
// it, until the network changes.
func TestUDPCandidatesStartWithTheWorkingAddressUntilThePathChanges(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("Working.test"), 443)
	other := net.UDPDestination(net.DomainAddress("other.test"), 443)
	processResolver(t, "::1", "127.0.0.1", "::2")
	UDPDialSucceeded(net.UDPDestination(net.DomainAddress("working.TEST"), 8443), net.ParseIP("::2"))
	if got := candidatesOf(t, context.Background(), dest, nil); got != "[::2]:443 127.0.0.1:443 [::1]:443" {
		t.Fatalf("candidates %s", got)
	}
	if got := candidatesOf(t, context.Background(), other, nil); got != "127.0.0.1:443 [::1]:443 [::2]:443" {
		t.Fatalf("another name's candidates %s", got)
	}
	UDPDialSucceeded(dest, net.ParseIP("192.0.2.9"))
	if got := candidatesOf(t, context.Background(), dest, nil); got != "127.0.0.1:443 [::1]:443 [::2]:443" {
		t.Fatalf("an address the name no longer has came first: %s", got)
	}
	UDPDialSucceeded(dest, net.ParseIP("::2"))
	SocketPathChanged()
	if got := candidatesOf(t, context.Background(), dest, nil); got != "127.0.0.1:443 [::1]:443 [::2]:443" {
		t.Fatalf("after a path change the candidates are %s", got)
	}
}

func udpDest(address string) net.Destination {
	return net.UDPDestination(net.ParseAddress(address), 443)
}

// A failed handshake moves on to the next address; each address gets an equal share of the
// budget, and the one that answers is tried first next time.
func TestDialUDPCandidatesMovesOnWithinTheBudget(t *testing.T) {
	t.Cleanup(SetUDPAttemptMinimum(100 * time.Millisecond))
	t.Cleanup(SocketPathChanged)
	dest := net.UDPDestination(net.DomainAddress("moves-on.test"), 443)
	const budget = 600 * time.Millisecond
	var tried []string
	started := time.Now()
	got, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("::1"), udpDest("127.0.0.1")}, budget, func(ctx context.Context, candidate net.Destination) (string, error) {
		tried = append(tried, candidate.NetAddr())
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > budget/2 || time.Until(deadline) < budget/2-100*time.Millisecond {
			t.Errorf("%s got %v of a %v budget for two addresses", candidate.NetAddr(), time.Until(deadline), budget)
		}
		if candidate.Address.IP().Equal(net.ParseIP("::1")) {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "connected", nil
	})
	if err != nil || got != "connected" || strings.Join(tried, " ") != "[::1]:443 127.0.0.1:443" {
		t.Fatalf("got %q, %v after trying %v", got, err, tried)
	}
	if elapsed := time.Since(started); elapsed > budget {
		t.Fatalf("connected after %v, budget %v", elapsed, budget)
	}
	processResolver(t, "::1", "127.0.0.1")
	if candidates := candidatesOf(t, context.Background(), dest, nil); candidates != "127.0.0.1:443 [::1]:443" {
		t.Fatalf("next candidates %s", candidates)
	}
}

// When no address answers, the error names each one with its failure, wraps the last, and
// comes within the budget.
func TestDialUDPCandidatesNamesEveryAddressTried(t *testing.T) {
	t.Cleanup(SetUDPAttemptMinimum(100 * time.Millisecond))
	dest := net.UDPDestination(net.DomainAddress("silent.test"), 443)
	const budget = 400 * time.Millisecond
	started := time.Now()
	_, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("::1"), udpDest("127.0.0.1")}, budget, func(ctx context.Context, _ net.Destination) (struct{}, error) {
		<-ctx.Done()
		return struct{}{}, ctx.Err()
	})
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

// No address is planned with less than the minimum: those the budget cannot hold are not
// tried. Each attempt gets what is left of the budget over the addresses left, so one that
// fails at once hands its time to the rest.
func TestDialUDPCandidatesDropsAddressesTheBudgetCannotHold(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("four.test"), 443)
	candidates := []net.Destination{udpDest("192.0.2.1"), udpDest("2001:db8::1"), udpDest("192.0.2.2"), udpDest("2001:db8::2")}
	shares := func(budget time.Duration) ([]time.Duration, error) {
		var shares []time.Duration
		_, err := DialUDPCandidates(context.Background(), dest, candidates, budget, func(ctx context.Context, _ net.Destination) (int, error) {
			deadline, _ := ctx.Deadline()
			shares = append(shares, time.Until(deadline).Round(100*time.Millisecond))
			return 0, errors.New("refused")
		})
		return shares, err
	}
	got, err := shares(10 * time.Second)
	if want := []time.Duration{3300 * time.Millisecond, 5 * time.Second, 10 * time.Second}; !slices.Equal(got, want) || strings.Contains(err.Error(), "2001:db8::2") {
		t.Fatalf("a 10 s budget gave the addresses %v, want %v: %v", got, want, err)
	}
	got, _ = shares(16 * time.Second)
	if want := []time.Duration{4 * time.Second, 5300 * time.Millisecond, 8 * time.Second, 16 * time.Second}; !slices.Equal(got, want) {
		t.Fatalf("a 16 s budget gave the addresses %v, want %v", got, want)
	}
}

// An attempt that ends early leaves the rest of its share to the addresses after it.
func TestDialUDPCandidatesHandsAnEarlyFailuresTimeOn(t *testing.T) {
	t.Cleanup(SetUDPAttemptMinimum(100 * time.Millisecond))
	dest := net.UDPDestination(net.DomainAddress("early.test"), 443)
	const budget = 900 * time.Millisecond
	var shares []time.Duration
	DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("::1"), udpDest("127.0.0.1"), udpDest("::2")}, budget, func(ctx context.Context, candidate net.Destination) (int, error) {
		deadline, _ := ctx.Deadline()
		shares = append(shares, time.Until(deadline))
		if candidate.Address.IP().Equal(net.ParseIP("::1")) {
			return 0, errors.New("unreachable")
		}
		<-ctx.Done()
		return 0, ctx.Err()
	})
	if len(shares) != 3 || shares[1] < budget/2-50*time.Millisecond || shares[2] < budget/2-100*time.Millisecond {
		t.Fatalf("a %v budget after an instant failure gave the others %v", budget, shares)
	}
}

// A server that answered and refused the client ends the attempts at once with its own error:
// another address of the same server would refuse it too. Nothing is remembered for the name.
func TestDialUDPCandidatesStopsAtAServerThatAnswered(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("refusing.test"), 443)
	refused := errors.New("auth failed")
	var tried []string
	_, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("127.0.0.1"), udpDest("::1")}, 10*time.Second, func(_ context.Context, candidate net.Destination) (int, error) {
		tried = append(tried, candidate.NetAddr())
		return 0, UDPServerAnswered(refused)
	})
	if err != refused || strings.Join(tried, " ") != "127.0.0.1:443" {
		t.Fatalf("tried %v, error %v", tried, err)
	}
	if working := udpWorkingIP(dest); working != nil {
		t.Fatalf("a refused dial remembered %v", working)
	}
	_, err = DialUDPCandidates(context.Background(), dest, []net.Destination{dest}, 10*time.Second, func(context.Context, net.Destination) (int, error) {
		return 0, UDPServerAnswered(refused)
	})
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
		})
		if calls != 1 || err != failed {
			t.Fatalf("%s: %d attempts, error %v", dest, calls, err)
		}
	}
}

// A caller that gives up ends the attempts: the next address is not tried.
func TestDialUDPCandidatesStopsWhenTheCallerGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dest := net.UDPDestination(net.DomainAddress("given-up.test"), 443)
	var tried int
	_, err := DialUDPCandidates(ctx, dest, []net.Destination{udpDest("::1"), udpDest("127.0.0.1")}, 10*time.Second, func(ctx context.Context, _ net.Destination) (int, error) {
		tried++
		cancel()
		return 0, ctx.Err()
	})
	if tried != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("%d attempts after the caller gave up: %v", tried, err)
	}
}

// A handshake that began before the network changed does not tell the new network which
// address works.
func TestDialUDPCandidatesStartedBeforeAPathChangeRemembersNothing(t *testing.T) {
	dest := net.UDPDestination(net.DomainAddress("old-path.test"), 443)
	_, err := DialUDPCandidates(context.Background(), dest, []net.Destination{udpDest("::1"), udpDest("127.0.0.1")}, 10*time.Second, func(_ context.Context, candidate net.Destination) (int, error) {
		if candidate.Address.IP().Equal(net.ParseIP("::1")) {
			return 0, errors.New("refused")
		}
		SocketPathChanged()
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withCoreResolver(&coreResolver{ips: []string{"::1", "127.0.0.1"}})
	if got := candidatesOf(t, ctx, dest, &SocketConfig{DomainStrategy: DomainStrategy_USE_IP}); got != "[::1]:443 127.0.0.1:443" {
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
