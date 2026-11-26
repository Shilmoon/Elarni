package xdns

import (
	"encoding/binary"
	"io"
	gonet "net"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet/finalmask"
)

// echoDNSOverTCP answers every length-prefixed query on a connection with the query itself.
func echoDNSOverTCP(t *testing.T) gonet.Listener {
	ln, err := gonet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				for {
					var size [2]byte
					if _, err := io.ReadFull(conn, size[:]); err != nil {
						return
					}
					query := make([]byte, binary.BigEndian.Uint16(size[:]))
					if _, err := io.ReadFull(conn, query); err != nil {
						return
					}
					if _, err := conn.Write(append(size[:], query...)); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln
}

// The TCP resolver frames each query with its length, as DNS over TCP is, and its workers
// answer every one; the old resolver wrote the length as an int, which binary.Write refuses.
func TestTCPResolverFramesQueriesAndAnswersEach(t *testing.T) {
	ln := echoDNSOverTCP(t)
	dialer := &finalmask.Dialer{DialTCP: func(dest net.Destination) (net.Conn, error) {
		return gonet.Dial("tcp", dest.NetAddr())
	}}
	resolver, err := NewTCPResolver(&ResolverProto{Type: "tcp", Addr: ln.Addr().String(), Worker: 2}, dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()

	queries := []string{"first query", "second query", "third query"}
	go func() {
		for _, q := range queries {
			resolver.Send([]byte(q))
		}
	}()
	answers := make(chan string)
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := resolver.Read(b)
			if err != nil {
				close(answers)
				return
			}
			answers <- string(b[:n])
		}
	}()
	var got []string
	for range queries {
		select {
		case a := <-answers:
			got = append(got, a)
		case <-time.After(5 * time.Second):
			t.Fatalf("answers %q of %q", got, queries)
		}
	}
	sort.Strings(got)
	want := append([]string(nil), queries...)
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("answers %q, want %q", got, want)
		}
	}
	if addr := resolver.Addr(); addr == nil || addr.String() != ln.Addr().String() {
		t.Fatalf("address %v, want %s", addr, ln.Addr())
	}

	resolver.Close()
	select {
	case _, open := <-answers:
		if open {
			t.Fatal("an answer after Close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read still waits after Close")
	}
}

// silentDNSOverTCP takes connections and reads their queries. The connections that answers tells by their
// number, from 0 in the order they came, answer each query with itself; the others answer none, tell of
// each query on queried and of their end on ended.
type silentDNSOverTCP struct {
	addr    string
	queried chan struct{}
	ended   chan struct{}
}

func newSilentDNSOverTCP(t *testing.T, answers func(conn int) bool) *silentDNSOverTCP {
	ln, err := gonet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &silentDNSOverTCP{addr: ln.Addr().String(), queried: make(chan struct{}, 16), ended: make(chan struct{}, 16)}
	var mu sync.Mutex
	var conns []gonet.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			conn.Close()
		}
	})
	go func() {
		for i := 0; ; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			answer := answers(i)
			go func() {
				for {
					var size [2]byte
					if _, err := io.ReadFull(conn, size[:]); err != nil {
						if !answer {
							s.ended <- struct{}{}
						}
						return
					}
					query := make([]byte, binary.BigEndian.Uint16(size[:]))
					if _, err := io.ReadFull(conn, query); err != nil {
						return
					}
					if !answer {
						s.queried <- struct{}{}
						continue
					}
					if _, err := conn.Write(append(size[:], query...)); err != nil {
						return
					}
				}
			}()
		}
	}()
	return s
}

func loopbackTCPDialer() *finalmask.Dialer {
	return &finalmask.Dialer{DialTCP: func(dest net.Destination) (net.Conn, error) {
		return gonet.Dial("tcp", dest.NetAddr())
	}}
}

func await(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

// A worker that waited for an answer did not see Close, which closed neither its connection nor Read: the
// xdns client above went on reading after it was closed, and the connection stayed open.
func TestTCPResolverCloseEndsAWorkerWaitingForAnAnswer(t *testing.T) {
	server := newSilentDNSOverTCP(t, func(int) bool { return false })
	resolver, err := NewTCPResolver(&ResolverProto{Type: "tcp", Addr: server.addr, Worker: 1}, loopbackTCPDialer())
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() {
		_, err := resolver.Read(make([]byte, 4096))
		read <- err
	}()
	resolver.Send([]byte("a query that gets no answer"))
	await(t, server.queried, "the query never reached the server")

	closed := make(chan struct{})
	go func() {
		resolver.Close()
		close(closed)
	}()
	await(t, closed, "Close still waits")
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("an answer after Close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read still waits after Close")
	}
	await(t, server.ended, "the connection is still open after Close")
}

// Each worker waited for its answer with no end, so every query that the resolver dropped took a worker out
// for good, and with all of them out Send waited for ever. A worker now gives up on the answer after its
// timeout, closes the connection and takes the next query on a new one.
func TestTCPResolverWorkerGivesUpOnAnAnswerThatNeverComes(t *testing.T) {
	// the first connection answers nothing, the ones after it answer every query
	server := newSilentDNSOverTCP(t, func(conn int) bool { return conn > 0 })
	resolver, err := newTCPResolver(&ResolverProto{Type: "tcp", Addr: server.addr, Worker: 1}, loopbackTCPDialer(), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	answers := make(chan string, 1)
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := resolver.Read(b)
			if err != nil {
				return
			}
			answers <- string(b[:n])
		}
	}()

	resolver.Send([]byte("dropped"))
	await(t, server.queried, "the first query never reached the server")
	go resolver.Send([]byte("answered"))
	select {
	case a := <-answers:
		if a != "answered" {
			t.Fatalf("answer %q", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the one worker still waits for the answer to its first query")
	}
	await(t, server.ended, "the connection without an answer is still open")
}

func TestTCPResolverNeedsAWorker(t *testing.T) {
	if _, err := NewTCPResolver(&ResolverProto{Type: "tcp", Addr: "127.0.0.1:53"}, &finalmask.Dialer{}); err == nil {
		t.Fatal("a resolver without workers would take no query")
	}
}
