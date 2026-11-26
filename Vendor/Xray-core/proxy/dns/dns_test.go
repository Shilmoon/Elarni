package dns

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/pipe"
	"golang.org/x/net/dns/dnsmessage"
)

type staticClient struct {
	ips []net.IP
	err error
}

func (staticClient) Type() interface{} { return dns.ClientType() }
func (staticClient) Start() error      { return nil }
func (staticClient) Close() error      { return nil }
func (c staticClient) LookupIP(string, dns.IPOption) ([]net.IP, uint32, error) {
	return c.ips, 60, c.err
}

type capturedMessages chan *buf.Buffer

func (c capturedMessages) WriteMessage(b *buf.Buffer) error {
	c <- b
	return nil
}

// Elarni: a lookup that failed for any reason but an rcode or an empty answer (every resolver timed
// out, the proxy was down) got no reply at all, so the app's stub resolver waited out its own
// timeout, often 5 s, before it tried again or failed over.
func TestHijackedQueriesAlwaysGetAnAnswer(t *testing.T) {
	for _, c := range []struct {
		name    string
		qType   dnsmessage.Type
		client  staticClient
		rCode   dnsmessage.RCode
		answers int
	}{
		{"resolvers timed out", dnsmessage.TypeA, staticClient{err: errors.New("returning nil for domain example.test").Base(errors.New("record not found"))}, dnsmessage.RCodeServerFailure, 0},
		{"NXDOMAIN", dnsmessage.TypeA, staticClient{err: errors.New("returning nil for domain example.test").Base(dns.RCodeError(dnsmessage.RCodeNameError))}, dnsmessage.RCodeNameError, 0},
		{"no records of this type", dnsmessage.TypeA, staticClient{err: dns.ErrEmptyResponse}, dnsmessage.RCodeSuccess, 0},
		// With queryStrategy UseIPv4 every AAAA query ends here: empty, not failed, so apps use the A answer.
		{"AAAA when only IPv4 is used", dnsmessage.TypeAAAA, staticClient{err: dns.ErrEmptyResponse}, dnsmessage.RCodeSuccess, 0},
		{"answered", dnsmessage.TypeA, staticClient{ips: []net.IP{net.ParseIP("192.0.2.1")}}, dnsmessage.RCodeSuccess, 1},
	} {
		messages := make(capturedMessages, 1)
		timer := signal.CancelAfterInactivity(context.Background(), func() {}, time.Minute)
		(&Handler{client: c.client}).handleIPQuery(7, c.qType, "example.test.", messages, timer, 512)
		timer.SetTimeout(0)
		select {
		case b := <-messages:
			var msg dnsmessage.Message
			common.Must(msg.Unpack(b.Bytes()))
			if msg.ID != 7 || msg.RCode != c.rCode || len(msg.Answers) != c.answers || len(msg.Questions) != 1 {
				t.Errorf("%s: id %d rcode %v with %d answers and %d questions, want id 7 rcode %v with %d answers",
					c.name, msg.ID, msg.RCode, len(msg.Answers), len(msg.Questions), c.rCode, c.answers)
			}
		default:
			t.Errorf("%s: no answer", c.name)
		}
	}
}

type udpDialer struct{ upstream *net.UDPAddr }

func (d udpDialer) Dial(context.Context, net.Destination) (stat.Connection, error) {
	return net.DialUDP("udp", nil, d.upstream)
}
func (udpDialer) DestIpAddress() net.IP                                 { return nil }
func (udpDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

type capturedWrites chan buf.MultiBuffer

func (c capturedWrites) WriteMultiBuffer(mb buf.MultiBuffer) error {
	c <- mb
	return nil
}

// The response side now reads only after a query dialed upstream; a query sent upstream must still
// get the upstream's reply back.
func TestQueriesSentUpstreamGetTheUpstreamReply(t *testing.T) {
	upstream, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	common.Must(err)
	defer upstream.Close()
	go func() {
		packet := make([]byte, 512)
		n, from, err := upstream.ReadFromUDP(packet)
		if err == nil {
			upstream.WriteToUDP(append([]byte("reply:"), packet[:n]...), from)
		}
	}()
	h := &Handler{client: staticClient{}, timeout: time.Minute, rules: []*DNSRule{{action: RuleAction_Direct}}}
	reader, writer := pipe.New(pipe.WithoutSizeLimit())
	defer writer.Close()
	replies := make(capturedWrites, 1)
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: net.UDPDestination(net.ParseAddress("192.0.2.53"), 53)}})
	go h.Process(ctx, &transport.Link{Reader: reader, Writer: replies}, udpDialer{upstream.LocalAddr().(*net.UDPAddr)})
	query := testQuery()
	common.Must(writer.WriteMultiBuffer(buf.MergeBytes(nil, query)))
	select {
	case mb := <-replies:
		if got := mb.String(); got != "reply:"+string(query) {
			t.Fatalf("reply %q, want the upstream's", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply from upstream")
	}
}

func testQuery() []byte {
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 1, RecursionDesired: true})
	common.Must(builder.StartQuestions())
	common.Must(builder.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("example.test."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}))
	query, err := builder.Finish()
	common.Must(err)
	return query
}

