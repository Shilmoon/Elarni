// Package shadowquic is the outbound for ShadowQUIC servers.
//
// The protocol is spoken by Elarni's copy of the ShadowQUIC client (Rust,
// Vendor/shadowquic), not by this package. The outbound runs one client, which listens
// for SOCKS5 on a loopback port, and hands every flow to it. The client is part of the
// same process where the core is built with cgo and the program has registered it
// (Android, iOS, the macOS network extension); elsewhere it is the `elarni-shadowquic`
// program next to the core, started as a child.
//
// The client owns its socket to the server, so the outbound tells it what the core's
// own sockets get from internet.RequiredSocketController: the routing mark (Linux) or
// the interface (Windows) that keeps the socket out of the tunnel.
package shadowquic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal"
	"github.com/xtls/xray-core/common/task"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/socks"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
)

// startRetry is how long a failed start is reported to later flows without another try:
// a missing program or a refused configuration would otherwise be retried per flow.
const startRetry = 2 * time.Second

type Client struct {
	config   *ClientConfig
	policy   policy.Manager
	up, down stats.Counter

	mu       sync.Mutex
	current  *upstream
	failed   error
	failedAt time.Time
	unwatch  func()
	closed   bool
	// moved is set when the sockets of the core would now be bound to another interface.
	moved atomic.Bool
}

// upstream is one running ShadowQUIC client.
type upstream struct {
	native  nativeClient
	address string // of its SOCKS5 listener
	login   *protocol.MemoryUser
	binding binding
}

// nativeClient is a running ShadowQUIC client, in this process or a child.
type nativeClient interface {
	Port() uint16
	Running() bool
	Stop()
}

// binding is what keeps a socket to the server out of the tunnel. The zero value is a
// socket the system routes by itself.
type binding struct {
	mark  uint32 // Linux routing mark
	index uint32 // Windows interface index
}

// settings is what the ShadowQUIC client reads (native/elarni-shadowquic).
type settings struct {
	Server             string   `json:"server"`
	ServerName         string   `json:"serverName"`
	Username           string   `json:"username"`
	Password           string   `json:"password"`
	ALPN               []string `json:"alpn,omitempty"`
	CongestionControl  string   `json:"congestionControl,omitempty"`
	ZeroRTT            bool     `json:"zeroRtt"`
	UDPOverStream      bool     `json:"udpOverStream"`
	InitialMTU         uint32   `json:"initialMtu,omitempty"`
	MinMTU             uint32   `json:"minMtu,omitempty"`
	FwMark             uint32   `json:"fwMark,omitempty"`
	BindInterfaceIndex uint32   `json:"bindInterfaceIndex,omitempty"`
	SocksUsername      string   `json:"socksUsername"`
	SocksPassword      string   `json:"socksPassword"`
}

func NewClient(ctx context.Context, config *ClientConfig) (*Client, error) {
	if config.Address == nil || config.Port == 0 || config.Port > 65535 || config.Username == "" || config.Password == "" || config.ServerName == "" {
		return nil, errors.New("invalid ShadowQUIC client configuration")
	}
	v := core.MustFromContext(ctx)
	c := &Client{config: config, policy: v.GetFeature(policy.ManagerType()).(policy.Manager)}
	if h := session.FullHandlerFromContext(ctx); h != nil && h.Tag() != "" {
		sm := v.GetFeature(stats.ManagerType()).(stats.Manager)
		c.up = sm.GetCounter("outbound>>>" + h.Tag() + ">>>traffic>>>uplink")
		c.down = sm.GetCounter("outbound>>>" + h.Tag() + ">>>traffic>>>downlink")
	}
	return c, nil
}

var _ proxy.LargePacketCarrier = (*Client)(nil)

// CarriesLargePackets implements proxy.LargePacketCarrier.
func (c *Client) CarriesLargePackets() {}

