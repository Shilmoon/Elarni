package share

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	socksproxy "golang.org/x/net/proxy"
)

type SubscriptionFetchRequest struct {
	URL              string   `json:"url"`
	ApprovedChain    []string `json:"approvedChain"`
	InstallationID   string   `json:"installationId"`
	DeviceSalt       string   `json:"deviceSalt,omitempty"` // set when the user resets this subscription's device ID
	FallbackHosts    []string `json:"fallbackHosts,omitempty"`
	FallbackURL      string   `json:"fallbackURL,omitempty"` // the provider's backup URL from an earlier response (Happ's fallback-url)
	DeviceOS         string   `json:"deviceOS,omitempty"`
	DeviceVersion    string   `json:"deviceVersion,omitempty"`
	DeviceModel      string   `json:"deviceModel,omitempty"`
	UserAgent        string   `json:"userAgent,omitempty"`
	EncounteredChain []string `json:"encounteredChain,omitempty"`
	ConnectAddress   string   `json:"connectAddress,omitempty"`
	HTTPHost         string   `json:"httpHost,omitempty"`
	FragmentLength   string   `json:"fragmentLength,omitempty"`
	FragmentInterval string   `json:"fragmentInterval,omitempty"`
	// The ETag and Last-Modified of the copy already stored; the server answers 304 when it is current.
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	// Proxy is the running core's loopback SOCKS inbound. Android excludes the
	// app's own sockets from its VPN, so fetches use it to stay in the tunnel.
	Proxy string `json:"proxy,omitempty"`
}

var privateNetworks = func() []netip.Prefix {
	var result []netip.Prefix
	// Special-use and non-global ranges, including NAT64 below.
	for _, cidr := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/96", "64:ff9b:1::/48", "100::/64", "100:1::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "5f00::/16", "fc00::/7", "fe80::/10", "ff00::/8"} {
		result = append(result, netip.MustParsePrefix(cidr))
	}
	// RFC 9780 dummy addresses are not globally reachable destinations.
	result = append(result, netip.MustParsePrefix("100:0:0:1::/64"))
	return result
}()

var nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")

func addressRisk(address netip.Addr) string {
	// A prefix contains no address with a zone, and a link-local answer carries one.
	address = address.Unmap().WithZone("")
	if nat64Prefix.Contains(address) {
		bytes := address.As16()
		address = netip.AddrFrom4([4]byte(bytes[12:]))
	}
	if address.IsLoopback() {
		return "loopback_target"
	}
	for _, prefix := range privateNetworks {
		if prefix.Contains(address) {
			return "private_target"
		}
	}
	return ""
}

// localNetworks are the addresses this device reaches and a server does not: loopback, RFC 1918,
// link-local and unique-local. Narrower than addressRisk on purpose: NAT64, CGNAT and the
// documentation ranges are no home or office network, and a fetch to them stays in the tunnel.
var localNetworks = func() []netip.Prefix {
	var result []netip.Prefix
	for _, cidr := range []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "::1/128", "fc00::/7", "fe80::/10"} {
		result = append(result, netip.MustParsePrefix(cidr))
	}
	return result
}()

func localAddress(address netip.Addr) bool {
	address = address.Unmap().WithZone("")
	return !fakeDNSAddress(address) && slices.ContainsFunc(localNetworks, func(prefix netip.Prefix) bool { return prefix.Contains(address) })
}

// localDials remembers, for one fetch, the local addresses that did not answer from this device.
// Every redirect hop and every mirror attempt dials anew, and each would wait for them again.
type localDials struct {
	sync.Mutex
	failed map[netip.Addr]bool
	// One address timed out: a network that drops the packets drops them for the others too.
	// This device itself drops none, so its own addresses are still dialed.
	silent bool
}

func (l *localDials) try(address netip.Addr) bool {
	l.Lock()
	defer l.Unlock()
	return !l.failed[address] && (!l.silent || address.Unmap().IsLoopback())
}

// fail notes that the address did not answer. waited says the dial had its whole wait: one cut
// short by the fetch's own deadline (a mirror's share of the time) says nothing about the network.
func (l *localDials) fail(address netip.Addr, err error, waited bool) {
	l.Lock()
	defer l.Unlock()
	if l.failed == nil {
		l.failed = map[netip.Addr]bool{}
	}
	if !waited {
		return
	}
	l.failed[address] = true
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() && !address.Unmap().IsLoopback() {
		l.silent = true
	}
}

