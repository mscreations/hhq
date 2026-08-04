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

// Command server is the HappyHome Quest application entrypoint.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os/signal"
	"path"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/db"
	"github.com/mscreations/hhq/internal/email"
	"github.com/mscreations/hhq/internal/handlers"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/plugins"
	"github.com/mscreations/hhq/internal/release"
	"github.com/mscreations/hhq/internal/scheduler"
	"github.com/mscreations/hhq/internal/util"
	"github.com/mscreations/hhq/internal/weather"
	webassets "github.com/mscreations/hhq/web"
)

// Version is stamped at build time via -ldflags "-X main.Version=...".
// "dev" is the fallback for a plain `go build` with no ldflags (e.g. running
// tests, or a developer building locally without `make build VERSION=...`).
var Version = "dev"

// attachmentLabel derives a human-readable label for an event attachment
// link from the last path segment of its URI (e.g. "invoice.pdf" from
// ".../files/invoice.pdf"), since the stored Name is actually the ATTACH
// property's FMTTYPE (a MIME type like "application/octet-stream") rather
// than a filename.
func attachmentLabel(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" {
		return uri
	}
	decoded, err := url.PathUnescape(base)
	if err != nil {
		return base
	}
	return decoded
}

// templateDict builds a map[string]any from alternating key/value arguments,
// letting templates pass more than one value into a {{template}} action
// (which otherwise only accepts a single pipeline argument) - used for the
// shared "parent/_head" partial's PageTitle/AppTitle.
func templateDict(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("dict requires an even number of arguments")
	}
	d := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		d[key] = values[i+1]
	}
	return d, nil
}

// bootstrapFromFile reads and parses a bootstrap config file (if present) and hands the
// resulting entries to apply. Reading errors, parse errors, and an empty/missing file are
// all logged and treated as a no-op rather than a fatal startup error. ctx is the app's
// own long-lived, shutdown-aware context (not context.Background()) so that any
// background work apply kicks off (e.g. BootstrapPlugins' registration-retry
// goroutines) stops cleanly on shutdown instead of leaking past it.
func bootstrapFromFile[T any](ctx context.Context, configDir, filename, label string, parse func(string) ([]T, error), apply func(context.Context, []T)) {
	raw, err := config.ReadBootstrapFile(configDir, filename)
	if err != nil {
		logging.Errorf("bootstrap: reading %s: %v", filename, err)
		return
	}
	if raw == "" {
		return
	}
	entries, err := parse(raw)
	if err != nil {
		logging.Errorf("bootstrap: %v", err)
		return
	}
	logging.Debugf("bootstrap: reconciling %d %s from %s", len(entries), label, filename)
	apply(ctx, entries)
}

// loadOrGenerateSecret returns envValue if the operator set it explicitly;
// otherwise it looks up settingsKey in the settings table (encrypted at rest
// with enc) and returns the decrypted value, generating and persisting a new
// random 32-byte hex secret on first run if no stored value exists yet. This
// lets SESSION_SECRET/APPROVAL_SECRET/INVITE_SECRET be optional - only
// ENCRYPTION_KEY needs to be provided by the operator.
func loadOrGenerateSecret(ctx context.Context, settings *models.SettingsStore, enc *util.Encryptor, envValue, settingsKey, label string) (string, error) {
	if envValue != "" {
		return envValue, nil
	}

	stored, err := settings.Get(ctx, settingsKey, "")
	if err != nil {
		return "", fmt.Errorf("loading stored %s: %w", label, err)
	}
	if stored != "" {
		ciphertext, err := hex.DecodeString(stored)
		if err != nil {
			return "", fmt.Errorf("decoding stored %s: %w", label, err)
		}
		plaintext, err := enc.Decrypt(ciphertext)
		if err != nil {
			return "", fmt.Errorf("decrypting stored %s: %w", label, err)
		}
		return plaintext, nil
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating %s: %w", label, err)
	}
	secret := hex.EncodeToString(raw)

	ciphertext, err := enc.Encrypt(secret)
	if err != nil {
		return "", fmt.Errorf("encrypting %s for storage: %w", label, err)
	}
	if err := settings.Set(ctx, settingsKey, hex.EncodeToString(ciphertext)); err != nil {
		return "", fmt.Errorf("storing %s: %w", label, err)
	}
	logging.Infof("%s not set via env var - generated one and stored it encrypted in the database", label)
	return secret, nil
}

