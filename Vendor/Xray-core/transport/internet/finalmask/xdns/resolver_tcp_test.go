package xdns

import (
	"encoding/binary"
	"io"
	gonet "net"
	"sort"
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

func TestTCPResolverNeedsAWorker(t *testing.T) {
	if _, err := NewTCPResolver(&ResolverProto{Type: "tcp", Addr: "127.0.0.1:53"}, &finalmask.Dialer{}); err == nil {
		t.Fatal("a resolver without workers would take no query")
	}
}
