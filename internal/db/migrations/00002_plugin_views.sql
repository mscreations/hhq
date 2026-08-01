-- +goose Up
-- A plugin can now register more than one kiosk nav button/view (previously
-- exactly one, cached as flat scalar columns on hhq_plugins). Each view gets
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

ALTER TABLE hhq_plugins DROP COLUMN view_enabled;
ALTER TABLE hhq_plugins DROP COLUMN view_label;
ALTER TABLE hhq_plugins DROP COLUMN view_icon;

-- +goose Down
ALTER TABLE hhq_plugins ADD COLUMN view_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE hhq_plugins ADD COLUMN view_label TEXT;
ALTER TABLE hhq_plugins ADD COLUMN view_icon TEXT;

DROP TABLE hhq_plugin_views;
