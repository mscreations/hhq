// Package config centralizes all environment-driven configuration for HappyHome Quest.
// Everything the app needs to run is read once at startup so the rest of the
// codebase can just pass around a *Config instead of calling os.Getenv everywhere.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type Config struct {
	// HTTP
	ListenAddr string // e.g. ":8080"

	// Postgres connection. In Kubernetes these come from the CNPG-generated
	// Secret (commonly named "<cluster>-app", e.g. "myapp-postgres-app"),
	// which exposes POSTGRES_* style keys. We read a single DSN built from
	// the standard CNPG secret keys: host, port, dbname, user, password.
	DBHost     string
	DBPort     string
	DBName     string
	DBUser     string
	DBPassword string
	DBSSLMode  string // CNPG clusters typically terminate TLS internally; "disable" or "require" depending on your cluster config

	// Session / security
	//
	// SessionSecret/ApprovalSecret/InviteSecret/PasswordResetSecret are all
	// optional: if left unset, main.go generates a random one on first
	// startup and stores it encrypted (via EncryptionKey) in the settings
	// table, so only EncryptionKey itself needs to be provided by the
	// operator. Setting one of these env vars explicitly still overrides
	// the stored value.
	SessionSecret        string // used to sign session cookies
	ApprovalSecret       string // HMAC key used to sign parent approval email links
	InviteSecret         string // HMAC key used to sign parent invite email links (kept separate from ApprovalSecret for isolation)
	PasswordResetSecret  string // HMAC key used to sign "forgot password" reset email links (kept separate from the others for isolation)
	EncryptionKey        string // hex-encoded 32-byte AES-256 key for encrypting calendar credentials (and, as of the auto-generated secrets feature, the other secrets above) at rest
	SessionLifetime      time.Duration
	ApprovalLinkTTL      time.Duration
	InviteLinkTTL        time.Duration
	PasswordResetLinkTTL time.Duration
	// CookieSecure controls the Secure flag on the session cookie. Defaults to
	// true when PublicBaseURL is https://, false otherwise - can be overridden
	// explicitly with the COOKIE_SECURE env var (e.g. force true even behind a
	// TLS-terminating proxy that doesn't set PublicBaseURL to https://).
	CookieSecure bool

	// SMTP (mounted from a Kubernetes Secret)
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPUseTLS   bool // implicit TLS (port 465 style)
	SMTPStartTLS bool // STARTTLS (port 587 style)
	SMTPFrom     string

	// App behavior
	AppTitle               string        // default display name shown on the kiosk/emails until a parent overrides it via settings
	CalendarSyncInterval   time.Duration // how often to poll CalDAV sources
	CalendarWindowDays     int           // how many days ahead to display/fetch
	WeeklyReportWeekday    time.Weekday  // day of week to auto-generate/email the report
	WeeklyReportHour       int           // hour of day (0-23, server-local time) to send it
	PublicBaseURL          string        // e.g. https://hhq.example.com - used to build links in emails
	WeatherRefreshInterval time.Duration // how often to poll Open-Meteo for the configured location
	PluginSyncInterval     time.Duration // how often to poll registered plugins for synthetic calendar events
	ReleaseCheckInterval   time.Duration // how often to poll GitHub for a newer release (update-available badge)

	// Google Calendar OAuth2. Both optional - if either is empty, the
	// "Connect Google Calendar" flow is hidden/disabled rather than the app
	// failing to start, consistent with keeping required secrets to a
	// minimum (see EncryptionKey's field comment above).
	GoogleOAuthClientID     string
	GoogleOAuthClientSecret string
}

