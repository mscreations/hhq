package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// requiredEnv sets every env var Load() requires to succeed, returning a
// function tests can use to override or unset specific ones.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_NAME", "hhq")
	t.Setenv("DB_USER", "hhq")
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd")
}

func TestLoadSucceedsWithAllRequiredVarsSet(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBHost != "localhost" || cfg.DBName != "hhq" || cfg.DBUser != "hhq" {
		t.Fatalf("unexpected DB config: %+v", cfg)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr default = %q, want :8080", cfg.ListenAddr)
	}
	if cfg.DBPort != "5432" {
		t.Errorf("DBPort default = %q, want 5432", cfg.DBPort)
	}
	if cfg.CalendarSyncInterval != 15*time.Minute {
		t.Errorf("CalendarSyncInterval default = %v, want 15m", cfg.CalendarSyncInterval)
	}
	if cfg.CalendarWindowDays != 7 {
		t.Errorf("CalendarWindowDays default = %d, want 7", cfg.CalendarWindowDays)
	}
	if cfg.WeeklyReportWeekday != time.Sunday || cfg.WeeklyReportHour != 20 {
		t.Errorf("weekly report defaults = %v/%d, want Sunday/20", cfg.WeeklyReportWeekday, cfg.WeeklyReportHour)
	}
}

func TestLoadSucceedsWithoutSessionApprovalOrInviteSecrets(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("APPROVAL_SECRET", "")
	t.Setenv("INVITE_SECRET", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SessionSecret != "" || cfg.ApprovalSecret != "" || cfg.InviteSecret != "" {
		t.Fatalf("expected empty secrets to pass through unset - main.go is responsible for generating them, got %+v", cfg)
	}
}

func TestLoadMissingRequiredVars(t *testing.T) {
	tests := []struct {
		name  string
		unset string
	}{
		{"missing DB_HOST", "DB_HOST"},
		{"missing DB_NAME", "DB_NAME"},
		{"missing DB_USER", "DB_USER"},
		{"missing ENCRYPTION_KEY", "ENCRYPTION_KEY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(tt.unset, "")

			if _, err := Load(); err == nil {
				t.Fatalf("expected Load to fail when %s is unset", tt.unset)
			}
		})
	}
}

func TestLoadCookieSecureDefaultsFromPublicBaseURL(t *testing.T) {
	setRequiredEnv(t)

	t.Setenv("PUBLIC_BASE_URL", "https://hhq.example.com")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.CookieSecure {
		t.Error("expected CookieSecure to default true for an https:// PublicBaseURL")
	}

	t.Setenv("PUBLIC_BASE_URL", "http://hhq.local")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CookieSecure {
		t.Error("expected CookieSecure to default false for an http:// PublicBaseURL")
	}
}

func TestLoadCookieSecureExplicitOverride(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PUBLIC_BASE_URL", "http://hhq.local")
	t.Setenv("COOKIE_SECURE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.CookieSecure {
		t.Error("expected explicit COOKIE_SECURE=true to override the http:// default")
	}
}

func TestLoadInvalidSMTPPort(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SMTP_PORT", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("expected Load to fail on an invalid SMTP_PORT")
	}
}

func TestLoadInvalidCalendarSyncInterval(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CALENDAR_SYNC_INTERVAL_MINUTES", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("expected Load to fail on an invalid CALENDAR_SYNC_INTERVAL_MINUTES")
	}
}

func TestLoadInvalidCalendarWindowDays(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CALENDAR_WINDOW_DAYS", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("expected Load to fail on an invalid CALENDAR_WINDOW_DAYS")
	}
}

func TestLoadInvalidWeatherRefreshInterval(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("WEATHER_REFRESH_INTERVAL_MINUTES", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("expected Load to fail on an invalid WEATHER_REFRESH_INTERVAL_MINUTES")
	}
}

func TestLoadInvalidPluginSyncInterval(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PLUGIN_SYNC_INTERVAL_MINUTES", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("expected Load to fail on an invalid PLUGIN_SYNC_INTERVAL_MINUTES")
	}
}

// TestLoadInvalidBoolFallsBackToDefault covers getEnvBool's error path: an
// unparseable value doesn't fail Load(), it silently falls back to the
// caller-supplied default (unlike the strconv.Atoi fields above, which do
// fail Load()).
func TestLoadInvalidBoolFallsBackToDefault(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SMTP_STARTTLS", "not-a-bool")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.SMTPStartTLS {
		t.Errorf("SMTPStartTLS = %v, want true (the default) when SMTP_STARTTLS fails to parse", cfg.SMTPStartTLS)
	}
}

// TestGetEnvFileSuffixPrecedence covers the KEY_FILE convention (used to
// read secrets mounted as files in Kubernetes) taking precedence over a
// plain KEY env var when both are set.
func TestGetEnvFileSuffixPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}

	t.Setenv("SOME_SECRET", "from-env")
	t.Setenv("SOME_SECRET_FILE", path)

	if got := Getenv("SOME_SECRET"); got != "from-file" {
		t.Fatalf("getEnv() = %q, want the file contents to take precedence", got)
	}
}

func TestGetEnvFallsBackToPlainEnvWhenFileMissing(t *testing.T) {
	t.Setenv("SOME_SECRET", "from-env")
	t.Setenv("SOME_SECRET_FILE", filepath.Join(t.TempDir(), "does-not-exist"))

	if got := Getenv("SOME_SECRET"); got != "from-env" {
		t.Fatalf("getEnv() = %q, want fallback to the plain env var when the file doesn't exist", got)
	}
}

func TestDSNFormatsAllFields(t *testing.T) {
	cfg := &Config{
		DBHost: "db.local", DBPort: "5432", DBName: "hhq",
		DBUser: "hhq", DBPassword: "s3cret", DBSSLMode: "disable",
	}
	want := "host=db.local port=5432 dbname=hhq user=hhq password=s3cret sslmode=disable"
	if got := cfg.DSN(); got != want {
		t.Fatalf("DSN() = %q, want %q", got, want)
	}
}

func TestGoogleOAuthEnabled(t *testing.T) {
	tests := []struct {
		name         string
		clientID     string
		clientSecret string
		want         bool
	}{
		{"both set", "id", "secret", true},
		{"both unset", "", "", false},
		{"only id set", "id", "", false},
		{"only secret set", "", "secret", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{GoogleOAuthClientID: tt.clientID, GoogleOAuthClientSecret: tt.clientSecret}
			if got := cfg.GoogleOAuthEnabled(); got != tt.want {
				t.Errorf("GoogleOAuthEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGoogleOAuthConfigBuildsRedirectURLFromPublicBaseURL(t *testing.T) {
	cfg := &Config{
		GoogleOAuthClientID:     "id",
		GoogleOAuthClientSecret: "secret",
		PublicBaseURL:           "https://hhq.example.com",
	}
	oauthCfg := GoogleOAuthConfig(cfg)
	want := "https://hhq.example.com/parent/calendar-accounts/google/callback"
	if oauthCfg.RedirectURL != want {
		t.Errorf("RedirectURL = %q, want %q", oauthCfg.RedirectURL, want)
	}
	if oauthCfg.ClientID != "id" || oauthCfg.ClientSecret != "secret" {
		t.Errorf("unexpected client credentials: %+v", oauthCfg)
	}
}