func feedTarget(text string) (*url.URL, error) {
	u, err := url.Parse(text)
	if err != nil || len(text) == 0 || len(text) > 16*1024 || strings.ContainsAny(text, "\r\n\x00") || u.User != nil || u.Fragment != "" || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("invalid_target")
	}
	if port := u.Port(); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return nil, errors.New("invalid_target")
		}
	}
	// An http URL may name a domain: every http hop, a redirect too, carries the cleartext_http
	// risk, which the user approves for that URL.
	return u, nil
}

func approvalFingerprint(target string, risks []string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "elarni.subscription-safety.v1")
	for _, value := range append([]string{target}, risks...) {
		_ = binary.Write(hash, binary.BigEndian, uint64(len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// deviceID is per origin, so providers cannot link one device across subscriptions.
func deviceID(installationID, salt string, u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	hash := hmac.New(sha256.New, []byte(installationID))
	message := u.Scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
	if salt != "" {
		message += "\n" + salt
	}
	_, _ = io.WriteString(hash, message)
	return hex.EncodeToString(hash.Sum(nil))
}

// SubscriptionDeviceID is the X-HWID a fetch of the subscription's own URL sends.
func SubscriptionDeviceID(request SubscriptionFetchRequest) (string, error) {
	u, err := feedTarget(request.URL)
	if err != nil || request.InstallationID == "" {
		return "", errors.New("invalid_target")
	}
	if err := request.ValidateOptions(); err != nil {
		return "", err
	}
	if request.HTTPHost != "" {
		u.Host = request.HTTPHost
	}
	return deviceID(request.InstallationID, request.DeviceSalt, u), nil
}

// appVersion mirrors pubspec.yaml; cmd/elarni-core's fetch test fails when they differ.
const appVersion = "0.6.4"

func FetchSubscription(ctx context.Context, request SubscriptionFetchRequest) (any, error) {
	return FetchResource(ctx, request, 4*1024*1024, 30*time.Second, false)
}

func FetchResource(ctx context.Context, request SubscriptionFetchRequest, limit int64, timeout time.Duration, binaryBody bool) (any, error) {
	return fetchResource(ctx, request, limit, timeout, binaryBody, "")
}

// FetchResourceToFile is FetchResource for a binary resource written to destination as it arrives (Elarni):
// a geofile never sits in memory whole. The result has its size as "bytes" instead of a body. A failed or
// unchanged download leaves no file.
func FetchResourceToFile(ctx context.Context, request SubscriptionFetchRequest, limit int64, timeout time.Duration, destination string) (any, error) {
	if destination == "" {
		return nil, errors.New("invalid_fetch_options")
	}
	return fetchResource(ctx, request, limit, timeout, true, destination)
}

func fetchResource(ctx context.Context, request SubscriptionFetchRequest, limit int64, timeout time.Duration, binaryBody bool, destination string) (any, error) {
	resolve := systemLookup
	if request.Proxy != "" {
		resolve = tunnelLookup(request.Proxy)
	}
	return fetchResourceUsing(ctx, request, resolve, func() *http.Transport { return &http.Transport{} }, limit, timeout, binaryBody, destination)
}

// LookupHost resolves host the way a fetch without a proxy does (Elarni).
func LookupHost(ctx context.Context, host string) ([]netip.Addr, error) {
	addresses, err := systemLookup(ctx, "ip", host)
	// Go answers IPv4 as IPv4-mapped IPv6 here, which callers would take for IPv6.
	for i := range addresses {
		addresses[i] = addresses[i].Unmap()
	}
	return addresses, err
}

// fakeDNSPools are Xray's default FakeDNS ranges, which Elarni's profiles use.
var fakeDNSPools = []netip.Prefix{netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("fc00::/18")}

func fakeDNSAddress(address netip.Addr) bool {
	return slices.ContainsFunc(fakeDNSPools, func(pool netip.Prefix) bool { return pool.Contains(address.Unmap()) })
}

// localName reports a name only this device's network can answer: mDNS (.local) and a
// name without a dot, which a router, hosts file or search domain may know and no public
// resolver does.
func localName(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host != "" && (strings.HasSuffix(host, ".local") || !strings.Contains(host, "."))
}

// systemLookup asks the system resolver. While a VPN with FakeDNS carries the app's own
// traffic (Apple, the desktop TUN), that answers with FakeDNS placeholders, which a fetch
// refuses as private; the name is then looked up over TLS, through that VPN. Without an
// answer there the placeholder stays: approved, it still connects through the VPN.
func systemLookup(ctx context.Context, network, host string) ([]netip.Addr, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, network, host)
	if err != nil || !slices.ContainsFunc(addresses, fakeDNSAddress) {
		return addresses, err
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	direct := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", tunnelDNS)
	}
	if answers, err := dotResolver(direct).LookupNetIP(lookup, network, host); err == nil && len(answers) > 0 {
		return answers, nil
	}
	return addresses, nil
}

// localLookup resolves a name of the local network. The system resolver comes first. It may
// not know the name: under the desktop TUN with FakeDNS it answers every name with a
// placeholder, and the core's resolver (built without cgo) never asks mDNS. A .local name is
// then asked on the local links. Without an answer there the name has none.
func localLookup(ctx context.Context, network, host string) ([]netip.Addr, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, network, host)
	if err == nil && len(addresses) > 0 && !slices.ContainsFunc(addresses, fakeDNSAddress) {
		return addresses, nil
	}
	if answers := mdnsLookup(ctx, network, host); len(answers) > 0 {
		return answers, nil
	}
	if err == nil && len(addresses) > 0 {
		// Only placeholders: connecting to one sends the name to the server, which cannot know it.
		return nil, errors.New("dns_failed")
	}
	return addresses, err
}

// mdnsGroup is where a .local name is asked (RFC 6762).
var mdnsGroup = "224.0.0.251:5353"

// mdnsWait is how long a .local name waits for its first answer; tests shorten it.
var mdnsWait = time.Second

// mdnsLookup asks the local links for the IPv4 addresses of a .local name, once, and waits a
// second for the first answer, or until the fetch is cancelled. IPv6 answers there are link-local, which a URL cannot dial.
func mdnsLookup(ctx context.Context, network, host string) []netip.Addr {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	name, err := dnsmessage.NewName(host + ".")
	group, groupErr := net.ResolveUDPAddr("udp4", mdnsGroup)
	if !strings.HasSuffix(host, ".local") || network == "ip6" || err != nil || groupErr != nil {
		return nil
	}
	// The top bit of the class asks for an answer to this socket, not to the group.
	query, err := (&dnsmessage.Message{Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET | 1<<15}}}).Pack()
	if err != nil {
		return nil
	}
	var sockets []*net.UDPConn
	if !group.IP.IsMulticast() {
		if socket, err := net.ListenUDP("udp4", nil); err == nil {
			sockets = append(sockets, socket)
		}
	} else if interfaces, err := net.Interfaces(); err == nil { // Refused on Android, whose resolver asks mDNS itself.
		for _, link := range interfaces {
			if link.Flags&net.FlagUp == 0 || link.Flags&net.FlagMulticast == 0 || link.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
				continue
			}
			addresses, _ := link.Addrs()
			for _, address := range addresses {
				network, ok := address.(*net.IPNet)
				if !ok || network.IP.To4() == nil {
					continue
				}
				// The default route may be the tunnel's: the question leaves by this link.
				socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: network.IP})
				if err != nil {
					continue
				}
				if ipv4.NewPacketConn(socket).SetMulticastInterface(&link) != nil {
					_ = socket.Close()
					continue
				}
				sockets = append(sockets, socket)
				break
			}
		}
	}
	deadline := time.Now().Add(mdnsWait)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	for _, socket := range sockets {
		_ = socket.SetDeadline(deadline)
	}
	// A cancelled fetch does not wait for the answer.
	stop := context.AfterFunc(ctx, func() {
		for _, socket := range sockets {
			_ = socket.SetDeadline(time.Now())
		}
	})
	defer stop()
	answers := make(chan []netip.Addr, len(sockets))
	for _, socket := range sockets {
		go func() {
			defer socket.Close()
			var found []netip.Addr
			defer func() { answers <- found }()
			if _, err := socket.WriteToUDP(query, group); err != nil {
				return
			}
			buffer := make([]byte, 1500)
			for len(found) == 0 {
				size, _, err := socket.ReadFromUDP(buffer)
				if err != nil {
					return
				}
				var message dnsmessage.Message
				if message.Unpack(buffer[:size]) != nil || !message.Response {
					continue
				}
				for _, answer := range message.Answers {
					if record, ok := answer.Body.(*dnsmessage.AResource); ok && strings.EqualFold(answer.Header.Name.String(), name.String()) {
						found = append(found, netip.AddrFrom4(record.A))
					}
				}
			}
		}()
	}
	for range sockets {
		if found := <-answers; len(found) > 0 {
			return found
		}
	}
	return nil
}

