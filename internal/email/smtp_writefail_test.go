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
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"testing"
)

// rstSMTPServer is a variant of scriptedSMTPServer (smtp_more_test.go) built
// specifically to make sendImplicitTLS's w.Write(msg) call itself (not just
// the final w.Close() that flushes the "." terminator and reads the server's
// response) return a non-nil error, deterministically rather than by luck.
//
// scriptedSMTPServer's "write" stage (see TestSenderSendImplicitTLSWriteError
// in smtp_more_test.go) closes the connection gracefully (a TCP FIN via the
// deferred conn.Close()) right after sending "354". That leaves a window
// where the OS still accepts the client's subsequent Write into its local
// send buffer without error - the write only fails later, which is why that
// existing test observes its error coming out of w.Close() (which performs a
// final flush + read of the server's reply) rather than out of w.Write
// itself; go tool cover confirms the w.Write error-return line stays
// uncovered under that test alone.
//
// This variant instead sets SO_LINGER(0) on the raw TCP connection before
// closing it, which makes the OS send an immediate RST instead of a FIN.
// Localhost RSTs are delivered essentially instantly, so by the time the
// client's very next Write syscall happens, the kernel already knows the
// connection is reset and returns the error right there - reliably
// attributing the failure to w.Write instead of w.Close.
func startRSTSMTPServer(t *testing.T) (host string, port int, rootCAs *x509.CertPool) {
	t.Helper()

	cert := selfSignedCertFor(t, net.ParseIP("127.0.0.1"))
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting raw tcp listener: %v", err)
	}
	t.Cleanup(func() { tcpLn.Close() })

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}

	go func() {
		for {
			raw, err := tcpLn.Accept()
			if err != nil {
				return
			}
			go handleRSTConn(raw, tlsConfig)
		}
	}()

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parsing generated certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	tcpAddr := tcpLn.Addr().(*net.TCPAddr)
	return "127.0.0.1", tcpAddr.Port, pool
}

func handleRSTConn(raw net.Conn, tlsConfig *tls.Config) {
	tlsConn := tls.Server(raw, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		raw.Close()
		return
	}

	r := bufio.NewReader(tlsConn)
	fmt.Fprint(tlsConn, "220 fake.smtp.local ESMTP\r\n")

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			raw.Close()
			return
		}
		upper := strings.ToUpper(strings.TrimRight(line, "\r\n"))

		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			fmt.Fprint(tlsConn, "250-fake.smtp.local\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			fmt.Fprint(tlsConn, "235 Authentication successful\r\n")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			fmt.Fprint(tlsConn, "250 OK\r\n")
		case strings.HasPrefix(upper, "RCPT TO:"):
			fmt.Fprint(tlsConn, "250 OK\r\n")
		case upper == "DATA":
			fmt.Fprint(tlsConn, "354 Start mail input\r\n")
			// Force an immediate RST on the underlying TCP connection
			// (bypassing the TLS close_notify machinery entirely, and
			// bypassing the graceful FIN a plain conn.Close() would send)
			// so the client's next Write fails right away instead of
			// succeeding into a local send buffer.
			if tcpConn, ok := raw.(*net.TCPConn); ok {
				tcpConn.SetLinger(0)
			}
			raw.Close()
			return
		case upper == "QUIT":
			fmt.Fprint(tlsConn, "221 Bye\r\n")
			raw.Close()
			return
		default:
			fmt.Fprint(tlsConn, "250 OK\r\n")
		}
	}
}

// TestSenderSendImplicitTLSWriteItselfFails is a more deterministic sibling
// of TestSenderSendImplicitTLSWriteError (smtp_more_test.go): it forces the
// connection reset with SO_LINGER(0) so that sendImplicitTLS's
// `if _, err := w.Write(msg); err != nil { return err }` branch itself is
// exercised, not just the later w.Close() call.
func TestSenderSendImplicitTLSWriteItselfFails(t *testing.T) {
	host, port, rootCAs := startRSTSMTPServer(t)

	sender := &Sender{
		Host:       host,
		Port:       port,
		From:       "hhq@example.com",
		UseTLS:     true,
		tlsRootCAs: rootCAs,
	}

	// A larger body makes it very unlikely the entire message slips into
	// the kernel's send buffer as a single successful Write before the
	// already-delivered RST is observed.
	bigBody := "<p>" + strings.Repeat("x", 4*1024*1024) + "</p>"

	err := sender.Send("", []string{"parent@example.com"}, "Subject", bigBody)
	if err == nil {
		t.Fatal("expected Send to fail when the connection is reset mid-DATA")
	}
}
