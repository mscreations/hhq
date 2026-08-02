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

// Package scheduler runs the app's recurring background jobs in a single
// goroutine tree: periodic CalDAV sync, daily chore-instance generation,
// session cleanup, and the weekly report email.
package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/caldav"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/email"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/plugins"
	"github.com/mscreations/hhq/internal/release"
	"github.com/mscreations/hhq/internal/report"
	"github.com/mscreations/hhq/internal/util"
	"github.com/mscreations/hhq/internal/weather"
)

type Scheduler struct {
	Cfg              *config.Config
	CalendarAccounts *models.CalendarAccountStore
	Calendars        *models.CalendarStore
	Events           *models.EventStore
	ChoreInstances   *models.ChoreInstanceStore
	Users            *models.UserStore
	Sessions         *models.SessionStore
	Settings         *models.SettingsStore
	Encryptor        *util.Encryptor
	Mailer           *email.Sender
	LoginLimiter     *auth.LoginLimiter
	Weather          *weather.Cache
	Plugins          *models.PluginStore
	Release          *release.Cache
	PluginVersions   *plugins.VersionCache

	// Version is the running app's version (see main.Version), used only to
	// decide which of GitHub's APIs runReleaseCheck polls - see checkRelease.
	Version string
}

// Run blocks forever, dispatching each job on its own ticker. Intended to be
// started in a goroutine from cmd/server/main.go.
func (s *Scheduler) Run(ctx context.Context) {
	logging.Debugf("scheduler: launching background jobs (calendar sync every %s, chore generation hourly, session cleanup every 6h, weekly report check hourly)", s.Cfg.CalendarSyncInterval)
	go s.runCalendarSync(ctx)
	go s.runDailyChoreGeneration(ctx)
	go s.runSessionCleanup(ctx)
	go s.runWeeklyReport(ctx)
	go s.runWeatherRefresh(ctx)
	go s.runPluginSync(ctx)
	go s.runReleaseCheck(ctx)
	go s.runPluginVersionCheck(ctx)
}

func (s *Scheduler) runCalendarSync(ctx context.Context) {
	logging.Debugf("scheduler: running initial calendar sync on startup")
	s.syncAllAccounts(ctx) // sync once immediately on startup, then on the ticker
	ticker := time.NewTicker(s.Cfg.CalendarSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logging.Debugf("scheduler: calendar sync loop stopping (context canceled)")
			return
		case <-ticker.C:
			logging.Debugf("scheduler: calendar sync tick")
			s.syncAllAccounts(ctx)
		}
	}
}

func (s *Scheduler) syncAllAccounts(ctx context.Context) {
	// Every account is synced regardless of its calendars' enabled state -
	// enabled/disabled now lives per-calendar (see models.Calendar), and we
	// still want to run discovery on disabled-everything accounts so that
	// newly-created server-side calendars get picked up (appearing disabled
	// by default would be a reasonable alternative, but auto-discovery
	// staying silent forever if a parent temporarily disabled every existing
	// calendar would be more surprising).
	accounts, err := s.CalendarAccounts.ListAll(ctx)
	if err != nil {
		logging.Errorf("scheduler: listing calendar accounts: %v", err)
		return
	}
	logging.Debugf("scheduler: syncing %d calendar account(s)", len(accounts))

	for _, account := range accounts {
		s.syncOneAccount(ctx, account)
	}
}

