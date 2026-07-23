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

package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type CalendarProvider string

const (
	ProviderFastmail CalendarProvider = "caldav_fastmail"
	ProviderICloud   CalendarProvider = "caldav_icloud"
	ProviderGeneric  CalendarProvider = "caldav_generic"
	ProviderGoogle   CalendarProvider = "google" // not yet implemented; reserved, see internal/caldav/google.go TODO
	// ProviderPlugin marks a calendar_accounts row auto-created to hold one
	// plugin's synthetic events (see internal/plugins, internal/models/plugin.go).
	// Never synced by the real CalDAV sync loop (internal/scheduler.syncAllAccounts,
	// internal/handlers/sync.go's syncAccountAsync both skip it) - it's synced
	// separately by internal/scheduler's runPluginSync.
	ProviderPlugin CalendarProvider = "plugin"
)

// DefaultCalDAVURL returns explicitURL unchanged if non-empty, otherwise the
// well-known CalDAV root for providers that have one (Fastmail, iCloud).
// Generic CalDAV accounts have no default - the caller must supply a URL.
// Shared between the parent dashboard's "add account" handler and the
// CALENDAR_ACCOUNTS bootstrap path so both know the same two well-known URLs.
func DefaultCalDAVURL(provider CalendarProvider, explicitURL string) string {
	if explicitURL != "" {
		return explicitURL
	}
	switch provider {
	case ProviderFastmail:
		return "https://caldav.fastmail.com/dav/"
	case ProviderICloud:
		return "https://caldav.icloud.com/"
	}
	return ""
}

// CalendarAccount holds the CalDAV login credentials for one account (a
// Fastmail login, an iCloud login, etc). One account can have several
// individual calendars underneath it - see Calendar below, which is where
// color and enabled/disabled now live, since a single Fastmail or iCloud
// login commonly has multiple calendars (Home, Work, Kids...) that a parent
// would want to distinguish and control independently on the kiosk.
type CalendarAccount struct {
	ID                    int
	Name                  string
	Provider              CalendarProvider
	CalDAVURL             sql.NullString
	Username              sql.NullString
	EncryptedPassword     []byte
	EncryptedRefreshToken []byte         // OAuth2 refresh token for provider=google, encrypted like EncryptedPassword; NULL otherwise
	GoogleEmail           sql.NullString // connected Google account's email, for "Connected as X" display; not a secret
	LastSyncedAt          sql.NullTime   // last successful/attempted discovery+sync pass for this account
	LastSyncError         sql.NullString
	BootstrapManaged      bool // true if created/kept in sync by CALENDAR_ACCOUNTS bootstrap config, not the parent UI
	CreatedAt             time.Time
}

type CalendarAccountStore struct {
	DB *sql.DB
}

const calendarAccountColumns = `id, name, provider, caldav_url, username, encrypted_password, encrypted_refresh_token, google_email, last_synced_at, last_sync_error, bootstrap_managed, created_at`

func (s *CalendarAccountStore) ListAll(ctx context.Context) ([]CalendarAccount, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT `+calendarAccountColumns+`
		FROM hhq_calendar_accounts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalendarAccounts(rows)
}

func (s *CalendarAccountStore) GetByID(ctx context.Context, id int) (*CalendarAccount, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT `+calendarAccountColumns+`
		FROM hhq_calendar_accounts WHERE id = $1`, id)
	a, err := scanCalendarAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	return a, err
}

// GetByName looks up an account by its display name (case-sensitive, exact
// match) - used by the CALENDAR_ACCOUNTS bootstrap path to decide whether an
// entry in the config already has a matching row to upsert.
func (s *CalendarAccountStore) GetByName(ctx context.Context, name string) (*CalendarAccount, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT `+calendarAccountColumns+`
		FROM hhq_calendar_accounts WHERE name = $1`, name)
	a, err := scanCalendarAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	return a, err
}

func (s *CalendarAccountStore) Create(ctx context.Context, a CalendarAccount) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_calendar_accounts (name, provider, caldav_url, username, encrypted_password, bootstrap_managed)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		a.Name, a.Provider, a.CalDAVURL, a.Username, a.EncryptedPassword, a.BootstrapManaged).Scan(&id)
	return id, err
}