// fallbackAttemptLimit caps each attempt but the last when a feed has mirrors.
var fallbackAttemptLimit = 10 * time.Second

// errUnreachable marks a fetch whose every address refused or timed out the connection.
var errUnreachable = errors.New("provider_unreachable")

// mirrorFailures leave the next mirror its turn: a blocked or overloaded domain
// answers this way, while a removed subscription or a device limit does not.
var mirrorFailures = map[string]bool{"fetch_failed": true, "dns_failed": true, "provider_unreachable": true,
	"provider_error": true, "rate_limited": true, "subscription_denied": true}

// tunnelDNS answers lookups for fetches that go through the core, over TLS: the core
// hands every port-53 query from its listeners to Elarni's DNS, whose FakeDNS answers
// are placeholders that a fetch refuses as private.
var tunnelDNS = "1.1.1.1:853"

// tunnelDNSRoots verifies tunnelDNS; nil uses the system roots.
var tunnelDNSRoots *x509.CertPool

// tunnelPlainDNS is asked through the proxy when tunnelDNS can't be reached, as a network
// or a profile rule may block DNS over TLS: the core's own DNS answers it, which is right
// unless FakeDNS is on.
var tunnelPlainDNS = "1.1.1.1:53"

func tunnelLookup(proxy string) func(context.Context, string, string) ([]netip.Addr, error) {
	return func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		if localName(host) {
			// The server's resolvers cannot know it, and the name never leaves the local network.
			return localLookup(ctx, network, host)
		}
		secure, cancel := context.WithTimeout(ctx, 5*time.Second)
		addresses, err := tunnelResolver(proxy).LookupNetIP(secure, network, host)
		cancel()
		var missing *net.DNSError
		if err == nil || errors.As(err, &missing) && missing.IsNotFound {
			return addresses, err
		}
		plain := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return socksDial(ctx, proxy, tunnelPlainDNS)
		}}
		return plain.LookupNetIP(ctx, network, host)
	}
}

