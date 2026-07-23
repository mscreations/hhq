// Copyright (C) 2026 Jon Shaulis
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package email

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// selfSignedCertFor generates an in-memory self-signed certificate valid for
// the given IP, for use with tls.Listen in tests - avoids any dependency on
// files on disk or a real CA.
func selfSignedCertFor(t *testing.T, ip net.IP) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: ip.String()},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{ip},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("building tls.Certificate: %v", err)
	}
	return cert
}

// startFakeSMTPServerTLS is the implicit-TLS (port-465-style) sibling of
// startFakeSMTPServer - same protocol handling (see fakeSMTPServer.handleConn
// in smtp_test.go), but the listener itself terminates TLS, so it exercises
// Sender.sendImplicitTLS end-to-end instead of the STARTTLS/plaintext path
// smtp.SendMail takes. This is a direct regression test for the previously
// untested implicit-TLS branch flagged in CLAUDE.md's known gaps.
func startFakeSMTPServerTLS(t *testing.T) (*fakeSMTPServer, *x509.CertPool) {
	t.Helper()
	cert := selfSignedCertFor(t, net.ParseIP("127.0.0.1"))
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("starting fake tls smtp listener: %v", err)
	}
	srv := &fakeSMTPServer{listener: ln}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.handleConn(conn)
		}
	}()

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parsing generated certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return srv, pool
}

func TestSenderSendImplicitTLSViaFakeSMTPServer(t *testing.T) {
	srv, rootCAs := startFakeSMTPServerTLS(t)
	host, port := srv.addr()

	sender := &Sender{
		Host:       host,
		Port:       port,
		From:       "hhq@example.com",
		UseTLS:     true,
		tlsRootCAs: rootCAs,
	}

	err := sender.Send("", []string{"parent@example.com"}, "TLS Test", "<p>hello over tls</p>")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(srv.mu.messages) != 1 {
		t.Fatalf("fake tls server received %d messages, want 1", len(srv.mu.messages))
	}
	got := srv.mu.messages[0]
	if !strings.Contains(strings.ToUpper(got.from), "HHQ@EXAMPLE.COM") {
		t.Errorf("MAIL FROM = %q", got.from)
	}
	if len(got.to) != 1 || !strings.Contains(strings.ToUpper(got.to[0]), "PARENT@EXAMPLE.COM") {
		t.Errorf("RCPT TO = %v", got.to)
	}
	if !strings.Contains(got.data, "hello over tls") {
		t.Error("expected the HTML body to appear in the transmitted DATA")
	}
}
