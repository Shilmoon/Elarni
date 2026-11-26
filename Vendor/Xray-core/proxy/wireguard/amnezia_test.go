package wireguard

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

// Both engines carry TCP, UDP and tunnel DNS over IPv4 and IPv6 between loopback peers, again after
// the device went down, and not after the handler closed.
func TestWireGuardEnginesCarryTCPAndUDP(t *testing.T) {
	for _, amnezia := range []bool{false, true} {
		t.Run(fmt.Sprintf("amnezia=%t", amnezia), func(t *testing.T) {
			settings := map[string]string{}
			if amnezia {
				// Full-width headers catch the reserved-byte corruption regression.
				obf := "jc=4\njmin=40\njmax=80\ns1=16\ns2=17\ns3=18\ns4=19\nh1=100000-100100\nh2=200000-200100\nh3=300000-300100\nh4=400000-400100\nheader_protection_key=" + hex.EncodeToString(bytes.Repeat([]byte{0x5a}, 32)) + "\ncontent_padding_addition=4-8\nrandom_trailers=true\ndisable_cookies=true\n"
				for _, line := range strings.Split(obf, "\n") {
					if key, value, ok := strings.Cut(line, "="); ok {
						settings[key] = value
					}
				}
			}
			f := newTunnelFixture(t, fixtureOptions{amnezia: amnezia, settings: settings})
			handler, clientNet, serverStack := f.handler, f.clientNet, f.serverStack
			if err := handler.init(context.Background()); err != nil {
				t.Fatal(err)
			}
			client := handler.dev
			if err := client.Up(); err != nil {
				t.Fatal(err)
			}
			dnsAddress, dnsFamily := convertToFullAddr(netip.MustParseAddrPort("10.29.0.1:53"))
			dnsSocket, err := gonet.DialUDP(serverStack, &dnsAddress, nil, dnsFamily)
			if err != nil {
				t.Fatal(err)
			}
			defer dnsSocket.Close()
			dnsDone := make(chan struct{})
			go func() {
				defer close(dnsDone)
				buffer := make([]byte, 2048)
				for {
					n, from, err := dnsSocket.ReadFrom(buffer)
					if err != nil {
						return
					}
					var request dns.Msg
					if request.Unpack(buffer[:n]) != nil {
						continue
					}
					response := new(dns.Msg)
					response.SetReply(&request)
					for _, q := range request.Question {
						if q.Qtype == dns.TypeA {
							response.Answer = append(response.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("192.0.2.29")})
						}
						if q.Qtype == dns.TypeAAAA {
							response.Answer = append(response.Answer, &dns.AAAA{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60}, AAAA: net.ParseIP("2001:db8::29")})
						}
					}
					packet, err := response.Pack()
					if err == nil {
						dnsSocket.WriteTo(packet, from)
					}
				}
			}()
			defer func() { dnsSocket.Close(); <-dnsDone }()
			for cycle := 0; cycle < 2; cycle++ {
				if cycle > 0 {
					if err := client.Down(); err != nil {
						t.Fatal(err)
					}
					if err := handler.init(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				addresses, err := clientNet.LookupContextHost(ctx, fmt.Sprintf("fixture-%d.test", cycle))
				cancel()
				if err != nil || !slices.Contains(addresses, "192.0.2.29") || !slices.Contains(addresses, "2001:db8::29") {
					t.Fatalf("configured tunnel DNS failed: %v %v", addresses, err)
				}
				for _, address := range []string{fmt.Sprintf("10.29.0.1:%d", 18181+cycle), fmt.Sprintf("[fd29::1]:%d", 18181+cycle)} {
					remote := netip.MustParseAddrPort(address)
					full, family := convertToFullAddr(remote)
					listener, err := gonet.ListenTCP(serverStack, full, family)
					if err != nil {
						t.Fatal(err)
					}
					defer listener.Close()
					go func() {
						conn, err := listener.Accept()
						if err != nil {
							return
						}
						defer conn.Close()
						conn.SetDeadline(time.Now().Add(10 * time.Second))
						io.Copy(conn, conn)
					}()
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					connection, err := clientNet.DialContextTCPAddrPort(ctx, remote)
					cancel()
					if err != nil {
						t.Fatal(err)
					}
					checkEcho(t, connection)
					udpServer, err := gonet.DialUDP(serverStack, &full, nil, family)
					if err != nil {
						t.Fatal(err)
					}
					defer udpServer.Close()
					go func() {
						udpServer.SetDeadline(time.Now().Add(10 * time.Second))
						bytes := make([]byte, 2048)
						n, from, err := udpServer.ReadFrom(bytes)
						if err == nil {
							udpServer.WriteTo(bytes[:n], from)
						}
					}()
					udpClient, err := clientNet.DialUDPAddrPort(netip.AddrPort{}, remote)
					if err != nil {
						t.Fatal(err)
					}
					checkEcho(t, udpClient)
				}
			}
			if err := handler.Close(); err != nil {
				t.Fatal(err)
			}
			if err := handler.init(context.Background()); err == nil {
				t.Fatal("closed WireGuard handler restarted")
			}
		})
	}
}

func checkEcho(t *testing.T, connection net.Conn) {
	t.Helper()
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(10 * time.Second))
	payload := []byte("elarni encrypted peer fixture")
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != string(payload) {
		t.Fatal("tunneled payload changed")
	}
}
