package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/util"
)

// testEncryptor returns a working Encryptor for tests that need to exercise
// code past the encrypt step (e.g. the DB-error branches below, where the
// failure needs to come from the store, not from encryption itself).
func testEncryptor(t *testing.T) *util.Encryptor {
	t.Helper()
	enc, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	return enc
}

// brokenDB returns an open-then-immediately-closed *sql.DB so every query
// against it deterministically fails with "sql: database is closed" -
// same technique cmd/server/router_test.go uses, reimplemented here (rather
// than exported/shared) since these tests call the handler methods directly
// rather than going through the router+RequireParent middleware chain: the
// DB-error branches below (a store's generic non-NotFound error) can only
// be reached once past that middleware, and driving a real login against a
// broken DB isn't possible (the session lookup itself needs the DB) - so
// these tests call a.GoogleConnectStart/a.GoogleConnectCallback as plain
// functions, bypassing auth entirely, which is fine here since none of the
// branches under test have anything to do with authorization.
func brokenDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://broken:broken@127.0.0.1:1/broken")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.Close()
	t.Cleanup(func() { db.Close() })
	return db
}

func TestGoogleConnectStartReturnsServerErrorOnGetByIDFailure(t *testing.T) {
	app := &App{
		Cfg:              &config.Config{GoogleOAuthClientID: "id", GoogleOAuthClientSecret: "secret"},
		CalendarAccounts: &models.CalendarAccountStore{DB: brokenDB(t)},
		GoogleOAuthState: auth.NewApprovalLinkSigner("state-secret"),
	}
	req := httptest.NewRequest(http.MethodGet, "/parent/calendar-accounts/google/connect?reconnect_id=1", nil)
	w := httptest.NewRecorder()

	app.GoogleConnectStart(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestGoogleConnectCallbackReturnsServerErrorOnUpdateGoogleTokenFailure(t *testing.T) {
	fake := startFakeGoogleOAuthForHandlerTest(t)
	app := &App{
		Cfg:              &config.Config{GoogleOAuthClientID: "id", GoogleOAuthClientSecret: "secret"},
		CalendarAccounts: &models.CalendarAccountStore{DB: brokenDB(t)},
		GoogleOAuthState: auth.NewApprovalLinkSigner("state-secret"),
		Encryptor:        testEncryptor(t),
	}
	origEndpoint := config.GoogleEndpoint
	config.GoogleEndpoint.AuthURL = fake.URL + "/auth"
	config.GoogleEndpoint.TokenURL = fake.URL + "/token"
	t.Cleanup(func() { config.GoogleEndpoint = origEndpoint })
	origUserInfoURL := GoogleUserInfoURL
	GoogleUserInfoURL = fake.URL + "/userinfo"
	t.Cleanup(func() { GoogleUserInfoURL = origUserInfoURL })

	state := app.GoogleOAuthState.Sign(42, auth.ActionGoogleOAuthConnect, time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/parent/calendar-accounts/google/callback?state="+state+"&code=fake-code", nil)
	w := httptest.NewRecorder()

	app.GoogleConnectCallback(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestGoogleConnectCallbackReturnsServerErrorOnCreateGoogleFailure(t *testing.T) {
	fake := startFakeGoogleOAuthForHandlerTest(t)
	app := &App{
		Cfg:              &config.Config{GoogleOAuthClientID: "id", GoogleOAuthClientSecret: "secret"},
		CalendarAccounts: &models.CalendarAccountStore{DB: brokenDB(t)},
		GoogleOAuthState: auth.NewApprovalLinkSigner("state-secret"),
		Encryptor:        testEncryptor(t),
	}
	origEndpoint := config.GoogleEndpoint
	config.GoogleEndpoint.AuthURL = fake.URL + "/auth"
	config.GoogleEndpoint.TokenURL = fake.URL + "/token"
	t.Cleanup(func() { config.GoogleEndpoint = origEndpoint })
	origUserInfoURL := GoogleUserInfoURL
	GoogleUserInfoURL = fake.URL + "/userinfo"
	t.Cleanup(func() { GoogleUserInfoURL = origUserInfoURL })

	state := app.GoogleOAuthState.Sign(0, auth.ActionGoogleOAuthConnect, time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/parent/calendar-accounts/google/callback?state="+state+"&code=fake-code", nil)
	w := httptest.NewRecorder()

	app.GoogleConnectCallback(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestGoogleConnectCallbackNotFoundWhenUnconfiguredCalledDirectly(t *testing.T) {
	app := &App{Cfg: &config.Config{}}
	req := httptest.NewRequest(http.MethodGet, "/parent/calendar-accounts/google/callback", nil)
	w := httptest.NewRecorder()

	app.GoogleConnectCallback(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestFetchGoogleEmailConnectionError(t *testing.T) {
	origUserInfoURL := GoogleUserInfoURL
	GoogleUserInfoURL = "http://127.0.0.1:1/unreachable"
	t.Cleanup(func() { GoogleUserInfoURL = origUserInfoURL })

	oauthCfg := &oauth2.Config{}
	token := &oauth2.Token{AccessToken: "x"}
	_, err := fetchGoogleEmail(t.Context(), oauthCfg, token)
	if err == nil {
		t.Fatal("expected an error hitting an unreachable userinfo endpoint")
	}
}

func TestTokenEmailHintNilToken(t *testing.T) {
	if got := tokenEmailHint(nil); got != "unknown" {
		t.Errorf("tokenEmailHint(nil) = %q, want %q", got, "unknown")
	}
}

// startFakeGoogleOAuthForHandlerTest is a trimmed copy of the token/userinfo
// fake server used in cmd/server's google_oauth_coverage_test.go, kept
// package-local here since internal/handlers can't import the cmd/server
// test helper (different module-internal package, and cmd/server itself
// imports internal/handlers - importing back would cycle).
type fakeGoogleOAuthServer struct {
	*httptest.Server
}

func startFakeGoogleOAuthForHandlerTest(t *testing.T) *fakeGoogleOAuthServer {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"fake-access-token","refresh_token":"fake-refresh-token","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"email":"kid@example.com"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fakeGoogleOAuthServer{Server: srv}
}