// CreateGoogle inserts a new Google Calendar account, created via the OAuth2
// connect flow (internal/handlers/google_oauth.go) rather than the parent
// dashboard's CalDAV form - it has no caldav_url/username/password, only an
// encrypted refresh token and the connected account's email.
func (s *CalendarAccountStore) CreateGoogle(ctx context.Context, name string, encryptedRefreshToken []byte, googleEmail string) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_calendar_accounts (name, provider, encrypted_refresh_token, google_email)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		name, ProviderGoogle, encryptedRefreshToken, googleEmail).Scan(&id)
	return id, err
}

// UpdateGoogleToken replaces a Google account's stored refresh token and
// connected email - used when a parent reconnects (e.g. after the token was
// revoked or expired).
func (s *CalendarAccountStore) UpdateGoogleToken(ctx context.Context, id int, encryptedRefreshToken []byte, googleEmail string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_calendar_accounts SET encrypted_refresh_token = $2, google_email = $3
		WHERE id = $1`,
		id, encryptedRefreshToken, googleEmail)
	return err
}

// UpdateGoogleName renames a Google account. Unlike Update (used for CalDAV
// accounts), a Google account's edit form only ever offers a label field -
// there's no url/username/password to change - so this only touches name.
func (s *CalendarAccountStore) UpdateGoogleName(ctx context.Context, id int, name string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_calendar_accounts SET name = $2 WHERE id = $1`, id, name)
	return err
}

// Update edits an existing calendar account. newEncryptedPassword may be nil
// to leave the currently stored password unchanged (used when the parent
// edits other fields without re-entering the app password).
func (s *CalendarAccountStore) Update(ctx context.Context, id int, name, caldavURL, username string, newEncryptedPassword []byte) error {
	if newEncryptedPassword != nil {
		_, err := s.DB.ExecContext(ctx, `
			UPDATE hhq_calendar_accounts SET name = $2, caldav_url = $3, username = $4, encrypted_password = $5
			WHERE id = $1`,
			id, name, nullableString(caldavURL), nullableString(username), newEncryptedPassword)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_calendar_accounts SET name = $2, caldav_url = $3, username = $4
		WHERE id = $1`,
		id, name, nullableString(caldavURL), nullableString(username))
	return err
}

// UpdateBootstrap refreshes a bootstrap-managed account's provider/url/
// username/password to match its current CALENDAR_ACCOUNTS config entry.
// Unlike Update (used by the parent UI, where a blank password field means
// "keep the existing one"), this always overwrites the password since the
// bootstrap config always supplies one, and also updates provider since a
// bootstrap entry's provider can change between restarts (the UI's edit form
// never offers changing provider after creation).
func (s *CalendarAccountStore) UpdateBootstrap(ctx context.Context, id int, provider CalendarProvider, caldavURL, username string, encryptedPassword []byte) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_calendar_accounts
		SET provider = $2, caldav_url = $3, username = $4, encrypted_password = $5
		WHERE id = $1`,
		id, provider, nullableString(caldavURL), nullableString(username), encryptedPassword)
	return err
}

// Delete removes the account and, via ON DELETE CASCADE, all of its
// calendars (and, in turn, their cached events).
func (s *CalendarAccountStore) Delete(ctx context.Context, id int) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM hhq_calendar_accounts WHERE id = $1`, id)
	return err
}

// MarkSynced records the result of an account-level discovery+sync pass
// (e.g. "could not authenticate" failures show up here, since they happen
// before we even know what calendars exist). Per-calendar failures (e.g. one
// specific calendar's REPORT request fails) are recorded on that Calendar
// row instead via CalendarStore.MarkSynced.
func (s *CalendarAccountStore) MarkSynced(ctx context.Context, id int, syncErr error) error {
	var errText sql.NullString
	if syncErr != nil {
		errText = sql.NullString{String: syncErr.Error(), Valid: true}
	}
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_calendar_accounts SET last_synced_at = now(), last_sync_error = $2 WHERE id = $1`,
		id, errText)
	return err
}