// tunnelResolver keeps a proxied fetch's lookups in the tunnel: Android excludes
// the app's own sockets from its VPN, so the system resolver would leak the host.
func tunnelResolver(proxy string) *net.Resolver {
	return dotResolver(func(ctx context.Context) (net.Conn, error) { return socksDial(ctx, proxy, tunnelDNS) })
}

func socksDial(ctx context.Context, proxy, address string) (net.Conn, error) {
	proxyURL, err := url.Parse(proxy)
	if err != nil {
		return nil, err
	}
	socks, err := socksproxy.FromURL(proxyURL, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	return socks.(socksproxy.ContextDialer).DialContext(ctx, "tcp", address)
}

// dotResolver looks names up at tunnelDNS over TLS, on connections from dial.
func dotResolver(dial func(context.Context) (net.Conn, error)) *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		host, _, _ := net.SplitHostPort(tunnelDNS)
		client := tls.Client(conn, &tls.Config{ServerName: host, RootCAs: tunnelDNSRoots, MinVersion: tls.VersionTLS12})
		if err := client.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		// A stream connection makes Go's resolver speak DNS over TCP, the framing DNS over TLS uses.
		return client, nil
	}}
}

func fetchResourceUsing(parent context.Context, request SubscriptionFetchRequest, resolve func(context.Context, string, string) ([]netip.Addr, error), newTransport func() *http.Transport, limit int64, timeout time.Duration, binaryBody bool, destination string) (any, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := request.ValidateOptions(); err != nil {
		return nil, err
	}
	if len(request.FallbackHosts) > 8 {
		return nil, errors.New("invalid_target")
	}
	targets := []string{request.URL}
	for _, host := range request.FallbackHosts {
		if len(host) == 0 || len(host) > 253 {
			return nil, errors.New("invalid_target")
		}
		target, err := SubscriptionMigrationURL(request.URL, nil, map[string]string{"new-domain": host})
		if err != nil {
			return nil, errors.New("invalid_target")
		}
		if target != "" {
			targets = append(targets, target)
		}
	}
	// Last, after the mirrors; an unusable stored fallback is skipped rather than failing the fetch.
	if fallback := SubscriptionFallbackURL(request.URL, nil, map[string]string{"fallback-url": request.FallbackURL}); fallback != "" {
		targets = append(targets, fallback)
	}
	chain := append([]string{}, request.EncounteredChain...)
	// What a review shows for the fingerprints this fetch added to the chain.
	destinations := []string{}
	local := &localDials{}
	var result any
	var err error
	for index, target := range targets {
		request.URL = target
		attempt := ctx
		var stop context.CancelFunc
		if index < len(targets)-1 {
			deadline, _ := ctx.Deadline()
			attempt, stop = context.WithTimeout(ctx, min(fallbackAttemptLimit, time.Until(deadline)/time.Duration(len(targets)-index)))
		}
		result, err = fetchResourceAttempt(attempt, request, targets[0], resolve, newTransport, limit, binaryBody, destination, &chain, &destinations, local)
		if stop != nil {
			stop()
		}
		if err != nil && errors.Is(ctx.Err(), context.Canceled) {
			return nil, errors.New("fetch_cancelled")
		}
		if err == nil || ctx.Err() != nil {
			return result, err
		}
		if !mirrorFailures[err.Error()] {
			return nil, err
		}
	}
	return nil, err
}

