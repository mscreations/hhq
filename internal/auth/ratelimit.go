package auth

import (
	"sync"
	"time"
)

// LoginLimiter is a simple in-memory sliding-window rate limiter for login
// attempts, keyed by an arbitrary string (e.g. "ip:1.2.3.4" or
// "email:someone@example.com" - see LoginSubmit, which checks/records both
// per request). In-memory is good enough given this app's deployment stays
// at a single replica (see deploy/k8s/deployment.yaml's note on why) - a
// distributed store (e.g. Redis) would be needed if that ever changes.
type LoginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	max      int
	window   time.Duration
}

// NewLoginLimiter allows at most max failed attempts per key within window.
func NewLoginLimiter(max int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{
		attempts: make(map[string][]time.Time),
		max:      max,
		window:   window,
	}
}

// Allow reports whether a new attempt for key is currently permitted. It
// does not record anything - call RecordFailure after an attempt actually
// fails, and Reset after one succeeds.
func (l *LoginLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(key)
	return len(l.attempts[key]) < l.max
}

// RecordFailure records a failed attempt for key.
func (l *LoginLimiter) RecordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(key)
	l.attempts[key] = append(l.attempts[key], time.Now())
}

// Reset clears any recorded failures for key, e.g. after a successful login.
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// Sweep drops all keys whose recorded attempts have entirely aged out of the
// window, so keys that stop being attempted (e.g. an IP that moves on) don't
// linger in memory forever. Intended to be called periodically - see
// internal/scheduler.
func (l *LoginLimiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key := range l.attempts {
		l.pruneLocked(key)
	}
}

// pruneLocked drops attempts for key older than the window, deleting the key
// entirely if nothing recent remains. Caller must hold l.mu.
func (l *LoginLimiter) pruneLocked(key string) {
	cutoff := time.Now().Add(-l.window)
	existing := l.attempts[key]
	kept := existing[:0]
	for _, t := range existing {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.attempts, key)
	} else {
		l.attempts[key] = kept
	}
}
