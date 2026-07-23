#!/usr/bin/env bash
# Checks/updates/diffs the vendored web/static/js/htmx.min.js against the
# latest htmx.org release on GitHub. Deliberately refuses to *adopt* a
# release until it has aged past HTMX_MIN_AGE_DAYS (default 90) - a
# compromised release has time to be caught and yanked upstream before we'd
# ever vendor it. `diff` mode ignores that gate since it doesn't change
# anything - it's just a preview of what's coming.
#
# Usage: scripts/update-htmx.sh [check|update|diff]
#   check  (default) - report current vs. latest, don't change anything
#   update           - actually vendor the new file + version if eligible
#   diff             - prettify current vs. latest and show a unified diff
set -euo pipefail

MIN_AGE_DAYS="${HTMX_MIN_AGE_DAYS:-90}"
VERSION_FILE="web/static/js/htmx.version"
JS_FILE="web/static/js/htmx.min.js"
MODE="${1:-check}"

for bin in curl jq; do
    command -v "$bin" >/dev/null 2>&1 || { echo "error: $bin is required" >&2; exit 1; }
done

PYTHON=""
for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1 && "$candidate" --version >/dev/null 2>&1; then
        PYTHON="$candidate"
        break
    fi
done

current_version="$(cat "$VERSION_FILE" 2>/dev/null || echo "unknown")"

# htmx does not set GitHub's `prerelease` flag on its own alpha/beta/rc tags
# (e.g. v4.0.0-beta5 comes back as prerelease=false), so /releases/latest
# can't be trusted to skip them. Instead, walk the release list ourselves
# and take the newest tag that looks like a plain "vX.Y.Z" release.
release_json="$(curl -fsSL "https://api.github.com/repos/bigskysoftware/htmx/releases?per_page=30")"
release="$(jq -r '[.[] | select(.tag_name | test("^v[0-9]+\\.[0-9]+\\.[0-9]+$"))][0]' <<<"$release_json")"

if [ "$release" = "null" ] || [ -z "$release" ]; then
    echo "error: no plain release tag (vX.Y.Z) found in the last 30 releases" >&2
    exit 1
fi

latest_version="$(jq -r '.tag_name' <<<"$release" | sed 's/^v//')"
published_at="$(jq -r '.published_at' <<<"$release")"

published_epoch="$(date -d "$published_at" +%s)"
now_epoch="$(date +%s)"
age_days=$(( (now_epoch - published_epoch) / 86400 ))

echo "Current vendored version: $current_version"
echo "Latest upstream release:  $latest_version (published $age_days days ago)"

if [ "$latest_version" = "$current_version" ]; then
    echo "Already up to date."
    exit 0
fi

if [ "$MODE" = "diff" ]; then
    [ -n "$PYTHON" ] || { echo "error: python (or python3) is required for diff mode" >&2; exit 1; }

    if [ "$age_days" -lt "$MIN_AGE_DAYS" ]; then
        echo "(note: still within the ${MIN_AGE_DAYS}-day quarantine window - preview only)"
    fi

    url="https://unpkg.com/htmx.org@${latest_version}/dist/htmx.min.js"
    echo "Downloading $url ..."
    new_min="$(mktemp)"
    curl -fsSL "$url" -o "$new_min"

    workdir="$(mktemp -d)"
    trap 'rm -rf "$workdir" "$new_min"' EXIT

    "$PYTHON" scripts/prettify_js.py "$JS_FILE" > "$workdir/current-$current_version.js"
    "$PYTHON" scripts/prettify_js.py "$new_min" > "$workdir/latest-$latest_version.js"

    diff -u "$workdir/current-$current_version.js" "$workdir/latest-$latest_version.js" || true
    exit 0
fi

if [ "$age_days" -lt "$MIN_AGE_DAYS" ]; then
    echo "Latest release is only $age_days day(s) old (< ${MIN_AGE_DAYS}-day quarantine window) - not upgrading yet."
    exit 0
fi

if [ "$MODE" != "update" ]; then
    echo "A newer, aged release is available: $latest_version. Run 'make htmx-update' to vendor it (or 'make htmx-diff' to preview it)."
    exit 0
fi

url="https://unpkg.com/htmx.org@${latest_version}/dist/htmx.min.js"
echo "Downloading $url ..."
tmpfile="$(mktemp)"
curl -fsSL "$url" -o "$tmpfile"

if [ ! -s "$tmpfile" ]; then
    echo "error: download failed or produced an empty file" >&2
    rm -f "$tmpfile"
    exit 1
fi

mv "$tmpfile" "$JS_FILE"
echo "$latest_version" > "$VERSION_FILE"
echo "Updated htmx.min.js to $latest_version - review the diff before committing."