func fetchResourceAttempt(ctx context.Context, request SubscriptionFetchRequest, identity string, resolve func(context.Context, string, string) ([]netip.Addr, error), newTransport func() *http.Transport, limit int64, binaryBody bool, destination string, encountered, destinations *[]string, local *localDials) (any, error) {
	u, err := feedTarget(request.URL)
	if err != nil {
		return nil, err
	}
	chain := *encountered
	defer func() { *encountered = chain }()
	origin := u.Scheme + "://" + u.Host
	// The connect address and HTTP Host name the subscription's own host; a mirror and the fallback URL are
	// other hosts, reached as they are named.
	own := false
	if base, err := feedTarget(identity); err == nil {
		own = origin == base.Scheme+"://"+base.Host
	}
	for redirects := 0; redirects <= 5; redirects++ {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, errors.New("fetch_cancelled")
			}
			return nil, errors.New("fetch_failed")
		}
		custom := origin == u.Scheme+"://"+u.Host
		override := custom && own
		dialHost := u.Hostname()
		if override && request.ConnectAddress != "" {
			dialHost = strings.Trim(request.ConnectAddress, "[]")
		}
		addresses, err := resolve(ctx, "ip", dialHost)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("dns_failed")
		}
		risks := []string{}
		if u.Scheme == "http" {
			risks = append(risks, "cleartext_http")
		}
		for _, risk := range []string{"loopback_target", "private_target"} {
			for _, address := range addresses {
				if addressRisk(address) == risk {
					risks = append(risks, risk)
					break
				}
			}
		}
		if len(risks) > 0 {
			if len(chain) >= 64 {
				return nil, errors.New("invalid_target")
			}
			target := u.String()
			if override && (request.ConnectAddress != "" || request.HTTPHost != "") {
				target += "\nconnect=" + request.ConnectAddress + "\nhost=" + request.HTTPHost
			}
			fingerprint := approvalFingerprint(target, risks)
			position := len(chain)
			chain = append(chain, fingerprint)
			named := u.Scheme + "://" + u.Host
			if dialHost != u.Hostname() {
				// The risk is the connect address's; the URL alone would hide it.
				named += " → " + dialHost
			}
			*destinations = append(*destinations, named)
			if position >= len(request.ApprovedChain) || request.ApprovedChain[position] != fingerprint {
				// The chain is fingerprints, which name nothing to the user: destinations does.
				return map[string]any{"warning": map[string]any{"riskCodes": risks, "chain": chain, "destinations": *destinations}}, nil
			}
		}
		dial := (&net.Dialer{Timeout: 5 * time.Second}).DialContext
		// A server cannot reach this device or its local network, so an address there is dialed
		// from here first. The tunnel still gets its turn: the feed may be in the server's network.
		// Only for an address the URL gives, or a name the local network answered. What a
		// resolver in the tunnel answered is the server's view, and stays in the tunnel: a
		// provider's feed inside its own network must not reach a device that has the same
		// address here. An address written in the URL can be either one; this device's wins,
		// after the user approved a private destination.
		var localDial func(context.Context, string, string) (net.Conn, error)
		_, literal := netip.ParseAddr(dialHost)
		if request.Proxy != "" && (literal == nil || localName(dialHost)) {
			localDial = (&net.Dialer{Timeout: 2 * time.Second}).DialContext
		}
		if request.Proxy != "" {
			// Only the connection goes through the core; addresses were evaluated above.
			proxyURL, _ := url.Parse(request.Proxy)
			socks, err := socksproxy.FromURL(proxyURL, &net.Dialer{Timeout: 5 * time.Second})
			if err != nil {
				return nil, errors.New("invalid_fetch_options")
			}
			dial = socks.(socksproxy.ContextDialer).DialContext
		}
		transport := newTransport()
		transport.DisableKeepAlives = true
		// Dial only evaluated addresses. TLS still verifies the original hostname.
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			for _, ip := range addresses {
				target := net.JoinHostPort(ip.String(), port)
				conn, err := net.Conn(nil), errUnreachable
				if localDial != nil && localAddress(ip) && local.try(ip) {
					if conn, err = localDial(ctx, "tcp", target); err != nil {
						local.fail(ip, err, ctx.Err() == nil)
					}
				}
				if err != nil {
					conn, err = dial(ctx, "tcp", target)
				}
				if err == nil {
					if request.FragmentLength != "" && u.Scheme == "https" {
						length, _ := fetchRange(request.FragmentLength, 1, 65535)
						interval, _ := fetchRange(request.FragmentInterval, 0, 1000)
						return &fragmentHelloConn{Conn: conn, ctx: ctx, length: length, interval: interval}, nil
					}
					return conn, nil
				}
			}
			return nil, errUnreachable
		}
		client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, errors.New("invalid_target")
		}
		req.Header.Set("User-Agent", "Elarni/"+appVersion+" (Xray; "+request.DeviceOS+")")
		if request.UserAgent != "" {
			req.Header.Set("User-Agent", request.UserAgent)
		}
		identityURL := *u
		if custom {
			// Mirrors of one panel present the subscription's own device ID.
			if base, err := url.Parse(identity); err == nil {
				identityURL = *base
			}
		}
		if custom && request.HTTPHost != "" {
			identityURL.Host = request.HTTPHost
			if override {
				req.Host = request.HTTPHost
			}
		}
		if request.InstallationID != "" {
			req.Header.Set("X-HWID", deviceID(request.InstallationID, request.DeviceSalt, &identityURL))
			req.Header.Set("X-Device-OS", request.DeviceOS)
			req.Header.Set("X-Ver-OS", request.DeviceVersion)
			req.Header.Set("X-Device-Model", request.DeviceModel)
		}
		if request.ETag != "" {
			req.Header.Set("If-None-Match", request.ETag)
		}
		if request.LastModified != "" {
			req.Header.Set("If-Modified-Since", request.LastModified)
		}
		response, err := client.Do(req)
		if err != nil {
			transport.CloseIdleConnections()
			if errors.Is(err, errUnreachable) {
				return nil, errUnreachable
			}
			return nil, errors.New("fetch_failed")
		}
		if response.StatusCode == 301 || response.StatusCode == 302 || response.StatusCode == 303 || response.StatusCode == 307 || response.StatusCode == 308 {
			next, err := response.Location()
			response.Body.Close()
			transport.CloseIdleConnections()
			if err != nil {
				return nil, errors.New("fetch_failed")
			}
			// A Location fragment is client-side only and never sent.
			next.Fragment, next.RawFragment = "", ""
			u, err = feedTarget(next.String())
			if err != nil {
				return nil, err
			}
			continue
		}
		if response.StatusCode == http.StatusNotModified && (request.ETag != "" || request.LastModified != "") {
			response.Body.Close()
			transport.CloseIdleConnections()
			return map[string]any{"notModified": true, "url": u.String(), "approvedChain": chain, "usedOverrides": custom}, nil
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			response.Body.Close()
			transport.CloseIdleConnections()
			if strings.EqualFold(response.Header.Get("X-Hwid-Limit"), "true") || strings.EqualFold(response.Header.Get("X-Hwid-Max-Devices-Reached"), "true") {
				return nil, errors.New("hwid_limit")
			}
			if strings.EqualFold(response.Header.Get("X-Hwid-Not-Supported"), "true") {
				return nil, errors.New("hwid_required")
			}
			switch {
			case response.StatusCode == 404 || response.StatusCode == 410:
				return nil, errors.New("subscription_unavailable")
			case response.StatusCode == 401 || response.StatusCode == 403:
				return nil, errors.New("subscription_denied")
			case response.StatusCode == 429:
				return nil, errors.New("rate_limited")
			case response.StatusCode >= 500:
				return nil, errors.New("provider_error")
			}
			return nil, errors.New("fetch_failed")
		}
		if response.ContentLength > limit {
			// Elarni: refuse before downloading the limit only to discard it.
			response.Body.Close()
			transport.CloseIdleConnections()
			return nil, errors.New("input_too_large")
		}
		var body []byte
		var size int64
		if destination != "" {
			size, err = saveBody(response.Body, destination, limit)
		} else {
			body, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
			size = int64(len(body))
		}
		response.Body.Close()
		transport.CloseIdleConnections()
		if errors.Is(err, errSaveFailed) {
			return nil, errSaveFailed
		}
		if err != nil {
			return nil, errors.New("fetch_failed")
		}
		if size > limit {
			if destination != "" {
				os.Remove(destination)
			}
			return nil, errors.New("input_too_large")
		}
		// Remnawave answers a device over its limit with HTTP 200, no feed and the reason in a header.
		if destination == "" && len(bytes.TrimSpace(body)) == 0 {
			if strings.EqualFold(response.Header.Get("X-Hwid-Limit"), "true") || strings.EqualFold(response.Header.Get("X-Hwid-Max-Devices-Reached"), "true") {
				return nil, errors.New("hwid_limit")
			}
			if strings.EqualFold(response.Header.Get("X-Hwid-Not-Supported"), "true") {
				return nil, errors.New("hwid_required")
			}
		}
		if !binaryBody && !utf8.Valid(body) {
			return nil, errors.New("invalid_feed")
		}
		headers := map[string]string{}
		if _, present := response.Header["Hide-Url"]; present {
			headers["hide-url"] = response.Header.Get("Hide-Url")
		}
		for _, key := range []string{"Subscription-Userinfo", "Profile-Title", "Profile-Update-Interval", "Announce", "Routing", "Routing-Enable", "Hide-Settings", "X-Hwid-Active", "Support-Url", "Profile-Web-Page-Url", "Banner-Text", "Banner-Button-Text", "Banner-Button-Url", "Premium-Url", "New-Url", "New-Domain", "Fallback-Url"} {
			if value := response.Header.Get(key); len(value) > 0 && len(value) <= 16*1024 {
				headers[strings.ToLower(key)] = value
			}
		}
		var content any = string(body)
		if binaryBody {
			content = body
		}
		result := map[string]any{"body": content, "url": u.String(), "headers": headers, "approvedChain": chain, "usedOverrides": custom}
		if destination != "" {
			delete(result, "body")
			result["bytes"] = size
		}
		for key, header := range map[string]string{"etag": "ETag", "lastModified": "Last-Modified"} {
			if value := response.Header.Get(header); validatorValue(value) {
				result[key] = value
			}
		}
		return result, nil
	}
	return nil, errors.New("fetch_failed")
}

