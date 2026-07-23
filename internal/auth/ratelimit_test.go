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

package auth

import (
	"testing"
	"time"
)

func TestLoginLimiterAllowsUpToMax(t *testing.T) {
	l := NewLoginLimiter(3, time.Minute)

	for i := 0; i < 3; i++ {
		if !l.Allow("ip:1.2.3.4") {
			t.Fatalf("expected attempt %d to be allowed", i+1)
		}
		l.RecordFailure("ip:1.2.3.4")
	}

	if l.Allow("ip:1.2.3.4") {
		t.Fatal("expected 4th attempt to be blocked after 3 failures with max=3")
	}
}

func TestLoginLimiterKeysAreIndependent(t *testing.T) {
	l := NewLoginLimiter(1, time.Minute)

	l.RecordFailure("ip:1.2.3.4")
	if l.Allow("ip:1.2.3.4") {
		t.Fatal("expected key to be blocked after 1 failure with max=1")
	}
	if !l.Allow("ip:5.6.7.8") {
		t.Fatal("expected a different key to be unaffected")
	}
}

func TestLoginLimiterReset(t *testing.T) {
	l := NewLoginLimiter(1, time.Minute)

	l.RecordFailure("email:a@example.com")
	if l.Allow("email:a@example.com") {
		t.Fatal("expected key to be blocked before reset")
	}

	l.Reset("email:a@example.com")
	if !l.Allow("email:a@example.com") {
		t.Fatal("expected key to be allowed again after reset")
	}
}

func TestLoginLimiterWindowExpiry(t *testing.T) {
	l := NewLoginLimiter(1, 20*time.Millisecond)

	l.RecordFailure("ip:1.2.3.4")
	if l.Allow("ip:1.2.3.4") {
		t.Fatal("expected key to be blocked immediately after failure")
	}

	time.Sleep(40 * time.Millisecond)
	if !l.Allow("ip:1.2.3.4") {
		t.Fatal("expected key to be allowed again once the window has passed")
	}
}

func TestLoginLimiterSweepDropsAgedOutKeys(t *testing.T) {
	l := NewLoginLimiter(1, 20*time.Millisecond)

	l.RecordFailure("ip:1.2.3.4")
	time.Sleep(40 * time.Millisecond)
	l.Sweep()

	l.mu.Lock()
	_, exists := l.attempts["ip:1.2.3.4"]
	l.mu.Unlock()
	if exists {
		t.Fatal("expected Sweep to remove the aged-out key entirely")
	}
}