func templateNames(t *template.Template) []string {
	var names []string
	for _, tmpl := range t.Templates() {
		if tmpl.Name() != "" {
			names = append(names, tmpl.Name())
		}
	}
	return names
}

func main() {
	logging.Infof("HappyHome Quest starting up (version %s, log level controlled by LOG_LEVEL env var, current effective level shown by debug messages below if LOG_LEVEL=debug; log format controlled by LOG_FORMAT, text or json)", Version)
	logging.Debugf("loading configuration from environment")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	logging.Debugf("config loaded: listen=%s db_host=%s db_name=%s smtp_host=%s sync_interval=%s window_days=%d",
		cfg.ListenAddr, cfg.DBHost, cfg.DBName, cfg.SMTPHost, cfg.CalendarSyncInterval, cfg.CalendarWindowDays)

	logging.Debugf("connecting to database at %s:%s/%s", cfg.DBHost, cfg.DBPort, cfg.DBName)
	conn, err := db.New(cfg.DSN())
	if err != nil {
		log.Fatalf("database connection error: %v", err)
	}
	defer conn.Close()
	logging.Infof("database connection established")

	logging.Debugf("running database migrations")
	if err := db.Migrate(conn); err != nil {
		log.Fatalf("migration error: %v", err)
	}
	logging.Infof("database migrations up to date")

	encryptor, err := util.NewEncryptor(cfg.EncryptionKey)
	if err != nil {
		log.Fatalf("encryption key error: %v", err)
	}
	logging.Debugf("credential encryptor initialized")

	settingsStore := &models.SettingsStore{DB: conn}

	sessionSecret, err := loadOrGenerateSecret(context.Background(), settingsStore, encryptor, cfg.SessionSecret, "internal_session_secret", "SESSION_SECRET")
	if err != nil {
		log.Fatalf("session secret error: %v", err)
	}
	approvalSecret, err := loadOrGenerateSecret(context.Background(), settingsStore, encryptor, cfg.ApprovalSecret, "internal_approval_secret", "APPROVAL_SECRET")
	if err != nil {
		log.Fatalf("approval secret error: %v", err)
	}
	inviteSecret, err := loadOrGenerateSecret(context.Background(), settingsStore, encryptor, cfg.InviteSecret, "internal_invite_secret", "INVITE_SECRET")
	if err != nil {
		log.Fatalf("invite secret error: %v", err)
	}
	passwordResetSecret, err := loadOrGenerateSecret(context.Background(), settingsStore, encryptor, cfg.PasswordResetSecret, "internal_password_reset_secret", "PASSWORD_RESET_SECRET")
	if err != nil {
		log.Fatalf("password reset secret error: %v", err)
	}

	weatherCache := &weather.Cache{}
	releaseCache := &release.Cache{}
	pluginVersionCache := &plugins.VersionCache{}

	templateFuncs := template.FuncMap{
		"colorName":       models.ColorName,
		"attachmentLabel": attachmentLabel,
		"dict":            templateDict,
	}
	templates := template.Must(template.New("root").Funcs(templateFuncs).ParseFS(webassets.FS, "templates/kiosk/*.html"))
	templates = template.Must(templates.ParseFS(webassets.FS, "templates/parent/*.html"))
	logging.Debugf("templates loaded: %v", templateNames(templates))

	app := &handlers.App{
		Cfg:              cfg,
		Version:          Version,
		Users:            &models.UserStore{DB: conn},
		Sessions:         &models.SessionStore{DB: conn},
		CalendarAccounts: &models.CalendarAccountStore{DB: conn},
		Calendars:        &models.CalendarStore{DB: conn},
		Events:           &models.EventStore{DB: conn},
		Chores:           &models.ChoreStore{DB: conn},
		ChoreDefs:        &models.ChoreDefinitionStore{DB: conn},
		ChoreInstances:   &models.ChoreInstanceStore{DB: conn},
		Settings:         settingsStore,
		Weather:          weatherCache,
		Plugins:          &models.PluginStore{DB: conn},
		Release:          releaseCache,
		PluginVersions:   pluginVersionCache,
		Approval:         auth.NewApprovalLinkSigner(approvalSecret),
		Invite:           auth.NewApprovalLinkSigner(inviteSecret),
		PasswordReset:    auth.NewApprovalLinkSigner(passwordResetSecret),
		GoogleOAuthState: auth.NewApprovalLinkSigner(sessionSecret),
		Encryptor:        encryptor,
		Templates:        templates,
		Mailer: &email.Sender{
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			Username: cfg.SMTPUser,
			Password: cfg.SMTPPassword,
			UseTLS:   cfg.SMTPUseTLS,
			StartTLS: cfg.SMTPStartTLS,
			From:     cfg.SMTPFrom,
		},
	}
	if !cfg.CookieSecure {
		logging.Warnf("startup: session cookie is being issued WITHOUT the Secure attribute (PUBLIC_BASE_URL is not https:// and COOKIE_SECURE was not set to true) - the session cookie can be read by anyone on the network path. Set PUBLIC_BASE_URL to an https:// URL or COOKIE_SECURE=true unless this is a trusted local-only deployment.")
	}
	app.SessionMgr = &auth.SessionManager{
		Sessions: app.Sessions,
		Users:    app.Users,
		TTL:      cfg.SessionLifetime,
		Secure:   cfg.CookieSecure, // see internal/config/config.go - defaults from PublicBaseURL's scheme, or COOKIE_SECURE env var
	}
	app.CSRF = auth.NewCSRFManager(sessionSecret)
	app.LoginLimiter = auth.NewLoginLimiter(10, 15*time.Minute) // 10 failed attempts per key (per-IP and per-email) per 15 minutes

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	configDir := config.ConfigDir()
	logging.Debugf("scanning %s for bootstrap config files (parents.json, calendars.json, children.json, chores.json, assignments.json, plugins.json)", configDir)

	bootstrapFromFile(ctx, configDir, "parents.json", "parent(s)", config.ParseParentsBootstrap, app.BootstrapParents)
	bootstrapFromFile(ctx, configDir, "calendars.json", "calendar account(s)", config.ParseCalendarAccountsBootstrap, app.BootstrapCalendarAccounts)
	bootstrapFromFile(ctx, configDir, "children.json", "child(ren)", config.ParseChildrenBootstrap, app.BootstrapChildren)
	bootstrapFromFile(ctx, configDir, "chores.json", "chore(s)", config.ParseChoresBootstrap, app.BootstrapChores)
	bootstrapFromFile(ctx, configDir, "assignments.json", "assignment(s)", config.ParseAssignmentsBootstrap, app.BootstrapAssignments)
	bootstrapFromFile(ctx, configDir, "plugins.json", "plugin(s)", config.ParsePluginsBootstrap, app.BootstrapPlugins)
	app.SchedulePluginCalendarCleanup(ctx)

	if parents, err := app.Users.ListParents(ctx); err != nil {
		logging.Errorf("bootstrap: checking for existing parent users: %v", err)
	} else if len(parents) == 0 {
		logging.Warnf("bootstrap: no parent users exist - add one to parents.json and restart, or insert a row directly, or you won't be able to log in.")
	}

	if location, latStr, lonStr, units := config.Getenv("WEATHER_LOCATION"), config.Getenv("WEATHER_LAT"), config.Getenv("WEATHER_LON"), config.Getenv("WEATHER_UNITS"); location != "" || (latStr != "" && lonStr != "") {
		var lat, lon float64
		hasCoords := latStr != "" && lonStr != ""
		if hasCoords {
			var err error
			if lat, err = strconv.ParseFloat(latStr, 64); err != nil {
				logging.Errorf("bootstrap: WEATHER_LAT is not a valid number, skipping: %v", err)
				hasCoords = false
			} else if lon, err = strconv.ParseFloat(lonStr, 64); err != nil {
				logging.Errorf("bootstrap: WEATHER_LON is not a valid number, skipping: %v", err)
				hasCoords = false
			}
		}
		if hasCoords || location != "" {
			app.BootstrapWeatherLocation(context.Background(), location, lat, lon, hasCoords, units)
		}
	}

	sched := &scheduler.Scheduler{
		Cfg:              cfg,
		CalendarAccounts: app.CalendarAccounts,
		Calendars:        app.Calendars,
		Events:           app.Events,
		ChoreInstances:   app.ChoreInstances,
		Users:            app.Users,
		Sessions:         app.Sessions,
		Settings:         app.Settings,
		Encryptor:        encryptor,
		Mailer:           app.Mailer,
		LoginLimiter:     app.LoginLimiter,
		Weather:          weatherCache,
		Plugins:          app.Plugins,
		Release:          releaseCache,
		PluginVersions:   pluginVersionCache,
		Version:          Version,
	}

	logging.Debugf("starting background scheduler (calendar sync every %s, chore generation, session cleanup, weekly report)", cfg.CalendarSyncInterval)
	sched.Run(ctx)

	router := buildRouter(app)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logging.Infof("%s listening on %s", cfg.AppTitle, cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	logging.Infof("shutdown signal received, shutting down gracefully...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logging.Errorf("error during shutdown: %v", err)
	} else {
		logging.Infof("shutdown complete")
	}
}

func buildRouter(app *handlers.App) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP) // trusts X-Forwarded-For from the Traefik reverse proxy in front of this app
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(app.SessionMgr.LoadUser)
	if logging.DebugEnabled() {
		r.Use(debugRequestLogger)
	}

	staticFS, err := fs.Sub(webassets.FS, "static")
	if err != nil {
		log.Fatalf("static assets error: %v", err)
	}
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	logging.Debugf("static assets served from embedded filesystem")

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// --- Kiosk: unauthenticated, view-only + chore-tap-to-complete ---
	r.Get("/", app.KioskIndex)
	r.Get("/kiosk/fragments/home", app.KioskFragmentHome)
	r.Get("/kiosk/fragments/week", app.KioskFragmentWeek)
	r.Get("/kiosk/fragments/agenda", app.KioskFragmentAgenda)
	r.Get("/kiosk/fragments/calendar", app.KioskFragmentCalendar)
	r.Get("/kiosk/fragments/chores", app.KioskFragmentChores)
	r.Post("/kiosk/chores/{id}/complete", app.KioskCompleteChore)
	r.Get("/kiosk/events/{id}", app.KioskEventDetail)
	r.Post("/kiosk/events/{id}/actions/{actionID}", app.KioskEventAction)
	r.Get("/kiosk/fragments/weather", app.KioskFragmentWeather)
	r.Get("/kiosk/weather/page", app.KioskWeatherPage)
	r.Get("/kiosk/view/plugin/{id}/{viewID}", app.KioskPluginView)

	// --- Avatars: unauthenticated (shown on the unauthenticated kiosk screen) ---
	r.Get("/avatars/{id}", app.ServeAvatar)

	// --- Public, signed-token approval links clicked from parent emails ---
	r.Get("/approval/respond", app.ApprovalRespond)

	// --- Public, signed-token parent invite links clicked from invite emails ---
	r.Get("/invite/accept", app.InviteAcceptPage)
	r.Post("/invite/accept", app.InviteAcceptSubmit)

	// --- First-run setup: only reachable until the first parent exists ---
	r.Get("/setup", app.SetupPage)
	r.Post("/setup", app.SetupSubmit)

	// --- Login ---
	r.Get("/login", app.LoginPage)
	r.Post("/login", app.LoginSubmit)
	r.Get("/logout", app.Logout)

	// --- Forgot/reset password ---
	r.Get("/forgot-password", app.ForgotPasswordPage)
	r.Post("/forgot-password", app.ForgotPasswordSubmit)
	r.Get("/reset-password", app.ResetPasswordPage)
	r.Post("/reset-password", app.ResetPasswordSubmit)

	// --- Parent dashboard: requires an authenticated parent session ---
	r.Group(func(r chi.Router) {
		r.Use(app.SessionMgr.RequireParent)
		r.Use(app.SessionMgr.VerifyCSRF(app.CSRF))

		r.Get("/parent", app.ParentDashboard)
		r.Get("/parent/report", app.WeeklyReportDownload)
		r.Post("/parent/settings", app.UpdateSettings)

		r.Post("/parent/users/children", app.CreateChild)
		r.Post("/parent/users/parents", app.CreateParent)
		r.Post("/parent/users/{id}/resend-invite", app.ResendParentInvite)
		r.Post("/parent/users/{id}/display-name", app.SetParentDisplayName)
		r.Post("/parent/users/{id}/avatar", app.UploadUserAvatar)
		r.Post("/parent/users/{id}/avatar/remove", app.RemoveUserAvatar)
		r.Post("/parent/users/{id}/remove", app.RemoveUser)

		r.Post("/parent/calendar-accounts", app.CreateCalendarAccount)
		r.Get("/parent/calendar-accounts/{id}/edit", app.EditCalendarAccountPage)
		r.Post("/parent/calendar-accounts/{id}", app.UpdateCalendarAccount)
		r.Post("/parent/calendar-accounts/{id}/delete", app.DeleteCalendarAccount)
		r.Post("/parent/calendar-accounts/{id}/resync", app.ResyncCalendarAccount)
		r.Get("/parent/calendar-accounts/refresh", app.RefreshCalendarAccounts)
		r.Get("/parent/calendar-accounts/google/connect", app.GoogleConnectStart)
		r.Get("/parent/calendar-accounts/google/callback", app.GoogleConnectCallback)
		r.Post("/parent/calendars/{id}/toggle", app.ToggleCalendarEnabled)
		r.Post("/parent/calendars/{id}/color", app.SetCalendarColor)

		r.Post("/parent/chores/definitions", app.CreateChoreDefinition)
		r.Post("/parent/chores/definitions/{id}/deactivate", app.DeactivateChoreDefinition)
		r.Post("/parent/chores/catalog/{id}", app.UpdateChore)
		r.Post("/parent/chores/catalog/{id}/deactivate", app.DeactivateChore)
		r.Post("/parent/chores/{id}/decide", app.ParentDecideChore)
		r.Post("/parent/chores/{id}/reset", app.ParentResetRejectedChore)

		r.Post("/parent/plugins/{id}/toggle", app.TogglePlugin)
		r.Get("/parent/plugins/{id}/settings", app.PluginSettingsPage)
		r.Post("/parent/plugins/{id}/settings", app.PluginSettingsPage)
	})

	return r
}

// debugRequestLogger adds extra per-request detail beyond chi's standard
// access log (method/path/status/duration) - only mounted when
// LOG_LEVEL=debug, since it's noisier and mostly useful while troubleshooting
// (e.g. confirming a request actually carried a session cookie, or which
// remote address it came from behind Traefik).
func debugRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, cookieErr := r.Cookie(cookieNameForLogging)
		logging.Debugf("request: %s %s remote=%s has_session_cookie=%v", r.Method, r.URL.Path, r.RemoteAddr, cookieErr == nil)
		next.ServeHTTP(w, r)
	})
}

const cookieNameForLogging = "hhq_session"
