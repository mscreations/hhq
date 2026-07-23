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

package scheduler

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/email"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
)

// captureSMTPServer is a minimal fake SMTP server (same technique as
// internal/email's fake server) used here to verify sendWeeklyReport's
// end-to-end wiring: DB query -> PDF build -> actual outbound email, not
// just that no error is returned.
type captureSMTPServer struct {
	listener net.Listener
	dataSeen chan string
}

func startCaptureSMTPServer(t *testing.T) *captureSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &captureSMTPServer{listener: ln, dataSeen: make(chan string, 1)}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.handle(conn)
		}
	}()
	return srv
}

func (s *captureSMTPServer) addr() (string, int) {
	return "127.0.0.1", s.listener.Addr().(*net.TCPAddr).Port
}

func (s *captureSMTPServer) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	fmt.Fprint(conn, "220 fake ESMTP\r\n")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		upper := strings.ToUpper(strings.TrimRight(line, "\r\n"))
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			fmt.Fprint(conn, "250-fake\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			fmt.Fprint(conn, "235 OK\r\n")
		case strings.HasPrefix(upper, "MAIL FROM:"), strings.HasPrefix(upper, "RCPT TO:"):
			fmt.Fprint(conn, "250 OK\r\n")
		case upper == "DATA":
			fmt.Fprint(conn, "354 go ahead\r\n")
			var body strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				body.WriteString(dl)
			}
			s.dataSeen <- body.String()
			fmt.Fprint(conn, "250 OK: queued\r\n")
		case upper == "QUIT":
			fmt.Fprint(conn, "221 Bye\r\n")
			return
		default:
			fmt.Fprint(conn, "250 OK\r\n")
		}
	}
}

func TestSendWeeklyReportEmailsAllParentsWithGeneratedPDF(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	users := &models.UserStore{DB: conn}
	chores := &models.ChoreStore{DB: conn}
	defs := &models.ChoreDefinitionStore{DB: conn}
	instances := &models.ChoreInstanceStore{DB: conn}

	childID, err := users.CreateChild(ctx, "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if _, err := users.CreateParent(ctx, "Parent", "parent@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	choreID, err := chores.Create(ctx, "Weekly chore", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}

	now := time.Now()
	weekStart := startOfWeek(now.AddDate(0, 0, -6))
	if _, err := defs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}
	if err := instances.EnsureForDate(ctx, weekStart); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}

	srv := startCaptureSMTPServer(t)
	host, port := srv.addr()

	s := &Scheduler{
		Cfg:            &config.Config{},
		ChoreInstances: instances,
		Users:          users,
		Settings:       &models.SettingsStore{DB: conn},
		Mailer:         &email.Sender{Host: host, Port: port, From: "hhq@example.com"},
	}

	if err := s.sendWeeklyReport(ctx, now); err != nil {
		t.Fatalf("sendWeeklyReport: %v", err)
	}

	select {
	case data := <-srv.dataSeen:
		if !strings.Contains(data, "Weekly Chore Report") {
			t.Error("expected the subject to mention the weekly chore report")
		}
		if !strings.Contains(data, "chore-report-") {
			t.Error("expected a chore-report-*.pdf attachment filename in the message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fake SMTP server to receive a message")
	}
}

func TestSendWeeklyReportSkipsSendWhenNoParentsHaveEmail(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	users := &models.UserStore{DB: conn}
	instances := &models.ChoreInstanceStore{DB: conn}

	// No parents at all - Mailer must not be invoked, so leaving it nil
	// should be safe if sendWeeklyReport correctly short-circuits.
	s := &Scheduler{
		Cfg:            &config.Config{},
		ChoreInstances: instances,
		Users:          users,
		Mailer:         nil,
	}

	if err := s.sendWeeklyReport(ctx, time.Now()); err != nil {
		t.Fatalf("sendWeeklyReport with no parents should no-op without error, got: %v", err)
	}
}