func scanCalendarAccounts(rows *sql.Rows) ([]CalendarAccount, error) {
	var out []CalendarAccount
	for rows.Next() {
		a, err := scanCalendarAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func scanCalendarAccount(row rowScanner) (*CalendarAccount, error) {
	var a CalendarAccount
	err := row.Scan(&a.ID, &a.Name, &a.Provider, &a.CalDAVURL, &a.Username, &a.EncryptedPassword, &a.EncryptedRefreshToken, &a.GoogleEmail, &a.LastSyncedAt, &a.LastSyncError, &a.BootstrapManaged, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

var ErrAccountNotFound = errors.New("calendar account not found")

// --- Individual calendars (one calendar_account can have several) ---

// colorPalette is a curated set of visually distinct colors assigned
// automatically to newly-discovered calendars, so calendars from different
// accounts don't end up looking identical on the kiosk. Colors are handed
// out in order, skipping any already in use by another calendar; if more
// calendars exist than palette entries, it cycles (some repeats become
// unavoidable, but this comfortably covers realistic family setups).
var colorPalette = []string{
	"#3B82F6", // blue
	"#EF4444", // red
	"#22C55E", // green
	"#F59E0B", // amber
	"#A855F7", // purple
	"#EC4899", // pink
	"#14B8A6", // teal
	"#F97316", // orange
	"#6366F1", // indigo
	"#84CC16", // lime
	"#06B6D4", // cyan
	"#D946EF", // fuchsia
	"#EAB308", // yellow
	"#10B981", // emerald
	"#F43F5E", // rose
	"#8B5CF6", // violet
	"#0EA5E9", // sky
	"#64748B", // slate
	"#1E3A8A", // navy
	"#991B1B", // maroon
	"#166534", // forest
	"#CA8A04", // mustard
	"#92400E", // brown
	"#7C3AED", // plum
}

// PaletteColors returns the palette calendars are auto-assigned colors from,
// in a stable order - used by the parent dashboard to render a color picker
// so a parent can override the auto-assigned color (via CalendarStore.SetColor)
// with another palette entry rather than typing a raw hex code.
func PaletteColors() []string {
	out := make([]string, len(colorPalette))
	copy(out, colorPalette)
	return out
}

// Calendar is one individual CalDAV calendar (e.g. "Home", "Work") under a
// CalendarAccount. This is the level at which color and enabled/disabled
// live, since one login commonly has multiple calendars a parent wants to
// distinguish and control independently.
type Calendar struct {
	ID                int
	CalendarAccountID int
	AccountName       string           // joined, for parent dashboard display
	Provider          CalendarProvider // joined, for parent dashboard display
	ExternalPath      string           // the CalDAV path used to query this specific calendar
	Name              string           // display name as reported by the server
	Color             string
	Enabled           bool
	LastSyncedAt      sql.NullTime
	LastSyncError     sql.NullString
	CreatedAt         time.Time
}

type CalendarStore struct {
	DB *sql.DB
}

func (s *CalendarStore) ListForAccount(ctx context.Context, accountID int) ([]Calendar, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT c.id, c.calendar_account_id, a.name, a.provider, c.external_path, c.name, c.color, c.enabled, c.last_synced_at, c.last_sync_error, c.created_at
		FROM hhq_calendars c
		JOIN hhq_calendar_accounts a ON a.id = c.calendar_account_id
		WHERE c.calendar_account_id = $1
		ORDER BY c.name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalendars(rows)
}

// ListAllWithAccount returns every calendar across every account, joined
// with account name/provider - used to render the parent dashboard's
// calendar management section grouped by account.
func (s *CalendarStore) ListAllWithAccount(ctx context.Context) ([]Calendar, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT c.id, c.calendar_account_id, a.name, a.provider, c.external_path, c.name, c.color, c.enabled, c.last_synced_at, c.last_sync_error, c.created_at
		FROM hhq_calendars c
		JOIN hhq_calendar_accounts a ON a.id = c.calendar_account_id
		ORDER BY a.name, c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalendars(rows)
}

func (s *CalendarStore) ListEnabledForAccount(ctx context.Context, accountID int) ([]Calendar, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT c.id, c.calendar_account_id, a.name, a.provider, c.external_path, c.name, c.color, c.enabled, c.last_synced_at, c.last_sync_error, c.created_at
		FROM hhq_calendars c
		JOIN hhq_calendar_accounts a ON a.id = c.calendar_account_id
		WHERE c.calendar_account_id = $1 AND c.enabled = TRUE
		ORDER BY c.name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalendars(rows)
}

func (s *CalendarStore) GetByID(ctx context.Context, id int) (*Calendar, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT c.id, c.calendar_account_id, a.name, a.provider, c.external_path, c.name, c.color, c.enabled, c.last_synced_at, c.last_sync_error, c.created_at
		FROM hhq_calendars c
		JOIN hhq_calendar_accounts a ON a.id = c.calendar_account_id
		WHERE c.id = $1`, id)
	cal, err := scanCalendar(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return cal, err
}

// NextAvailableColor picks the first palette color not currently in use by
// any calendar. Not wrapped in a transaction/lock - for this single-instance,
// low-write-frequency app (discovery only runs on account add/edit or a
// periodic sync tick), the tiny race window between checking and inserting
// is an acceptable trade-off against the complexity of proper allocation
// locking, and the worst case is just two calendars sharing a color.
func (s *CalendarStore) NextAvailableColor(ctx context.Context) (string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT color FROM hhq_calendars`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	used := map[string]bool{}
	total := 0
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", err
		}
		used[c] = true
		total++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	for _, c := range colorPalette {
		if !used[c] {
			return c, nil
		}
	}
	// Every palette color is taken - cycle. Total calendar count (not the
	// count of distinct colors in use, which caps at len(colorPalette) and
	// would pin this at index 0 forever) modulo palette length spreads
	// repeats out rather than piling them all onto the first color.
	return colorPalette[total%len(colorPalette)], nil
}

// UpsertDiscovered inserts a newly-discovered calendar (assigning it the
// given color) or, if a calendar with the same (account, external_path)
// already exists, just refreshes its display name - it deliberately leaves
// color and enabled untouched on conflict, since those are the parent's own
// choices and shouldn't be reset just because a routine re-discovery ran.
func (s *CalendarStore) UpsertDiscovered(ctx context.Context, accountID int, externalPath, name, color string) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_calendars (calendar_account_id, external_path, name, color)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (calendar_account_id, external_path) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`,
		accountID, externalPath, name, color).Scan(&id)
	return id, err
}

func (s *CalendarStore) SetEnabled(ctx context.Context, id int, enabled bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_calendars SET enabled = $2 WHERE id = $1`, id, enabled)
	return err
}

func (s *CalendarStore) SetColor(ctx context.Context, id int, color string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_calendars SET color = $2 WHERE id = $1`, id, color)
	return err
}

func (s *CalendarStore) MarkSynced(ctx context.Context, id int, syncErr error) error {
	var errText sql.NullString
	if syncErr != nil {
		errText = sql.NullString{String: syncErr.Error(), Valid: true}
	}
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_calendars SET last_synced_at = now(), last_sync_error = $2 WHERE id = $1`,
		id, errText)
	return err
}

