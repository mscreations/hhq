package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCSRFManagerTokenIsDeterministicPerSession(t *testing.T) {
	c := NewCSRFManager("csrf-secret")

	t1 := c.Token("session-a")
	t2 := c.Token("session-a")
	if t1 != t2 {
		t.Fatal("expected the same session token to always derive the same CSRF token")
	}

	t3 := c.Token("session-b")
	if t1 == t3 {
		t.Fatal("expected different session tokens to derive different CSRF tokens")
	}
}

func TestCSRFManagerVerify(t *testing.T) {
	c := NewCSRFManager("csrf-secret")
	token := c.Token("session-a")

	if !c.Verify("session-a", token) {
		t.Fatal("expected verify to succeed with the correct token")
	}
	if c.Verify("session-a", "wrong-token") {
		t.Fatal("expected verify to fail with an incorrect token")
	}
	if c.Verify("session-a", "") {
		t.Fatal("expected verify to fail with an empty submitted token")
	}
	if c.Verify("", token) {
		t.Fatal("expected verify to fail with an empty session token")
	}
}

func TestVerifyCSRFMiddleware(t *testing.T) {
	csrf := NewCSRFManager("csrf-secret")
	sm := &SessionManager{}

	called := false
	handler := sm.VerifyCSRF(csrf)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	// GET requests always pass through, regardless of CSRF token or session.
	req := httptest.NewRequest(http.MethodGet, "/parent", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("GET should pass through: called=%v code=%d", called, rec.Code)
	}

	// POST without a session cookie is rejected.
	called = false
	req = httptest.NewRequest(http.MethodPost, "/parent/children", strings.NewReader(""))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if called {
		t.Fatal("POST without a session cookie should not reach the handler")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// POST with a session cookie but no/invalid CSRF token is rejected.
	called = false
	req = httptest.NewRequest(http.MethodPost, "/parent/children", strings.NewReader(url.Values{
		CSRFFormFieldName: {"bogus"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "session-a"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if called {
		t.Fatal("POST with an invalid CSRF token should not reach the handler")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// POST with a session cookie and the matching CSRF token succeeds.
	called = false
	token := csrf.Token("session-a")
	req = httptest.NewRequest(http.MethodPost, "/parent/children", strings.NewReader(url.Values{
		CSRFFormFieldName: {token},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "session-a"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !called {
		t.Fatal("POST with a valid CSRF token should reach the handler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestVerifyCSRFMiddlewareRejectsUnparseableForm exercises VerifyCSRF's
// r.ParseForm() failure path: a session cookie is present (so the handler
// gets past the missing-session check), but the request's raw query string
// contains an invalid percent-escape, which makes ParseForm return an error
// before CSRF verification is even attempted.
func TestVerifyCSRFMiddlewareRejectsUnparseableForm(t *testing.T) {
	csrf := NewCSRFManager("csrf-secret")
	sm := &SessionManager{}

	called := false
	handler := sm.VerifyCSRF(csrf)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/parent/children", strings.NewReader(""))
	req.URL.RawQuery = "%zz"
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "session-a"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("POST with an unparseable form should not reach the handler")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCSRFTokenHelper(t *testing.T) {
	csrf := NewCSRFManager("csrf-secret")
	sm := &SessionManager{}

	req := httptest.NewRequest(http.MethodGet, "/parent", nil)
	if got := sm.CSRFToken(csrf, req); got != "" {
		t.Fatalf("expected empty token with no session cookie, got %q", got)
	}

	req.AddCookie(&http.Cookie{Name: cookieName, Value: "session-a"})
	got := sm.CSRFToken(csrf, req)
	want := csrf.Token("session-a")
	if got != want {
		t.Fatalf("CSRFToken() = %q, want %q", got, want)
	}
}
