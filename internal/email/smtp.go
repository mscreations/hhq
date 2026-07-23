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

// Package email sends notification emails (chore approval requests, weekly
// reports) via SMTP using credentials mounted from a Kubernetes Secret.
package email

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"mime"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/mscreations/hhq/internal/logging"
)

// rejectCRLF refuses a value that will be embedded in a MIME header
// (From/To/Subject/Content-Type/Content-Disposition) if it contains a
// carriage return or line feed. Every one of these values ultimately traces
// back to data a parent can set through the dashboard (app_title, a child/
// parent's display name via the chore-approval subject line, an attachment
// filename) - without this check, a value containing "\r\n" could inject
// arbitrary extra headers or start a new MIME part, i.e. email header/
// content injection. Rejecting outright (rather than stripping the
// characters and sending anyway) means a malformed value fails loudly
// instead of silently sending a mangled header.
func rejectCRLF(field, s string) error {
	if strings.ContainsAny(s, "\r\n") {
		return fmt.Errorf("%s contains a line break, refusing to send", field)
	}
	return nil
}

// displayFrom formats the message's From: header. s.From (the SMTP_FROM env
// var) must stay a bare address for the SMTP envelope MAIL FROM - Fastmail
// and other servers reject a display name there (RFC 5321 mailbox) - but the
// From: header itself (RFC 5322) is exactly where a friendly name belongs, so
// it's built separately here and never used for the envelope.
func displayFrom(name, address string) string {
	if name == "" {
		return address
	}
	return (&mail.Address{Name: name, Address: address}).String()
}

type Sender struct {
	Host     string
	Port     int
	Username string
	Password string
	UseTLS   bool // implicit TLS, e.g. port 465
	StartTLS bool // STARTTLS, e.g. port 587
	From     string

	// tlsRootCAs overrides the system trust store used by sendImplicitTLS,
	// unexported since it only exists so tests can dial a self-signed fake
	// SMTP server (see smtp_tls_test.go) - nil (the zero value, used for
	// every real deployment) means "use the system pool" exactly as before.
	tlsRootCAs *x509.CertPool
}

type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Send sends a multipart email (HTML body + optional attachments, e.g. the
// weekly PDF report) to one or more recipients. fromName, if non-empty, is
// used as the display name on the From: header (e.g. the app_title setting,
// "HappyHome Quest") - the SMTP envelope MAIL FROM always stays s.From's bare
// address, since some servers (Fastmail included) reject a display name there.
func (s *Sender) Send(fromName string, to []string, subject, htmlBody string, attachments ...Attachment) error {
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	logging.Debugf("email: sending %q to %v via %s (tls=%v starttls=%v, %d attachment(s))", subject, to, addr, s.UseTLS, s.StartTLS, len(attachments))

	auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)

	msg, err := buildMIMEMessage(displayFrom(fromName, s.From), to, subject, htmlBody, attachments)
	if err != nil {
		return fmt.Errorf("building message: %w", err)
	}

	var sendErr error
	if s.UseTLS {
		sendErr = s.sendImplicitTLS(addr, auth, to, msg)
	} else {
		// smtp.SendMail handles STARTTLS automatically when the server advertises
		// it, which covers the common port-587 case.
		sendErr = smtp.SendMail(addr, auth, s.From, to, msg)
	}

	if sendErr != nil {
		logging.Errorf("email: sending %q to %v failed: %v", subject, to, sendErr)
		return sendErr
	}
	logging.Infof("email: sent %q to %v", subject, to)
	return nil
}

func (s *Sender) sendImplicitTLS(addr string, auth smtp.Auth, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.Host, RootCAs: s.tlsRootCAs})
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(s.From); err != nil {
		return err
	}
	for _, addr := range to {
		if err := client.Rcpt(addr); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	return w.Close()
}

func buildMIMEMessage(from string, to []string, subject, htmlBody string, attachments []Attachment) ([]byte, error) {
	if err := rejectCRLF("from address", from); err != nil {
		return nil, err
	}
	for _, addr := range to {
		if err := rejectCRLF("recipient address", addr); err != nil {
			return nil, err
		}
	}
	if err := rejectCRLF("subject", subject); err != nil {
		return nil, err
	}
	for _, a := range attachments {
		if err := rejectCRLF("attachment filename", a.Filename); err != nil {
			return nil, err
		}
		if err := rejectCRLF("attachment content type", a.ContentType); err != nil {
			return nil, err
		}
	}

	var buf bytes.Buffer
	boundary := fmt.Sprintf("hhq-%d", time.Now().UnixNano())

	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", subject))
	buf.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	buf.WriteString(htmlBody)
	buf.WriteString("\r\n")

	for _, a := range attachments {
		fmt.Fprintf(&buf, "--%s\r\n", boundary)
		fmt.Fprintf(&buf, "Content-Type: %s; name=%q\r\n", a.ContentType, a.Filename)
		buf.WriteString("Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=%q\r\n\r\n", a.Filename)
		buf.WriteString(base64Chunked(a.Data))
		buf.WriteString("\r\n")
	}

	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return buf.Bytes(), nil
}

// base64Chunked encodes data and wraps it at 76 chars per line, as required by
// the MIME spec for base64 content transfer encoding.
func base64Chunked(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var buf bytes.Buffer
	for i := 0; i < len(encoded); i += 76 {
		end := min(i+76, len(encoded))
		buf.WriteString(encoded[i:end])
		buf.WriteString("\r\n")
	}
	return buf.String()
}