func scanCalendars(rows *sql.Rows) ([]Calendar, error) {
	var out []Calendar
	for rows.Next() {
		var c Calendar
		if err := rows.Scan(&c.ID, &c.CalendarAccountID, &c.AccountName, &c.Provider, &c.ExternalPath, &c.Name, &c.Color, &c.Enabled, &c.LastSyncedAt, &c.LastSyncError, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanCalendar(row rowScanner) (*Calendar, error) {
	var c Calendar
	if err := row.Scan(&c.ID, &c.CalendarAccountID, &c.AccountName, &c.Provider, &c.ExternalPath, &c.Name, &c.Color, &c.Enabled, &c.LastSyncedAt, &c.LastSyncError, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// --- Events cache ---

// Attendee is one ATTENDEE entry on a VEVENT.
type Attendee struct {
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	Status string `json:"status,omitempty"` // PARTSTAT, e.g. "ACCEPTED", "DECLINED", "NEEDS-ACTION"
}

// Attachment is one ATTACH entry on a VEVENT.
type Attachment struct {
	Name string `json:"name,omitempty"` // FMTTYPE if present, else just shown as a generic link label
	URI  string `json:"uri,omitempty"`
}

// EventAction is one plugin-supplied action button (e.g. "Mark Paid") to
// show on the kiosk's event detail popup - see internal/plugins.EventAction
// for the wire shape this is upserted from, and internal/handlers/kiosk.go's
// KioskEventAction for how ID gets POSTed back to the owning plugin.
type EventAction struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	RequiresParent bool   `json:"requires_parent,omitempty"`
}

type Event struct {
	ID             int
	CalendarID     int
	CalendarName   string // joined, for display
	AccountName    string // joined, for display
	CalendarColor  string // joined, for display
	UID            string
	Summary        string
	Location       sql.NullString
	Description    sql.NullString
	OrganizerName  sql.NullString
	OrganizerEmail sql.NullString
	Attendees      []Attendee
	Attachments    []Attachment
	Actions        []EventAction
	StartsAt       time.Time
	EndsAt         time.Time
	AllDay         bool
}

type EventStore struct {
	DB *sql.DB
}

// Upsert inserts or updates a cached event keyed by (calendar_id, uid).
func (s *EventStore) Upsert(ctx context.Context, e Event) error {
	attendeesJSON, err := marshalAttendees(e.Attendees)
	if err != nil {
		return err
	}
	attachmentsJSON, err := marshalAttachments(e.Attachments)
	if err != nil {
		return err
	}
	actionsJSON, err := marshalActions(e.Actions)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO hhq_calendar_events_cache
			(calendar_id, uid, summary, location, description, organizer_name, organizer_email, attendees, attachments, actions, starts_at, ends_at, all_day, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now())
		ON CONFLICT (calendar_id, uid) DO UPDATE SET
			summary = EXCLUDED.summary,
			location = EXCLUDED.location,
			description = EXCLUDED.description,
			organizer_name = EXCLUDED.organizer_name,
			organizer_email = EXCLUDED.organizer_email,
			attendees = EXCLUDED.attendees,
			attachments = EXCLUDED.attachments,
			actions = EXCLUDED.actions,
			starts_at = EXCLUDED.starts_at,
			ends_at = EXCLUDED.ends_at,
			all_day = EXCLUDED.all_day,
			updated_at = now()`,
		e.CalendarID, e.UID, e.Summary, e.Location, e.Description, e.OrganizerName, e.OrganizerEmail,
		attendeesJSON, attachmentsJSON, actionsJSON, e.StartsAt, e.EndsAt, e.AllDay)
	return err
}

// marshalAttendees/marshalAttachments encode to JSON text for storage, or a
// NULL string for an empty list (nothing to show, no point storing "[]").
func marshalAttendees(list []Attendee) (sql.NullString, error) {
	if len(list) == 0 {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func marshalAttachments(list []Attachment) (sql.NullString, error) {
	if len(list) == 0 {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func marshalActions(list []EventAction) (sql.NullString, error) {
	if len(list) == 0 {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func unmarshalActions(s sql.NullString) ([]EventAction, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	var out []EventAction
	if err := json.Unmarshal([]byte(s.String), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func unmarshalAttendees(s sql.NullString) ([]Attendee, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	var out []Attendee
	if err := json.Unmarshal([]byte(s.String), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func unmarshalAttachments(s sql.NullString) ([]Attachment, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	var out []Attachment
	if err := json.Unmarshal([]byte(s.String), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PruneStale removes cached events for a calendar whose UID was not seen in
// the most recent sync (i.e. it was deleted at the source). Called after a
// sync with the set of UIDs that were just fetched.
func (s *EventStore) PruneStale(ctx context.Context, calendarID int, seenUIDs []string) error {
	_, err := s.DB.ExecContext(ctx, `
		DELETE FROM hhq_calendar_events_cache
		WHERE calendar_id = $1 AND uid <> ALL($2)`,
		calendarID, seenUIDs)
	return err
}

// ListInWindow returns all cached events starting today (even if already
// passed) through `days` days ahead, across all ENABLED calendars, for
// kiosk/dashboard display. Filtering on c.enabled here (not just at sync
// time) means disabling a calendar hides it immediately, without waiting
// for the next sync to prune its cached events.
func (s *EventStore) ListInWindow(ctx context.Context, days int) ([]Event, error) {
	// Boundaries are computed here in Go (using time.Local) rather than via
	// Postgres's now()/date_trunc - the app's pods have the correct local
	// TZ (set cluster-wide, see CLAUDE.md's timezone round), but the
	// Postgres pod's session timezone is not guaranteed to match, so letting
	// the database decide "today" could bucket events into the wrong local
	// day even though the stored TIMESTAMPTZ instants themselves are correct.
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to := from.AddDate(0, 0, days)

	rows, err := s.DB.QueryContext(ctx, `
		SELECT e.id, e.calendar_id, c.name, a.name, c.color, e.uid, e.summary, e.location, e.starts_at, e.ends_at, e.all_day
		FROM hhq_calendar_events_cache e
		JOIN hhq_calendars c ON c.id = e.calendar_id
		JOIN hhq_calendar_accounts a ON a.id = c.calendar_account_id
		WHERE c.enabled = TRUE
		  AND e.starts_at >= $1
		  AND e.starts_at < $2
		ORDER BY e.starts_at`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// ListToday returns today's events only (including ones already in the past
// today), for the "Today's Agenda" panel. Also filtered to enabled calendars.
func (s *EventStore) ListToday(ctx context.Context) ([]Event, error) {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to := from.AddDate(0, 0, 1)

	rows, err := s.DB.QueryContext(ctx, `
		SELECT e.id, e.calendar_id, c.name, a.name, c.color, e.uid, e.summary, e.location, e.starts_at, e.ends_at, e.all_day
		FROM hhq_calendar_events_cache e
		JOIN hhq_calendars c ON c.id = e.calendar_id
		JOIN hhq_calendar_accounts a ON a.id = c.calendar_account_id
		WHERE c.enabled = TRUE
		  AND e.starts_at >= $1 AND e.starts_at < $2
		ORDER BY e.starts_at`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// GetByID returns one event with its full detail fields (description,
// organizer, attendees, attachments) populated - used to back the kiosk's
// "tap an event for details" popup. The list queries above deliberately
// don't select these columns since they're not needed for the list view and
// are re-fetched on every 60s poll; this is only called on demand, per tap.
func (s *EventStore) GetByID(ctx context.Context, id int) (*Event, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT e.id, e.calendar_id, c.name, a.name, c.color, e.uid, e.summary, e.location,
		       e.description, e.organizer_name, e.organizer_email, e.attendees, e.attachments, e.actions,
		       e.starts_at, e.ends_at, e.all_day
		FROM hhq_calendar_events_cache e
		JOIN hhq_calendars c ON c.id = e.calendar_id
		JOIN hhq_calendar_accounts a ON a.id = c.calendar_account_id
		WHERE e.id = $1`, id)

	var e Event
	var attendeesJSON, attachmentsJSON, actionsJSON sql.NullString
	err := row.Scan(&e.ID, &e.CalendarID, &e.CalendarName, &e.AccountName, &e.CalendarColor, &e.UID, &e.Summary, &e.Location,
		&e.Description, &e.OrganizerName, &e.OrganizerEmail, &attendeesJSON, &attachmentsJSON, &actionsJSON,
		&e.StartsAt, &e.EndsAt, &e.AllDay)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if e.Attendees, err = unmarshalAttendees(attendeesJSON); err != nil {
		return nil, err
	}
	if e.Attachments, err = unmarshalAttachments(attachmentsJSON); err != nil {
		return nil, err
	}
	if e.Actions, err = unmarshalActions(actionsJSON); err != nil {
		return nil, err
	}
	e.StartsAt = e.StartsAt.In(time.Local)
	e.EndsAt = e.EndsAt.In(time.Local)
	return &e, nil
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.CalendarID, &e.CalendarName, &e.AccountName, &e.CalendarColor, &e.UID, &e.Summary, &e.Location, &e.StartsAt, &e.EndsAt, &e.AllDay); err != nil {
			return nil, err
		}
		// starts_at/ends_at come back from pgx tagged UTC regardless of the
		// stored instant's origin - convert to time.Local (the cluster-wide
		// TZ, see CLAUDE.md) so every downstream consumer (day-grouping,
		// dayLabel, and the kiosk templates' .Format calls) shows the
		// user's actual local wall-clock time, not UTC.
		e.StartsAt = e.StartsAt.In(time.Local)
		e.EndsAt = e.EndsAt.In(time.Local)
		out = append(out, e)
	}
	return out, rows.Err()
}