var errSaveFailed = errors.New("assets_unavailable")

// saveBody writes up to limit+1 bytes of body to path, replacing an earlier attempt's file; a
// failure leaves no file. A disk failure is errSaveFailed, which no mirror can fix.
func saveBody(body io.Reader, path string, limit int64) (int64, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return 0, errSaveFailed
	}
	sink := &fileSink{file: file}
	n, err := io.Copy(sink, io.LimitReader(body, limit+1))
	if sink.err != nil {
		err = errSaveFailed
	}
	if err == nil && n <= limit && file.Sync() != nil {
		err = errSaveFailed
	}
	if file.Close() != nil && err == nil {
		err = errSaveFailed
	}
	if err != nil {
		os.Remove(path)
	}
	return n, err
}

// fileSink records a write failure, which io.Copy reports like a read one.
type fileSink struct {
	file *os.File
	err  error
}

func (s *fileSink) Write(p []byte) (int, error) {
	n, err := s.file.Write(p)
	if err != nil {
		s.err = err
	}
	return n, err
}

// validatorValue holds an ETag or Last-Modified worth storing and sending back as one header line.
func validatorValue(value string) bool {
	return value != "" && len(value) <= 1024 && !strings.ContainsAny(value, "\r\n\x00")
}

// Host overrides are deliberately host-only: the subscription URL owns the
// scheme, TLS server name, port and token. Redirects to another origin drop them.
func (request SubscriptionFetchRequest) ValidateOptions() error {
	invalid := errors.New("invalid_fetch_options")
	if len(request.ApprovedChain) > 64 || len(request.EncounteredChain) > 64 {
		return invalid
	}
	for _, value := range []string{request.ETag, request.LastModified} {
		if value != "" && !validatorValue(value) {
			return invalid
		}
	}
	for _, fingerprint := range append(append([]string{}, request.ApprovedChain...), request.EncounteredChain...) {
		if len(fingerprint) != 64 {
			return invalid
		}
		if _, err := hex.DecodeString(fingerprint); err != nil {
			return invalid
		}
	}
	for _, host := range append(append([]string{}, request.FallbackHosts...), request.ConnectAddress, request.HTTPHost) {
		if host == "" {
			continue
		}
		if len(host) > 253 || strings.IndexFunc(host, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:[]", c))
		}) >= 0 {
			return invalid
		}
		if _, err := SubscriptionMigrationURL("https://validation.invalid/feed", nil, map[string]string{"new-domain": host}); err != nil {
			return invalid
		}
	}
	if len(request.FallbackHosts) > 8 {
		return invalid
	}
	for _, host := range request.FallbackHosts {
		if host == "" {
			return invalid
		}
	}
	if request.FragmentLength != "" {
		if _, err := fetchRange(request.FragmentLength, 1, 65535); err != nil {
			return invalid
		}
		if _, err := fetchRange(request.FragmentInterval, 0, 1000); err != nil {
			return invalid
		}
	} else if request.FragmentInterval != "" {
		return invalid
	}
	if (request.ConnectAddress != "" || request.HTTPHost != "" || request.FragmentLength != "") && request.URL != "" {
		if u, err := feedTarget(request.URL); err != nil || u.Scheme != "https" {
			return invalid
		}
	}
	for _, header := range []string{request.DeviceOS, request.DeviceVersion, request.DeviceModel, request.UserAgent} {
		if len(header) > 256 || strings.ContainsAny(header, "\r\n\x00") {
			return invalid
		}
	}
	if request.Proxy != "" {
		u, err := url.Parse(request.Proxy)
		if err != nil || u.Scheme != "socks5" || u.Port() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return invalid
		}
		if address, err := netip.ParseAddr(u.Hostname()); err != nil || !address.IsLoopback() {
			return invalid
		}
	}
	return nil
}