type oneQueryReader struct{ query []byte }

func (r *oneQueryReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if r.query == nil {
		return nil, io.EOF
	}
	b := buf.New()
	b.Write(r.query)
	r.query = nil
	return buf.MultiBuffer{b}, nil
}

// queryFor is an A or AAAA query for example.test. that announces ednsSize in an OPT record, or
// carries none when ednsSize is 0.
func queryFor(qType dnsmessage.Type, ednsSize int) []byte {
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 9, RecursionDesired: true})
	common.Must(builder.StartQuestions())
	common.Must(builder.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("example.test."), Type: qType, Class: dnsmessage.ClassINET}))
	if ednsSize != 0 {
		common.Must(builder.StartAdditionals())
		var opt dnsmessage.ResourceHeader
		common.Must(opt.SetEDNS0(ednsSize, dnsmessage.RCodeSuccess, false))
		common.Must(builder.OPTResource(opt, dnsmessage.OPTResource{}))
	}
	query, err := builder.Finish()
	common.Must(err)
	return query
}

// answerFor sends query to h on a flow of its own, as an app's stub resolver does over network,
// and returns the answer without its TCP length.
func answerFor(t *testing.T, h *Handler, network net.Network, query []byte) []byte {
	t.Helper()
	target := net.UDPDestination(net.ParseAddress("192.0.2.53"), 53)
	if network == net.Network_TCP {
		target = net.TCPDestination(net.ParseAddress("192.0.2.53"), 53)
		query = append(binary.BigEndian.AppendUint16(nil, uint16(len(query))), query...)
	}
	replies := make(capturedWrites, 1)
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: target}})
	go h.Process(ctx, &transport.Link{Reader: &oneQueryReader{query: query}, Writer: replies}, nil)
	select {
	case mb := <-replies:
		answer := []byte(mb.String())
		buf.ReleaseMulti(mb)
		if network == net.Network_TCP {
			if len(answer) < 2 || int(binary.BigEndian.Uint16(answer)) != len(answer)-2 {
				t.Fatalf("TCP answer of %d bytes does not match its length", len(answer))
			}
			answer = answer[2:]
		}
		return answer
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
		return nil
	}
}

