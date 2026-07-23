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