func fetchRange(value string, low, high int) ([2]int, error) {
	parts := strings.Split(value, "-")
	var result [2]int
	if len(parts) < 1 || len(parts) > 2 {
		return result, errors.New("invalid_range")
	}
	for index, part := range parts {
		if part == "" || strings.IndexFunc(part, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
			return result, errors.New("invalid_range")
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < low || n > high {
			return result, errors.New("invalid_range")
		}
		result[index] = n
	}
	if len(parts) == 1 {
		result[1] = result[0]
	}
	if result[1] < result[0] {
		return result, errors.New("invalid_range")
	}
	return result, nil
}

type fragmentHelloConn struct {
	net.Conn
	ctx              context.Context
	length, interval [2]int
	written          bool
}

func (c *fragmentHelloConn) Write(data []byte) (int, error) {
	first := !c.written
	c.written = true
	if !first || len(data) < 6 || data[0] != 22 || data[5] != 1 {
		return c.Conn.Write(data)
	}
	written := 0
	for written < len(data) {
		if err := c.ctx.Err(); err != nil {
			return written, err
		}
		size := min(c.length[0]+rand.IntN(c.length[1]-c.length[0]+1), len(data)-written)
		n, err := c.Conn.Write(data[written : written+size])
		written += n
		if err != nil {
			return written, err
		}
		if n != size {
			return written, io.ErrShortWrite
		}
		if written < len(data) {
			delay := time.Duration(c.interval[0]+rand.IntN(c.interval[1]-c.interval[0]+1)) * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-c.ctx.Done():
				timer.Stop()
				return written, c.ctx.Err()
			case <-timer.C:
			}
		}
	}
	return written, nil
}
