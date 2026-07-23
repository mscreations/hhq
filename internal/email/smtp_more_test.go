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

// --- Send's own sendErr-propagation branch (line 80) ---

// TestSenderSendReturnsErrorWhenSendMailFails covers Send's sendErr != nil
// branch for the non-TLS (STARTTLS/plaintext) path: an address nothing is
// listening on makes smtp.SendMail fail at the initial dial, which Send
// must log and propagate rather than swallow.
func TestSenderSendReturnsErrorWhenSendMailFails(t *testing.T) {
	sender := &Sender{
		Host: "127.0.0.1",
		Port: unusedTCPPort(t),
		From: "hhq@example.com",
	}

	err := sender.Send("", []string{"parent@example.com"}, "Subject", "<p>body</p>")
	if err == nil {
		t.Fatal("expected Send to fail and return the underlying smtp.SendMail error")
	}
}

// --- sendImplicitTLS's own dial-error branch (line 90) ---

// TestSenderSendImplicitTLSDialError covers sendImplicitTLS's tls.Dial-error
// branch: an unreachable address must surface as a wrapped "tls dial" error.
func TestSenderSendImplicitTLSDialError(t *testing.T) {
	sender := &Sender{
		Host:   "127.0.0.1",
		Port:   unusedTCPPort(t),
		From:   "hhq@example.com",
		UseTLS: true,
	}

	err := sender.Send("", []string{"parent@example.com"}, "Subject", "<p>body</p>")
	if err == nil {
		t.Fatal("expected Send to fail via sendImplicitTLS's tls.Dial")
	}
	if !strings.Contains(err.Error(), "tls dial") {
		t.Fatalf("error = %v, want it to include the 'tls dial' wrap context", err)
	}
}

// unusedTCPPort opens and immediately closes a listener to get a port
// nothing is listening on by the time the test dials it.
func unusedTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding an unused port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// --- scriptedSMTPServer: an implicit-TLS fake SMTP server whose responses
// can be scripted to fail at a specific stage of the RFC 5321 dialog, so
// each of sendImplicitTLS's error-return branches (smtp.NewClient, Auth,
// Mail, Rcpt, Data, and the Data write itself) can be triggered
// independently. Mirrors fakeSMTPServer (smtp_test.go)/its TLS variant
// (smtp_tls_test.go), which only exercise the all-success path.

type scriptedSMTPServer struct {
	listener  net.Listener
	failStage string // "greeting", "auth", "mail", "rcpt", "data", "write", or "" for none
}

func startScriptedSMTPServerTLS(t *testing.T, failStage string) (*scriptedSMTPServer, *x509.CertPool) {
	t.Helper()
	cert := selfSignedCertFor(t, net.ParseIP("127.0.0.1"))
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("starting scripted tls smtp listener: %v", err)
	}
	srv := &scriptedSMTPServer{listener: ln, failStage: failStage}
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

func (s *scriptedSMTPServer) addr() (host string, port int) {
	tcpAddr := s.listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", tcpAddr.Port
}

func (s *scriptedSMTPServer) handleConn(conn net.Conn) {
	defer conn.Close()

	// tls.Listen's Accept hands back a *tls.Conn whose handshake is lazy -
	// it only runs on first Read/Write. Force it to complete here so the
	// client's tls.Dial succeeds even when failStage == "greeting" closes
	// the connection immediately afterward - otherwise the client would
	// fail the TLS handshake itself (a "tls dial" error) rather than
	// reaching smtp.NewClient's greeting read.
	if tlsConn, ok := conn.(*tls.Conn); ok {
		if err := tlsConn.Handshake(); err != nil {
			return
		}
	}

	if s.failStage == "greeting" {
		// Close before ever sending the "220" greeting smtp.NewClient reads.
		return
	}

	r := bufio.NewReader(conn)
	fmt.Fprint(conn, "220 fake.smtp.local ESMTP\r\n")

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			fmt.Fprint(conn, "250-fake.smtp.local\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			if s.failStage == "auth" {
				fmt.Fprint(conn, "535 Authentication failed\r\n")
				continue
			}
			fmt.Fprint(conn, "235 Authentication successful\r\n")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			if s.failStage == "mail" {
				fmt.Fprint(conn, "550 mail from rejected\r\n")
				continue
			}
			fmt.Fprint(conn, "250 OK\r\n")
		case strings.HasPrefix(upper, "RCPT TO:"):
			if s.failStage == "rcpt" {
				fmt.Fprint(conn, "550 rcpt to rejected\r\n")
				continue
			}
			fmt.Fprint(conn, "250 OK\r\n")
		case upper == "DATA":
			if s.failStage == "data" {
				fmt.Fprint(conn, "503 data rejected\r\n")
				continue
			}
			fmt.Fprint(conn, "354 Start mail input\r\n")
			if s.failStage == "write" {
				// Close right after the 354 the client is waiting on, before
				// it writes the message body, so the body Write itself fails
				// rather than the DATA command.
				return
			}
			var body strings.Builder
			for {
				dataLine, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
				body.WriteString(dataLine)
			}
			fmt.Fprint(conn, "250 OK: queued\r\n")
		case upper == "QUIT":
			fmt.Fprint(conn, "221 Bye\r\n")
			return
		default:
			fmt.Fprint(conn, "250 OK\r\n")
		}
	}
}