// Process implements proxy.Outbound. The dialer is unused: the ShadowQUIC client owns
// the socket to the server, and the hop to the client is loopback.
func (c *Client) Process(ctx context.Context, link *transport.Link, _ internet.Dialer) error {
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 || !outbounds[len(outbounds)-1].Target.IsValid() {
		return errors.New("ShadowQUIC target not specified")
	}
	ob := outbounds[len(outbounds)-1]
	ob.Name, ob.CanSpliceCopy = "shadowquic", 3
	target := ob.Target

	conn, login, err := c.connect(ctx)
	if err != nil {
		return errors.New("ShadowQUIC client unavailable").Base(err)
	}
	defer conn.Close()

	request := &protocol.RequestHeader{Version: 5, Command: protocol.RequestCommandTCP, Address: target.Address, Port: target.Port, User: login}
	if target.Network == xnet.Network_UDP {
		request.Command = protocol.RequestCommandUDP
	}
	p := c.policy.ForLevel(0)
	_ = conn.SetDeadline(time.Now().Add(p.Timeouts.Handshake))
	relay, err := socks.ClientHandshake(request, conn, conn)
	if err != nil {
		return errors.New("ShadowQUIC client refused the flow").Base(err)
	}
	_ = conn.SetDeadline(time.Time{})

	var detached context.Context
	var detach context.CancelFunc
	if session.TimeoutOnlyFromContext(ctx) {
		detached, detach = context.WithCancel(context.Background())
	}
	ctx, cancel := context.WithCancel(ctx)
	timer := signal.CancelAfterInactivity(ctx, func() {
		cancel()
		if detach != nil {
			detach()
		}
	}, p.Timeouts.ConnectionIdle)

	var reader buf.Reader
	var writer buf.Writer
	if request.Command == protocol.RequestCommandTCP {
		reader, writer = buf.NewReader(conn), buf.NewWriter(conn)
	} else {
		// The client answers with the loopback port that takes this flow's datagrams.
		datagrams, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(relay.Port)})
		if err != nil {
			return errors.New("failed to reach the ShadowQUIC client").Base(err)
		}
		defer datagrams.Close()
		counted := c.count(datagrams)
		reader, writer = &socks.UDPReader{Reader: counted}, &socks.UDPWriter{Writer: counted, Request: request}
	}
	requestDone := func() error {
		defer timer.SetTimeout(p.Timeouts.DownlinkOnly)
		return buf.Copy(link.Reader, writer, buf.UpdateActivity(timer))
	}
	responseDone := func() error {
		defer timer.SetTimeout(p.Timeouts.UplinkOnly)
		return buf.Copy(reader, link.Writer, buf.UpdateActivity(timer))
	}
	if detached != nil {
		ctx = detached
	}
	if err := task.Run(ctx, requestDone, task.OnSuccess(responseDone, task.Close(link.Writer))); err != nil {
		return errors.New("connection ends").Base(err)
	}
	return nil
}

// count adds a flow's bytes to the outbound's traffic counters, when it has them.
func (c *Client) count(conn net.Conn) net.Conn {
	if c.up == nil && c.down == nil {
		return conn
	}
	return &stat.CounterConnection{Connection: conn, ReadCounter: c.down, WriteCounter: c.up}
}

// connect opens a connection to the client's listener, starting the client first when
// none runs. A listener that is gone is replaced once.
func (c *Client) connect(ctx context.Context) (net.Conn, *protocol.MemoryUser, error) {
	for attempt := 0; ; attempt++ {
		up, err := c.upstream(ctx)
		if err != nil {
			return nil, nil, err
		}
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp4", up.address)
		if err == nil {
			return c.count(conn), up.login, nil
		}
		if attempt > 0 || ctx.Err() != nil {
			return nil, nil, err
		}
		c.discard(up)
	}
}

// upstream returns the running client, started or replaced as needed.
func (c *Client) upstream(ctx context.Context) (*upstream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, os.ErrClosed
	}
	if c.unwatch == nil {
		c.unwatch = internet.OnSocketPathChange(func() { c.moved.Store(true) })
	}
	if up := c.current; up != nil && up.native.Running() {
		if !c.moved.Swap(false) {
			return up, nil
		}
		// The client's socket follows the routes unless it is bound to an interface that
		// is no longer the one to use; an unreadable path keeps the client that runs.
		if _, now, err := c.locate(ctx); err != nil || now == up.binding {
			return up, nil
		}
		errors.LogInfo(ctx, "the path to the ShadowQUIC server moved to another interface; restarting the client")
	}
	if c.current != nil {
		c.current.native.Stop()
		c.current = nil
	}
	if c.failed != nil && time.Since(c.failedAt) < startRetry {
		return nil, c.failed
	}
	up, err := c.start(ctx)
	if err != nil {
		c.failed, c.failedAt = err, time.Now()
		return nil, err
	}
	c.failed, c.current = nil, up
	return up, nil
}