// syncOneAccount dispatches a single account to the right sync path based on
// its provider - pulled out of syncAllAccounts's loop body so the dispatch
// logic (including the defensive "unknown provider" fallback, which the
// calendar_provider Postgres enum makes unreachable via the normal
// ListAll-fed path today) is directly testable with a hand-built
// models.CalendarAccount, without needing to smuggle an invalid enum value
// into the database.
func (s *Scheduler) syncOneAccount(ctx context.Context, account models.CalendarAccount) {
	logging.Debugf("scheduler: syncing account %q (id=%d, provider=%s)", account.Name, account.ID, account.Provider)

	if account.Provider == models.ProviderPlugin {
		// Synthetic calendar accounts auto-created for plugins (see
		// internal/handlers/plugin_bootstrap.go) have no password and are
		// synced separately by runPluginSync, not through the real CalDAV
		// path - skip silently before attempting to decrypt anything.
		return
	}

	if account.Provider == models.ProviderGoogle {
		// Google accounts have no encrypted_password (they authenticate
		// via OAuth2 refresh token instead) - handled entirely separately
		// from the CalDAV password-decrypt-then-dispatch path below, the
		// same way ProviderPlugin is skipped above.
		refreshToken, err := s.Encryptor.Decrypt(account.EncryptedRefreshToken)
		if err != nil {
			logging.Errorf("scheduler: decrypting refresh token for account %q: %v", account.Name, err)
			_ = s.CalendarAccounts.MarkSynced(ctx, account.ID, err)
			return
		}
		err = caldav.SyncGoogleAccount(ctx, s.Calendars, s.Events, account, refreshToken, config.GoogleOAuthConfig(s.Cfg), s.Cfg.CalendarWindowDays)
		if err != nil {
			logging.Errorf("scheduler: syncing account %q failed: %v", account.Name, err)
		} else {
			logging.Debugf("scheduler: account %q synced successfully", account.Name)
		}
		_ = s.CalendarAccounts.MarkSynced(ctx, account.ID, err)
		return
	}

	password, err := s.Encryptor.Decrypt(account.EncryptedPassword)
	if err != nil {
		logging.Errorf("scheduler: decrypting password for account %q: %v", account.Name, err)
		_ = s.CalendarAccounts.MarkSynced(ctx, account.ID, err)
		return
	}

	switch account.Provider {
	case models.ProviderFastmail, models.ProviderICloud, models.ProviderGeneric:
		err = caldav.DiscoverAndSyncAccount(ctx, s.Calendars, s.Events, account, password, s.Cfg.CalendarWindowDays)
	default:
		err = fmt.Errorf("unknown provider %q", account.Provider)
	}

	if err != nil {
		logging.Errorf("scheduler: syncing account %q failed: %v", account.Name, err)
	} else {
		logging.Debugf("scheduler: account %q synced successfully", account.Name)
	}
	_ = s.CalendarAccounts.MarkSynced(ctx, account.ID, err)
}

// runDailyChoreGeneration ensures today's (and, as a safety net, the next 2
// days') chore instances exist, independent of the kiosk page also calling
// EnsureForDate on load — this way chores exist even before the kiosk is
// first viewed each day.
func (s *Scheduler) runDailyChoreGeneration(ctx context.Context) {
	s.generateChoreInstances(ctx)

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.generateChoreInstances(ctx)
		}
	}
}

// generateChoreInstances ensures today's (and the next 2 days') chore
// instances exist - the actual work performed both on startup and on each
// hourly tick, pulled into its own method (rather than a local closure) so
// it's directly callable from tests without waiting on the real, hardcoded
// 1h ticker.
func (s *Scheduler) generateChoreInstances(ctx context.Context) {
	now := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.ChoreInstances.EnsureForDate(ctx, now.AddDate(0, 0, i)); err != nil {
			logging.Errorf("scheduler: generating chore instances for %s: %v", now.AddDate(0, 0, i).Format("2006-01-02"), err)
		}
	}
	logging.Debugf("scheduler: chore instances ensured for today + next 2 days")
}

// runSessionCleanup also sweeps the login rate limiter on the same ticker -
// both are "prune stale in-memory/DB state" housekeeping jobs with no need
// for their own separate schedule.
func (s *Scheduler) runSessionCleanup(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupSessions(ctx)
		}
	}
}

