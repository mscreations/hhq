-- +goose Up
-- Optional GitHub repo URL for a plugin (set via CONFIG_DIR/plugins.json's
-- "repo_url" field), used to check for a newer published version of the
-- plugin itself - see internal/release.CheckForUpdate and
-- internal/scheduler's checkPluginUpdates. NULL means "don't check" (a
-- plugin with no configured repo_url just never shows an update icon).
ALTER TABLE hhq_plugins ADD COLUMN repo_url TEXT;

-- +goose Down
ALTER TABLE hhq_plugins DROP COLUMN repo_url;