func (c *Client) start(ctx context.Context) (*upstream, error) {
	server, binding, err := c.locate(ctx)
	if err != nil {
		return nil, errors.New("failed to resolve the ShadowQUIC server").Base(err)
	}
	var secret [32]byte
	common.Must2(rand.Read(secret[:]))
	username, password := hex.EncodeToString(secret[:16]), hex.EncodeToString(secret[16:])
	description, err := json.Marshal(settings{Server: server.String(), ServerName: c.config.ServerName,
		Username: c.config.Username, Password: c.config.Password, ALPN: c.config.Alpn,
		CongestionControl: c.config.CongestionControl, ZeroRTT: c.config.ZeroRtt, UDPOverStream: c.config.UdpOverStream,
		InitialMTU: c.config.InitialMtu, MinMTU: c.config.MinMtu, FwMark: binding.mark, BindInterfaceIndex: binding.index,
		SocksUsername: username, SocksPassword: password})
	if err != nil {
		return nil, err
	}
	native, err := startClient(description)
	if err != nil {
		return nil, err
	}
	return &upstream{native: native, address: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(native.Port()))),
		login:   &protocol.MemoryUser{Account: &socks.Account{Username: username, Password: password}},
		binding: binding}, nil
}

// locate resolves the server the way the core's own sockets do and reads, from a socket
// the core would use for it, what keeps that socket out of the tunnel.
func (c *Client) locate(ctx context.Context) (netip.AddrPort, binding, error) {
	destination := xnet.UDPDestination(c.config.Address.AsAddress(), xnet.Port(c.config.Port))
	conn, err := internet.DialSystem(context.WithoutCancel(ctx), destination, nil)
	if err != nil {
		return netip.AddrPort{}, binding{}, err
	}
	defer conn.Close()
	wrapper, ok := conn.(*xnet.PacketConnWrapper)
	if !ok {
		return netip.AddrPort{}, binding{}, errors.New("unexpected socket type")
	}
	resolved, ok := wrapper.Dest.(*net.UDPAddr)
	if !ok {
		return netip.AddrPort{}, binding{}, errors.New("unexpected address type")
	}
	server := resolved.AddrPort()
	server = netip.AddrPortFrom(server.Addr().Unmap().WithZone(""), server.Port())
	var bound binding
	if socket, ok := wrapper.PacketConn.(interface {
		SyscallConn() (syscall.RawConn, error)
	}); ok {
		if raw, err := socket.SyscallConn(); err == nil {
			bound = readBinding(raw, server.Addr().Is6())
		}
	}
	return server, bound, nil
}

// discard stops a client whose listener no longer accepts.
func (c *Client) discard(up *upstream) {
	c.mu.Lock()
	if c.current == up {
		c.current = nil
	}
	c.mu.Unlock()
	up.native.Stop()
}

func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	up, unwatch := c.current, c.unwatch
	c.current, c.unwatch = nil, nil
	c.mu.Unlock()
	if unwatch != nil {
		unwatch()
	}
	if up != nil {
		up.native.Stop()
	}
	return nil
}

// startClient is how the outbound starts its client; tests pick one of the two ways.
var startClient = startNative

// startNative runs a client that is part of this process, or else the client program.
func startNative(description []byte) (nativeClient, error) {
	if client, err := startLinked(description); err != errNotLinked {
		return client, err
	}
	return startProgram(description)
}

var errNotLinked = errors.New("the ShadowQUIC client is not part of this program")

// logLine passes a warning or an error of the client to the core's log.
func logLine(line string) {
	errors.LogWarning(context.Background(), "shadowquic: ", line)
}

func init() {
	common.Must(common.RegisterConfig((*ClientConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return NewClient(ctx, config.(*ClientConfig))
	}))
}