// Elarni: every address went into one UDP answer of up to 8 KiB, but a stub resolver takes 512
// bytes, or the EDNS size it announces, so a name with about 30 or more addresses got an answer
// the asker dropped or cut.
func TestUDPAnswersKeepTheAddressesThatFitWhatTheAskerTakes(t *testing.T) {
	ipv4, ipv6 := make([]net.IP, 60), make([]net.IP, 60)
	for i := range 60 {
		ipv4[i] = net.ParseIP(fmt.Sprintf("198.51.100.%d", i+1))
		ipv6[i] = net.ParseIP(fmt.Sprintf("2001:db8::%x", i+1))
	}
	for _, c := range []struct {
		name     string
		network  net.Network
		qType    dnsmessage.Type
		ednsSize int
		limit    int // 0: every address
	}{
		{"60 A records without EDNS", net.Network_UDP, dnsmessage.TypeA, 0, 512},
		{"60 A records with EDNS 1232", net.Network_UDP, dnsmessage.TypeA, 1232, 0}, // 990 bytes
		{"60 AAAA records with EDNS 1232", net.Network_UDP, dnsmessage.TypeAAAA, 1232, 1232},
		{"EDNS above 1232 counts as 1232", net.Network_UDP, dnsmessage.TypeAAAA, 4096, 1232},
		{"EDNS below 512 counts as 512", net.Network_UDP, dnsmessage.TypeA, 100, 512},
		{"60 A records over TCP", net.Network_TCP, dnsmessage.TypeA, 0, 0},
		{"60 AAAA records over TCP", net.Network_TCP, dnsmessage.TypeAAAA, 1232, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			ips, recordSize := ipv4, 16
			if c.qType == dnsmessage.TypeAAAA {
				ips, recordSize = ipv6, 28
			}
			h := &Handler{client: staticClient{ips: ips}, timeout: time.Minute}
			answer := answerFor(t, h, c.network, queryFor(c.qType, c.ednsSize))
			var msg dnsmessage.Message
			if err := msg.Unpack(answer); err != nil {
				t.Fatalf("answer of %d bytes does not parse: %v", len(answer), err)
			}
			if msg.ID != 9 || msg.RCode != dnsmessage.RCodeSuccess || msg.Truncated || len(msg.Questions) != 1 {
				t.Fatalf("id %d rcode %v truncated %t with %d questions", msg.ID, msg.RCode, msg.Truncated, len(msg.Questions))
			}
			for i, record := range msg.Answers {
				var got net.IP
				switch body := record.Body.(type) {
				case *dnsmessage.AResource:
					got = body.A[:]
				case *dnsmessage.AAAAResource:
					got = body.AAAA[:]
				}
				if !got.Equal(ips[i]) {
					t.Fatalf("answer %d is %v, want %v: the addresses in their order", i, got, ips[i])
				}
			}
			n := len(msg.Answers)
			if c.limit == 0 {
				if n != len(ips) {
					t.Fatalf("%d of %d addresses in %d bytes, want all", n, len(ips), len(answer))
				}
				return
			}
			if len(answer) > c.limit || n == 0 || n >= len(ips) {
				t.Fatalf("%d of %d addresses in %d bytes, want some within %d bytes", n, len(ips), len(answer), c.limit)
			}
			if len(answer)+recordSize <= c.limit {
				t.Fatalf("%d addresses in %d bytes leave room for another within %d", n, len(answer), c.limit)
			}
		})
	}
}

// A short answer is the message the handler always sent: same header, question and records.
func TestShortAnswersAreUnchanged(t *testing.T) {
	ips := []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2"), net.ParseIP("192.0.2.3")}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 9, RecursionAvailable: true, RecursionDesired: true, Response: true, Authoritative: true})
	builder.EnableCompression()
	common.Must(builder.StartQuestions())
	common.Must(builder.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("example.test."), Class: dnsmessage.ClassINET, Type: dnsmessage.TypeA}))
	common.Must(builder.StartAnswers())
	for _, ip := range ips {
		var r dnsmessage.AResource
		copy(r.A[:], ip.To4())
		common.Must(builder.AResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("example.test."), Class: dnsmessage.ClassINET, TTL: 60}, r))
	}
	want, err := builder.Finish()
	common.Must(err)
	h := &Handler{client: staticClient{ips: ips}, timeout: time.Minute}
	for _, network := range []net.Network{net.Network_UDP, net.Network_TCP} {
		for _, ednsSize := range []int{0, 1232} {
			if got := answerFor(t, h, network, queryFor(dnsmessage.TypeA, ednsSize)); !bytes.Equal(got, want) {
				t.Errorf("%v with EDNS size %d: answer %x, want %x", network, ednsSize, got, want)
			}
		}
	}
}

// One app query answered from the local resolver, on a flow of its own as stub resolvers send them.
func BenchmarkLocallyAnsweredQueryFlow(b *testing.B) {
	query := testQuery()
	h := &Handler{client: staticClient{ips: []net.IP{net.ParseIP("192.0.2.1")}}, timeout: time.Minute}
	log.ReplaceWithSeverityLogger(log.Severity_Warning) // the app's default level
	b.ReportAllocs()
	for b.Loop() {
		ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: net.UDPDestination(net.ParseAddress("192.0.2.53"), 53)}})
		link := &transport.Link{Reader: &oneQueryReader{query: query}, Writer: buf.Discard}
		h.Process(ctx, link, nil) // ends with the flow's canceled context, as every flow does
	}
}