// cleanupSessions deletes expired sessions and sweeps the login rate
// limiter - the actual work performed on each 6h tick, pulled into its own
// method so it's directly callable from tests without waiting on the real,
// hardcoded 6h ticker.
func (s *Scheduler) cleanupSessions(ctx context.Context) {
	if err := s.Sessions.DeleteExpired(ctx); err != nil {
		logging.Errorf("scheduler: cleaning expired sessions: %v", err)
	} else {
		logging.Debugf("scheduler: expired sessions cleaned up")
	}
	if s.LoginLimiter != nil {
		s.LoginLimiter.Sweep()
		logging.Debugf("scheduler: login rate limiter swept")
	}
}

// Settings keys letting a parent override config.WeeklyReportWeekday/Hour
// from the dashboard, without needing a restart - mirrors the weather
// package's Setting* keys (internal/weather/cache.go). Unset/invalid values
// fall back to the config defaults, so these two env vars remain the
// initial/no-DB-yet source of truth (e.g. first boot before any settings row
// exists).
const (
	SettingWeeklyReportWeekday = "weekly_report_weekday"
	SettingWeeklyReportHour    = "weekly_report_hour"
)

// loadWeeklyReportSchedule resolves the effective weekday/hour to send the
// weekly report, preferring a parent-configured settings override over
// s.Cfg's env-var-sourced defaults. Falls back to the config value if
// Settings is nil (e.g. some unit tests construct a bare Scheduler), the
// setting is unset, or its stored value is out of range.
func (s *Scheduler) loadWeeklyReportSchedule(ctx context.Context) (time.Weekday, int) {
	weekday, hour := s.Cfg.WeeklyReportWeekday, s.Cfg.WeeklyReportHour
	if s.Settings == nil {
		return weekday, hour
	}
	if v, err := s.Settings.Get(ctx, SettingWeeklyReportWeekday, ""); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 6 {
			weekday = time.Weekday(n)
		}
	}
	if v, err := s.Settings.Get(ctx, SettingWeeklyReportHour, ""); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 23 {
			hour = n
		}
	}
	return weekday, hour
}

// runWeeklyReport checks once an hour whether it's time to send the weekly
// report (per the settings-overridable config.WeeklyReportWeekday/Hour, see
// loadWeeklyReportSchedule) and, if so, generates and emails it to all
// parents. A simple "already sent this week" guard based on the last sent
// timestamp avoids double-sends if the check fires twice in the same hour
// window.
func (s *Scheduler) runWeeklyReport(ctx context.Context) {
	var lastSentWeek string

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lastSentWeek = s.maybeSendWeeklyReport(ctx, time.Now(), lastSentWeek)
		}
	}
}

// maybeSendWeeklyReport checks whether now falls within the configured
// weekly-report weekday/hour and, if so and it hasn't already been sent for
// that week (per lastSentWeek), generates and sends it - returning the
// (possibly updated) lastSentWeek value for the caller to carry into the
// next tick. Pulled out of runWeeklyReport's ticker case so this decision
// logic is directly testable with an arbitrary `now`, rather than needing
// to wait on the real, hardcoded 1h ticker or the real wall clock to reach
// the configured weekday/hour.
func (s *Scheduler) maybeSendWeeklyReport(ctx context.Context, now time.Time, lastSentWeek string) string {
	weekday, hour := s.loadWeeklyReportSchedule(ctx)
	if now.Weekday() != weekday || now.Hour() != hour {
		return lastSentWeek
	}
	weekKey := now.Format("2006-01-02")
	if weekKey == lastSentWeek {
		return lastSentWeek
	}
	logging.Infof("scheduler: generating and sending weekly report")
	if err := s.sendWeeklyReport(ctx, now); err != nil {
		logging.Errorf("scheduler: sending weekly report: %v", err)
		return lastSentWeek
	}
	logging.Infof("scheduler: weekly report sent")
	return weekKey
}

