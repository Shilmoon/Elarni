package tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/protocol/tls/cert"
)

// Regression (uTLS issue 402): the "firefox" hello offers X25519MLKEM768, X25519 and P-256 key
// shares but kept the private key of the first classical share only, so a server that picks
// P-256 ended the handshake with "tls: invalid server key share".
func TestFirefoxFingerprintCompletesWithAServerThatPicksP256(t *testing.T) {
	certificate, _ := cert.MustGenerate(nil, cert.DNSNames("fixture.test"), cert.CommonName("fixture.test"))
	pair, err := tls.X509KeyPair(certificate.ToPEM())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates:     []tls.Certificate{pair},
		MinVersion:       tls.VersionTLS13,
		CurvePreferences: []tls.CurveID{tls.CurveP256},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var curve tls.CurveID
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		server := conn.(*tls.Conn)
		if err := server.Handshake(); err != nil {
			served <- err
			return
		}
		curve = server.ConnectionState().CurveID
		_, err = io.Copy(conn, io.LimitReader(conn, 4))
		served <- err
	}()
	raw, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := UClient(raw, &tls.Config{ServerName: "fixture.test", RootCAs: roots}, GetFingerprint("firefox")).(*UConn)
	defer client.Close()
	client.SetDeadline(time.Now().Add(10 * time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.HandshakeContext(ctx); err != nil {
		t.Fatalf("firefox hello against a P-256 server: %v", err)
	}
	if version := client.ConnectionState().Version; version != tls.VersionTLS13 {
		t.Fatalf("negotiated version %x, want TLS 1.3", version)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(client, reply); err != nil || string(reply) != "ping" {
		t.Fatalf("echo %q: %v", reply, err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if curve != tls.CurveP256 {
		t.Fatalf("server picked %v, want P-256", curve)
	}
}
