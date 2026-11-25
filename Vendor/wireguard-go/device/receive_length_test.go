package device

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/ipv6"
	"golang.zx2c4.com/wireguard/tun/tuntest"
)

// Upstream b3bc704ef0: an IPv6 payload length near 65535 overflowed uint16 when the 40-byte
// header was added, so the receiver cut the packet to fewer bytes than an IPv6 header and wrote
// it to the tunnel instead of dropping it.
func TestReceiveDropsIPv6PacketWhoseLengthOverflows(t *testing.T) {
	goroutineLeakCheck(t)
	pair := genTestPair(t, false)
	ips := [2]netip.Addr{netip.MustParseAddr("fd00::1"), netip.MustParseAddr("fd00::2")}
	for i := range pair {
		for key := range pair[i].dev.peers.keyMap {
			if err := pair[i].dev.IpcSet(uapiCfg("public_key", hex.EncodeToString(key[:]), "allowed_ip", ips[i^1].String()+"/128")); err != nil {
				t.Fatal(err)
			}
		}
	}

	// From pair[1] to pair[0], longer than its header but claiming 65535 bytes of payload.
	pkt := make([]byte, ipv6.HeaderLen+20)
	pkt[0] = 6 << 4
	binary.BigEndian.PutUint16(pkt[IPv6offsetPayloadLength:], 0xffff)
	pkt[6] = 59 // no next header
	pkt[7] = 64
	copy(pkt[IPv6offsetSrc:], ips[1].AsSlice())
	copy(pkt[IPv6offsetDst:], ips[0].AsSlice())
	pair[1].tun.Outbound <- pkt
	// The same peer's packets arrive in order, so the ping behind it is the first one out.
	ping := tuntest.Ping(pair[0].ip, pair[1].ip)
	pair[1].tun.Outbound <- ping

	select {
	case got := <-pair[0].tun.Inbound:
		if !bytes.Equal(got, ping) {
			t.Fatalf("tunnel got a %d-byte packet before the ping, want the overflowing packet dropped", len(got))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ping did not transit")
	}
}
