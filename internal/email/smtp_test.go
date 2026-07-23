package email

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestBase64ChunkedWrapsAt76Chars(t *testing.T) {
	data := make([]byte, 200)
	for i := range data {
		data[i] = byte('A' + i%26)
	}
	chunked := base64Chunked(data)
	lines := strings.Split(strings.TrimRight(chunked, "\r\n"), "\r\n")
	for i, line := range lines {
		if i < len(lines)-1 && len(line) != 76 {
			t.Errorf("line %d length = %d, want 76 (except possibly the last line)", i, len(line))
		}
	}
	// Round-trips back to the original bytes once unwrapped.
	joined := strings.Join(lines, "")
	decoded, err := base64.StdEncoding.DecodeString(joined)
	if err != nil {
		t.Fatalf("decoding chunked base64: %v", err)
	}
	if string(decoded) != string(data) {
		t.Fatal("chunked base64 did not round-trip to the original data")
	}
}

func TestBuildMIMEMessageStructure(t *testing.T) {
	msg, err := buildMIMEMessage("hhq@example.com", []string{"parent@example.com"}, "Test Subject", "<p>hello</p>", []Attachment{
		{Filename: "report.pdf", ContentType: "application/pdf", Data: []byte("%PDF-fake-data")},
	})
	if err != nil {
		t.Fatalf("buildMIMEMessage: %v", err)
	}
	s := string(msg)

	for _, want := range []string{
		"From: hhq@example.com",
		"To: parent@example.com",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed;",
		"<p>hello</p>",
		`filename="report.pdf"`,
		"Content-Type: application/pdf",
		"Content-Transfer-Encoding: base64",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("message missing %q\nfull message:\n%s", want, s)
		}
	}
}

func TestBuildMIMEMessageWithoutAttachments(t *testing.T) {
	msg, err := buildMIMEMessage("hhq@example.com", []string{"a@example.com", "b@example.com"}, "Subj", "<p>body</p>", nil)
	if err != nil {
		t.Fatalf("buildMIMEMessage: %v", err)
	}
	s := string(msg)
	if !strings.Contains(s, "To: a@example.com, b@example.com") {
		t.Errorf("expected multiple recipients joined with comma, got:\n%s", s)
	}
	if strings.Contains(s, "Content-Disposition: attachment") {
		t.Error("expected no attachment part when none are given")
	}
}

// --- fakeSMTPServer: a minimal SMTP server sufficient to exercise
// Sender.Send end-to-end, mirroring the "verify against something real,
// not just a code read" rigor CLAUDE.md documents for CalDAV. It implements
// just enough of RFC 5321 for net/smtp's client to complete a plain-auth
// send: EHLO, AUTH PLAIN, MAIL FROM, RCPT TO, DATA, QUIT.

type fakeSMTPServer struct {
	listener net.Listener
	mu       struct {
		messages []capturedMessage
	}
}

type capturedMessage struct {
	from string
	to   []string
	data string
}

func startFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting fake smtp listener: %v", err)
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
	return srv
}

func (s *fakeSMTPServer) addr() (host string, port int) {
	tcpAddr := s.listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", tcpAddr.Port
}

func (s *fakeSMTPServer) handleConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	fmt.Fprint(conn, "220 fake.smtp.local ESMTP\r\n")

	var msg capturedMessage
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
			fmt.Fprint(conn, "235 Authentication successful\r\n")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			msg.from = line
			fmt.Fprint(conn, "250 OK\r\n")
		case strings.HasPrefix(upper, "RCPT TO:"):
			msg.to = append(msg.to, line)
			fmt.Fprint(conn, "250 OK\r\n")
		case upper == "DATA":
			fmt.Fprint(conn, "354 Start mail input\r\n")
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
			msg.data = body.String()
			s.mu.messages = append(s.mu.messages, msg)
			fmt.Fprint(conn, "250 OK: queued\r\n")
		case upper == "QUIT":
			fmt.Fprint(conn, "221 Bye\r\n")
			return
		default:
			fmt.Fprint(conn, "250 OK\r\n")
		}
	}
}

func TestSenderSendViaFakeSMTPServer(t *testing.T) {
	srv := startFakeSMTPServer(t)
	host, port := srv.addr()

	sender := &Sender{
		Host: host,
		Port: port,
		From: "hhq@example.com",
	}

	err := sender.Send("HappyHome Quest", []string{"parent@example.com"}, "Weekly Report", "<p>Report attached</p>", Attachment{
		Filename:    "report.pdf",
		ContentType: "application/pdf",
		Data:        []byte("fake-pdf-bytes"),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Give the server goroutine a moment to finish appending to messages;
	// net/smtp's Quit only returns after the server acks "221 Bye", so by
	// the time Send returns the message is already appended.
	if len(srv.mu.messages) != 1 {
		t.Fatalf("fake server received %d messages, want 1", len(srv.mu.messages))
	}
	got := srv.mu.messages[0]
	// MAIL FROM must stay a bare address - Fastmail (and other servers)
	// reject a display name there, see internal/email/smtp.go's displayFrom.
	if !strings.Contains(strings.ToUpper(got.from), "HHQ@EXAMPLE.COM") || strings.Contains(got.from, "HappyHome") {
		t.Errorf("MAIL FROM = %q, want a bare address with no display name", got.from)
	}
	if !strings.Contains(got.data, `From: "HappyHome Quest" <hhq@example.com>`) {
		t.Error("expected the From: header to carry the display name")
	}
	if len(got.to) != 1 || !strings.Contains(strings.ToUpper(got.to[0]), "PARENT@EXAMPLE.COM") {
		t.Errorf("RCPT TO = %v", got.to)
	}
	if !strings.Contains(got.data, "Weekly Report") {
		t.Error("expected the subject to appear in the transmitted DATA")
	}
	if !strings.Contains(got.data, "Report attached") {
		t.Error("expected the HTML body to appear in the transmitted DATA")
	}
}
