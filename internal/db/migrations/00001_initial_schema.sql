-- +goose Up
CREATE TYPE user_role AS ENUM ('parent', 'child');
CREATE TYPE auth_provider AS ENUM ('local', 'authentik');

CREATE TABLE hhq_users (
    id              SERIAL PRIMARY KEY,
    name            TEXT NOT NULL,
    role            user_role NOT NULL,
    -- Children are distinguished on the kiosk screen by this color (hex, e.g. #4F86C6).
    color           TEXT NOT NULL DEFAULT '#888888',
    -- Parents only:
    email           TEXT UNIQUE,
    password_hash   TEXT,
    -- Parents only: optional kiosk display name (e.g. "Mom"/"Dad") shown on
    -- the chore tracker instead of the real name. NULL falls back to name.
    display_name    TEXT,
    auth_provider   auth_provider NOT NULL DEFAULT 'local',
    -- Reserved for future Authentik/OIDC support: the "sub" claim returned by the IdP.
    external_subject TEXT,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set when a parent invite email is sent; NULL for accounts created directly
    -- (bootstrap parent) or for children. Used to track pending invite acceptance.
    invited_at      TIMESTAMPTZ,
    -- True if this (child) row is created/kept in sync by the children.json
    -- bootstrap config file (see internal/handlers/bootstrap_config.go) rather
    -- than through the parent dashboard. Such children are read-only in the
    -- UI (name/color can't be edited, can't be removed), since the config
    -- file is the source of truth for them and would otherwise silently
    -- overwrite a manual edit on the next restart.
    bootstrap_managed BOOLEAN NOT NULL DEFAULT FALSE,
    -- Optional uploaded avatar photo, shown on the kiosk chore tracker and
    -- the parent dashboard instead of (alongside) the color swatch. Stored
    -- directly in Postgres (BYTEA) rather than a filesystem/volume - this app
    -- has no shared/persistent volume infrastructure, only CNPG Postgres as
    -- durable shared storage (same rationale as encrypted CalDAV credentials
    -- living in hhq_calendar_accounts). All four avatar_* columns are set or
    -- NULL together (see avatar_fields_consistent below). avatar_checksum
    -- (sha256 hex of avatar_image) lets the children.json bootstrap file's
    -- avatar_file reconciliation (internal/handlers/bootstrap_config.go)
    -- detect an unchanged file cheaply, without re-fetching/comparing the
    -- full image or needlessly bumping avatar_updated_at (which cache-busts
    -- the served image's URL) on every restart.
    avatar_image        BYTEA,
    avatar_content_type TEXT,
    avatar_checksum     TEXT,
    avatar_updated_at   TIMESTAMPTZ,

    CONSTRAINT parent_has_email CHECK (role <> 'parent' OR email IS NOT NULL),
    CONSTRAINT avatar_fields_consistent CHECK (
        (avatar_image IS NULL AND avatar_content_type IS NULL AND avatar_checksum IS NULL AND avatar_updated_at IS NULL)
        OR (avatar_image IS NOT NULL AND avatar_content_type IS NOT NULL AND avatar_checksum IS NOT NULL AND avatar_updated_at IS NOT NULL)
    )
);

CREATE TABLE hhq_sessions (
    token       TEXT PRIMARY KEY, -- random opaque token; stored as the session cookie value
    user_id     INTEGER NOT NULL REFERENCES hhq_users(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_sessions_user_id ON hhq_sessions(user_id);
CREATE INDEX idx_sessions_expires_at ON hhq_sessions(expires_at);

CREATE TYPE calendar_provider AS ENUM ('caldav_fastmail', 'caldav_icloud', 'caldav_generic', 'google', 'plugin');

CREATE TABLE hhq_calendar_accounts (
    id                  SERIAL PRIMARY KEY,
    name                TEXT NOT NULL, -- display label, e.g. "Mom's Fastmail"
    provider            calendar_provider NOT NULL,
    caldav_url          TEXT,          -- CalDAV principal/calendar URL (not used for provider='google')
    username            TEXT,
    -- Encrypted with AES-GCM using ENCRYPTION_KEY (see internal/util/crypto.go).
    -- Never stored or logged in plaintext.
    encrypted_password  BYTEA,
    -- OAuth2 refresh token for provider='google', encrypted the same way as
    -- encrypted_password above. NULL for CalDAV/plugin providers.
    encrypted_refresh_token BYTEA,
    -- The connected Google account's email, for a "Connected as X" display on
    -- the parent dashboard. Not a secret; not used for non-google providers.
    google_email        TEXT,
    last_synced_at      TIMESTAMPTZ,
    last_sync_error     TEXT,
    -- True if this account was created/is kept in sync by the CONFIG_DIR/
    -- calendars.json bootstrap file (see internal/handlers/bootstrap_config.go)
    -- rather than through the parent dashboard. Such accounts are read-only in
    -- the UI, since the config file is the source of truth for them and would
    -- otherwise silently overwrite any manual edit on the next restart.
    bootstrap_managed   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Individual calendars discovered under a calendar_account (one Fastmail or
-- iCloud login can have several calendars - "Home", "Work", "Kids", etc.).
-- Color and enabled/disabled live here rather than on calendar_accounts,
-- since each calendar needs its own distinguishing color on the kiosk and
-- its own independent on/off toggle.
CREATE TABLE hhq_calendars (
    id                  SERIAL PRIMARY KEY,
    calendar_account_id INTEGER NOT NULL REFERENCES hhq_calendar_accounts(id) ON DELETE CASCADE,
    -- The CalDAV path used to query this specific calendar (e.g.
    -- "/dav/calendars/user/foo@example.com/Default/"), as reported by the
    -- server during discovery (FindCalendars).
    external_path       TEXT NOT NULL,
    -- Display name as reported by the server (e.g. "Home", "Work").
    name                TEXT NOT NULL,
    color               TEXT NOT NULL DEFAULT '#3B82F6',
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    last_synced_at      TIMESTAMPTZ,
    last_sync_error     TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (calendar_account_id, external_path)
);

CREATE TABLE hhq_calendar_events_cache (
    id              SERIAL PRIMARY KEY,
    calendar_id     INTEGER NOT NULL REFERENCES hhq_calendars(id) ON DELETE CASCADE,
    uid             TEXT NOT NULL, -- iCalendar UID
    summary         TEXT NOT NULL,
    location        TEXT,
    description     TEXT,
    organizer_name  TEXT,
    organizer_email TEXT,
    -- attendees/attachments are stored as JSON text rather than JSONB - there's
    -- no need to query into their structure from SQL, so plain TEXT sidesteps
    -- any pgx/jsonb encoding gotchas for a column that's only ever read back
    -- whole and unmarshaled in Go (see the event detail popup, internal/models/calendar.go).
    attendees       TEXT,
    attachments     TEXT,
    -- actions holds plugin-supplied kiosk detail-popup buttons (JSON array of
    -- {id,label}, see internal/models.EventAction) - only ever populated for
    -- plugin-synthetic events (internal/plugins.SyncOne), NULL for real
    -- CalDAV events.
    actions         TEXT,
    starts_at       TIMESTAMPTZ NOT NULL,
    ends_at         TIMESTAMPTZ NOT NULL,
    all_day         BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (calendar_id, uid)
);

CREATE INDEX idx_events_starts_at ON hhq_calendar_events_cache(starts_at);
CREATE INDEX idx_events_calendar ON hhq_calendar_events_cache(calendar_id);

-- A plugin is an externally-run HTTP service that extends hhq with a kiosk
-- nav button/full-screen view and/or synthetic calendar events, without its
-- logic living in this codebase (see internal/plugins). Registered via the
-- CONFIG_DIR/plugins.json
-- bootstrap file, mirroring calendars.json/children.json/etc (see
-- internal/handlers/plugin_bootstrap.go).
CREATE TABLE hhq_plugins (
    id                  TEXT PRIMARY KEY, -- stable slug from the plugin's manifest; also the URL path segment
    name                TEXT NOT NULL,
    base_url            TEXT NOT NULL,
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    provides_events     BOOLEAN NOT NULL DEFAULT FALSE,
    -- The dedicated synthetic calendar this plugin's events are upserted
    -- into (auto-created the first time a provides_events plugin registers).
    -- Never shared with a real CalDAV-synced calendar - see
    -- internal/scheduler/plugin_sync.go for why that isolation matters.
    calendar_id         INTEGER REFERENCES hhq_calendars(id) ON DELETE SET NULL,
    -- Shared secret sent as "Authorization: Bearer <token>" on every
    -- outbound call to this plugin (internal/plugins' client.go/proxy.go),
    -- and required by the plugin itself to reject any request not from hhq.
    -- NULL until the plugin self-registers (see internal/plugins/
    -- register.go and internal/handlers/plugin_bootstrap.go's
    -- tryRegisterAndRefresh): hhq POSTs to the plugin's own unauthenticated
    -- POST /register the first time it sees a plugin with no stored token,
    -- the plugin generates and returns one, and it's never re-issued after
    -- that. Encrypted at rest with the same AES-256-GCM Encryptor used for
    -- CalDAV account passwords.
    encrypted_token     BYTEA,
    -- True if this row is created/kept in sync by the plugins.json bootstrap
    -- file rather than through the parent dashboard - always true today,
    -- since there's no manual "add a plugin" UI yet, but mirrors the same
    -- BootstrapManaged convention used elsewhere in this schema.
    bootstrap_managed   BOOLEAN NOT NULL DEFAULT FALSE,
    -- Cached from the plugin's last successful GET /manifest fetch - the
    -- plugin's own build version, shown on the parent dashboard's Plugins
    -- card so a parent can see what's actually running without shelling
    -- into the container.
    version             TEXT,
    last_healthy_at     TIMESTAMPTZ,
    last_error          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A plugin can register more than one kiosk nav button/view. Each view gets
-- its own row here, keyed by the plugin's own stable view id (a slug, used
-- as a URL path segment the same way the plugin id itself is - see
-- internal/handlers/plugins.go). The whole set for a plugin is replaced on
-- every manifest refresh (see models.PluginStore.ReplaceViews), so there's
-- no separate "enabled" column to keep in sync - a view not currently
-- enabled in the plugin's manifest simply isn't in this table.
CREATE TABLE hhq_plugin_views (
    plugin_id  TEXT NOT NULL REFERENCES hhq_plugins(id) ON DELETE CASCADE,
    view_id    TEXT NOT NULL,
    label      TEXT NOT NULL,
    icon       TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (plugin_id, view_id)
);

CREATE TYPE chore_status AS ENUM ('incomplete', 'pending_approval', 'approved', 'rejected');

-- A chore is the shared catalog entry a parent defines once - name plus the
-- description of what's expected for completion - independent of which
-- child(ren) it's assigned to. Editing a chore's description here changes it
-- everywhere it's assigned, since the expectations shouldn't drift between
-- children doing "the same" chore.
CREATE TABLE hhq_chores (
    id              SERIAL PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    description     TEXT,
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- True if this catalog entry is created/kept in sync by the chores.json
    -- bootstrap config file (see internal/handlers/bootstrap_config.go)
    -- rather than through the parent dashboard. Read-only in the UI for the
    -- same reason bootstrap-managed calendar accounts are.
    bootstrap_managed BOOLEAN NOT NULL DEFAULT FALSE
);

-- A chore_definition is the assignment of a catalog chore to a specific
-- child on a recurring or one-off schedule, e.g. "Take out trash" every
-- Tue/Fri worth 5 points, assigned to a specific child. Points live here
-- (not on chores) so the same chore can be worth different amounts for
-- different children.
CREATE TABLE hhq_chore_definitions (
    id              SERIAL PRIMARY KEY,
    child_id        INTEGER NOT NULL REFERENCES hhq_users(id) ON DELETE CASCADE,
    chore_id        INTEGER NOT NULL REFERENCES hhq_chores(id) ON DELETE CASCADE,
    points          INTEGER NOT NULL DEFAULT 1,
    -- Bitmask of days this chore recurs on: bit 0 = Sunday ... bit 6 = Saturday
    -- (matches Go's time.Weekday numbering). NULL for one-off chores.
    days_of_week    INTEGER,
    -- One-off chores have a single specific due date instead of a recurrence rule.
    one_off_date    DATE,
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- True if this assignment is created/kept in sync by the
    -- assignments.json bootstrap config file (see
    -- internal/handlers/bootstrap_config.go) rather than through the parent
    -- dashboard. Read-only in the UI for the same reason bootstrap-managed
    -- calendar accounts are.
    bootstrap_managed BOOLEAN NOT NULL DEFAULT FALSE,

    CONSTRAINT recurring_xor_one_off CHECK (
        (days_of_week IS NOT NULL AND one_off_date IS NULL) OR
        (days_of_week IS NULL AND one_off_date IS NOT NULL)
    )
);

-- A chore_instance is a single day's occurrence of a chore_definition. The scheduler
-- (internal/scheduler) generates these each day for active recurring definitions, and
-- immediately for one-off definitions. This is what the kiosk and parent UI actually
-- read/write — chore_definitions never change state, only instances do.
CREATE TABLE hhq_chore_instances (
    id                  SERIAL PRIMARY KEY,
    chore_definition_id INTEGER NOT NULL REFERENCES hhq_chore_definitions(id) ON DELETE CASCADE,
    child_id            INTEGER NOT NULL REFERENCES hhq_users(id) ON DELETE CASCADE,
    due_date            DATE NOT NULL,
    status              chore_status NOT NULL DEFAULT 'incomplete',
    -- When the child marked it complete (may be on time or late relative to due_date).
    completed_at        TIMESTAMPTZ,
    -- When a parent approved or rejected it.
    decided_at          TIMESTAMPTZ,
    decided_by          INTEGER REFERENCES hhq_users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (chore_definition_id, due_date)
);

CREATE INDEX idx_chore_instances_due_date ON hhq_chore_instances(due_date);
CREATE INDEX idx_chore_instances_child ON hhq_chore_instances(child_id);
CREATE INDEX idx_chore_instances_status ON hhq_chore_instances(status);

CREATE TABLE hhq_settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL
);

-- app_title is intentionally not seeded here: Settings.Get falls back to
-- Config.AppTitle (the APP_TITLE env var, default "HappyHome Quest") until a
-- parent explicitly sets one via the dashboard, so the env var controls the
-- initial value instead of a literal baked into the migration.
INSERT INTO hhq_settings (key, value) VALUES
    ('timezone', 'America/New_York');

-- +goose Down
DROP TABLE hhq_settings;
DROP TABLE hhq_chore_instances;
DROP TABLE hhq_chore_definitions;
DROP TABLE hhq_chores;
DROP TYPE chore_status;
DROP TABLE hhq_plugin_views;
DROP TABLE hhq_plugins;
DROP TABLE hhq_calendar_events_cache;
DROP TABLE hhq_calendars;
DROP TABLE hhq_calendar_accounts;
DROP TYPE calendar_provider;
DROP TABLE hhq_sessions;
DROP TABLE hhq_users;
DROP TYPE auth_provider;
DROP TYPE user_role;