func sendViaScriptedServer(t *testing.T, failStage string) error {
	t.Helper()
	srv, rootCAs := startScriptedSMTPServerTLS(t, failStage)
	host, port := srv.addr()

	sender := &Sender{
		Host:       host,
		Port:       port,
		From:       "hhq@example.com",
		UseTLS:     true,
		tlsRootCAs: rootCAs,
	}
	return sender.Send("", []string{"parent@example.com"}, "Subject", "<p>body</p>")
}

// TestSenderSendImplicitTLSNewClientError covers the smtp.NewClient-error
// branch (line 96): a server that closes without ever sending the "220"
// greeting makes NewClient fail reading it.
func TestSenderSendImplicitTLSNewClientError(t *testing.T) {
	err := sendViaScriptedServer(t, "greeting")
	if err == nil {
		t.Fatal("expected Send to fail when the server never sends a greeting")
	}
	if !strings.Contains(err.Error(), "smtp client") {
		t.Fatalf("error = %v, want it to include the 'smtp client' wrap context", err)
	}
}

// TestSenderSendImplicitTLSAuthError covers the client.Auth-error branch
// (line 101): a server rejecting AUTH PLAIN with 535 must surface as a
// wrapped "smtp auth" error.
func TestSenderSendImplicitTLSAuthError(t *testing.T) {
	err := sendViaScriptedServer(t, "auth")
	if err == nil {
		t.Fatal("expected Send to fail when the server rejects AUTH")
	}
	if !strings.Contains(err.Error(), "smtp auth") {
		t.Fatalf("error = %v, want it to include the 'smtp auth' wrap context", err)
	}
}

// TestSenderSendImplicitTLSMailError covers the client.Mail-error branch
// (line 104): a server rejecting MAIL FROM must surface that error
// (unwrapped, per sendImplicitTLS's own code - see the corresponding "return
// err" at that line).
func TestSenderSendImplicitTLSMailError(t *testing.T) {
	err := sendViaScriptedServer(t, "mail")
	if err == nil {
		t.Fatal("expected Send to fail when the server rejects MAIL FROM")
	}
	if !strings.Contains(err.Error(), "550") {
		t.Fatalf("error = %v, want it to include the server's 550 rejection", err)
	}
}

// TestSenderSendImplicitTLSRcptError covers the client.Rcpt-error branch
// (line 108): a server rejecting RCPT TO must surface that error.
func TestSenderSendImplicitTLSRcptError(t *testing.T) {
	err := sendViaScriptedServer(t, "rcpt")
	if err == nil {
		t.Fatal("expected Send to fail when the server rejects RCPT TO")
	}
	if !strings.Contains(err.Error(), "550") {
		t.Fatalf("error = %v, want it to include the server's 550 rejection", err)
	}
}

// TestSenderSendImplicitTLSDataError covers the client.Data-error branch
// (line 113): a server rejecting the DATA command must surface that error.
func TestSenderSendImplicitTLSDataError(t *testing.T) {
	err := sendViaScriptedServer(t, "data")
	if err == nil {
		t.Fatal("expected Send to fail when the server rejects DATA")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("error = %v, want it to include the server's 503 rejection", err)
	}
}

// TestSenderSendImplicitTLSWriteError covers the w.Write(msg)-error branch
// (line 116): distinct from the DATA-command rejection above, this is a
// server that accepts DATA (354) but then closes the connection before the
// client writes the message body, so the write itself fails.
func TestSenderSendImplicitTLSWriteError(t *testing.T) {
	err := sendViaScriptedServer(t, "write")
	if err == nil {
		t.Fatal("expected Send to fail when the connection drops mid-write")
	}
}