func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr: getEnvDefault("LISTEN_ADDR", ":8080"),

		DBHost:     Getenv("DB_HOST"),
		DBPort:     getEnvDefault("DB_PORT", "5432"),
		DBName:     Getenv("DB_NAME"),
		DBUser:     Getenv("DB_USER"),
		DBPassword: Getenv("DB_PASSWORD"),
		DBSSLMode:  getEnvDefault("DB_SSLMODE", "disable"),

		SessionSecret:       Getenv("SESSION_SECRET"),
		ApprovalSecret:      Getenv("APPROVAL_SECRET"),
		InviteSecret:        Getenv("INVITE_SECRET"),
		PasswordResetSecret: Getenv("PASSWORD_RESET_SECRET"),
		EncryptionKey:       Getenv("ENCRYPTION_KEY"),

		SMTPHost:     Getenv("SMTP_HOST"),
		SMTPUser:     Getenv("SMTP_USER"),
		SMTPPassword: Getenv("SMTP_PASSWORD"),
		SMTPFrom:     getEnvDefault("SMTP_FROM", "hhq@example.com"),

		PublicBaseURL: Getenv("PUBLIC_BASE_URL"),

		AppTitle: getEnvDefault("APP_TITLE", "HappyHome Quest"),

		GoogleOAuthClientID:     Getenv("GOOGLE_OAUTH_CLIENT_ID"),
		GoogleOAuthClientSecret: Getenv("GOOGLE_OAUTH_CLIENT_SECRET"),
	}

	var err error
	cfg.SMTPPort, err = strconv.Atoi(getEnvDefault("SMTP_PORT", "587"))
	if err != nil {
		return nil, fmt.Errorf("invalid SMTP_PORT: %w", err)
	}
	cfg.SMTPUseTLS = getEnvBool("SMTP_USE_TLS", false)
	cfg.SMTPStartTLS = getEnvBool("SMTP_STARTTLS", true)

	syncMinutes, err := strconv.Atoi(getEnvDefault("CALENDAR_SYNC_INTERVAL_MINUTES", "15"))
	if err != nil {
		return nil, fmt.Errorf("invalid CALENDAR_SYNC_INTERVAL_MINUTES: %w", err)
	}
	cfg.CalendarSyncInterval = time.Duration(syncMinutes) * time.Minute

	cfg.CalendarWindowDays, err = strconv.Atoi(getEnvDefault("CALENDAR_WINDOW_DAYS", "7"))
	if err != nil {
		return nil, fmt.Errorf("invalid CALENDAR_WINDOW_DAYS: %w", err)
	}

	weatherMinutes, err := strconv.Atoi(getEnvDefault("WEATHER_REFRESH_INTERVAL_MINUTES", "15"))
	if err != nil {
		return nil, fmt.Errorf("invalid WEATHER_REFRESH_INTERVAL_MINUTES: %w", err)
	}
	cfg.WeatherRefreshInterval = time.Duration(weatherMinutes) * time.Minute

	pluginSyncMinutes, err := strconv.Atoi(getEnvDefault("PLUGIN_SYNC_INTERVAL_MINUTES", "15"))
	if err != nil {
		return nil, fmt.Errorf("invalid PLUGIN_SYNC_INTERVAL_MINUTES: %w", err)
	}
	cfg.PluginSyncInterval = time.Duration(pluginSyncMinutes) * time.Minute

	releaseCheckMinutes, err := strconv.Atoi(getEnvDefault("RELEASE_CHECK_INTERVAL_MINUTES", "1440"))
	if err != nil {
		return nil, fmt.Errorf("invalid RELEASE_CHECK_INTERVAL_MINUTES: %w", err)
	}
	cfg.ReleaseCheckInterval = time.Duration(releaseCheckMinutes) * time.Minute

	cfg.SessionLifetime = 14 * 24 * time.Hour // parent sessions last 2 weeks by default
	cfg.ApprovalLinkTTL = 7 * 24 * time.Hour  // approval links good for a week
	cfg.InviteLinkTTL = 7 * 24 * time.Hour    // invite links good for a week
	cfg.PasswordResetLinkTTL = 1 * time.Hour  // reset links are short-lived, since they grant account takeover

	cfg.CookieSecure = getEnvBool("COOKIE_SECURE", strings.HasPrefix(cfg.PublicBaseURL, "https://"))

	cfg.WeeklyReportWeekday = time.Sunday
	cfg.WeeklyReportHour = 20 // 8 PM server-local time

	if cfg.DBHost == "" || cfg.DBName == "" || cfg.DBUser == "" {
		return nil, fmt.Errorf("DB_HOST, DB_NAME, and DB_USER must be set")
	}
	// SessionSecret/ApprovalSecret/InviteSecret/PasswordResetSecret are
	// intentionally NOT required here - see the field comments above.
	// main.go generates and persists them (encrypted with EncryptionKey) if
	// left unset.
	if cfg.EncryptionKey == "" {
		return nil, fmt.Errorf("ENCRYPTION_KEY must be set (generate with: openssl rand -hex 32)")
	}

	return cfg, nil
}

// GoogleOAuthEnabled reports whether Google Calendar OAuth2 credentials are
// configured. Used both by the dashboard template (whether to show "Connect
// Google Calendar") and the connect handler (to fail cleanly with a clear
// error instead of constructing an oauth2.Config with empty credentials).
func (c *Config) GoogleOAuthEnabled() bool {
	return c.GoogleOAuthClientID != "" && c.GoogleOAuthClientSecret != ""
}

// GoogleEndpoint is the oauth2.Endpoint used for the Google Calendar connect
// flow. A package-level var (rather than inlining google.Endpoint below) so
// tests can redirect it to a local httptest server, the same pattern
// internal/weather uses for ForecastURL/GeocodeURL.
var GoogleEndpoint = google.Endpoint

// GoogleOAuthConfig builds the oauth2.Config used for the Google Calendar
// connect flow. Lives here (not internal/handlers) so internal/scheduler can
// also use it to build a TokenSource for refreshing access tokens during
// sync, without handlers importing scheduler or vice versa. Cheap to
// construct (no I/O) - callers build a fresh one per use rather than caching
// it on App/Scheduler.
func GoogleOAuthConfig(c *Config) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.GoogleOAuthClientID,
		ClientSecret: c.GoogleOAuthClientSecret,
		RedirectURL:  c.PublicBaseURL + "/parent/calendar-accounts/google/callback",
		Scopes:       []string{"https://www.googleapis.com/auth/calendar.readonly", "openid", "email"},
		Endpoint:     GoogleEndpoint,
	}
}

// DSN builds a libpq-style connection string for pgx.
func (c *Config) DSN() string {
	return fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBName, c.DBUser, c.DBPassword, c.DBSSLMode)
}

func getEnvDefault(key, def string) string {
	if v := Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	v := Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func Getenv(key string) string {
	if path := os.Getenv(key + "_FILE"); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			return string(data)
		}
	}
	return os.Getenv(key)
}