func (s *Scheduler) sendWeeklyReport(ctx context.Context, now time.Time) error {
	weekStart := startOfWeek(now.AddDate(0, 0, -6)) // report on the week that just ended
	instances, err := s.ChoreInstances.ListForWeek(ctx, weekStart)
	if err != nil {
		return err
	}

	pdfBytes, err := report.BuildWeeklyPDF(weekStart, instances)
	if err != nil {
		return err
	}

	parents, err := s.Users.ListParents(ctx)
	if err != nil {
		return err
	}
	var to []string
	for _, p := range parents {
		if p.Email.Valid {
			to = append(to, p.Email.String)
		}
	}
	if len(to) == 0 {
		return nil
	}

	appTitle, err := s.Settings.Get(ctx, "app_title", s.Cfg.AppTitle)
	if err != nil {
		logging.Errorf("scheduler: loading app_title setting: %v", err)
		appTitle = s.Cfg.AppTitle
	}

	subject := fmt.Sprintf("Weekly Chore Report - week of %s", weekStart.Format("Jan 2"))
	body := "<p>Attached is this week's chore report.</p>"
	return s.Mailer.Send(appTitle, to, subject, body, email.Attachment{
		Filename:    fmt.Sprintf("chore-report-%s.pdf", weekStart.Format("2006-01-02")),
		ContentType: "application/pdf",
		Data:        pdfBytes,
	})
}

// runWeatherRefresh periodically re-fetches the forecast for whatever
// location is currently configured in settings, mirroring runCalendarSync's
// shape (fetch once immediately on startup, then on a ticker).
func (s *Scheduler) runWeatherRefresh(ctx context.Context) {
	s.refreshWeather(ctx)
	ticker := time.NewTicker(s.Cfg.WeatherRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshWeather(ctx)
		}
	}
}

func (s *Scheduler) refreshWeather(ctx context.Context) {
	lat, lon, units, ok, err := weather.LoadLocation(ctx, s.Settings)
	if err != nil {
		logging.Errorf("scheduler: loading weather location settings: %v", err)
		return
	}
	if !ok {
		logging.Debugf("scheduler: no weather location configured yet, skipping refresh")
		return
	}

	forecast, err := weather.FetchForecast(ctx, lat, lon, units)
	if err != nil {
		logging.Errorf("scheduler: fetching weather forecast: %v", err)
		return
	}
	s.Weather.Set(forecast)
	logging.Debugf("scheduler: weather forecast refreshed (lat=%.4f, lon=%.4f)", lat, lon)
}

// runReleaseCheck periodically polls GitHub for the latest published release,
// mirroring runWeatherRefresh's shape (fetch once immediately on startup,
// then on a ticker) - drives the parent dashboard's "Update Available" badge.
func (s *Scheduler) runReleaseCheck(ctx context.Context) {
	s.checkRelease(ctx)
	ticker := time.NewTicker(s.Cfg.ReleaseCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkRelease(ctx)
		}
	}
}

// checkRelease polls GitHub for the newest known version. A promoted-release
// build (the default) checks /releases/latest, same as always. A dev build
// (Version ends in "-dev") checks the tags API instead: dev only ever gets
// git tags pushed on every commit, never a GitHub Release (only
// version-main.yml's promotion to main cuts one of those), so
// /releases/latest would only ever reflect main and never show a dev build
// as up to date with dev's own newest tag.
func (s *Scheduler) checkRelease(ctx context.Context) {
	fetch := release.FetchLatest
	if strings.HasSuffix(s.Version, "-dev") {
		fetch = release.FetchLatestTag
	}

	latest, err := fetch(ctx)
	if err != nil {
		logging.Debugf("scheduler: checking for a newer release: %v", err)
		return
	}
	s.Release.Set(latest)
	logging.Debugf("scheduler: latest known release is %s", latest.Version)
}

func startOfWeek(t time.Time) time.Time {
	daysSinceSunday := int(t.Weekday())
	d := t.AddDate(0, 0, -daysSinceSunday)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
}
