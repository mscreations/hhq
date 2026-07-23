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

// Package release checks GitHub's Releases API for a newer published version
// of this app than the one currently running, so the parent dashboard can
// show an "Update Available" badge. Keyless (unauthenticated), matching this
// app's "no new secrets to manage" approach already used for weather/CalDAV.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Release is the parsed, app-shaped view of a GitHub release.
type Release struct {
	FetchedAt time.Time
	Version   string // e.g. "1.2.0" - the tag with any leading "v" stripped
	URL       string // HTML link to the release/its notes
}

// LatestReleaseURL is a var (not const) so tests can point it at a local
// httptest server instead of the real GitHub API, the same pattern
// internal/weather uses for ForecastURL.
var LatestReleaseURL = "https://api.github.com/repos/mscreations/hhq/releases/latest"

type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// FetchLatest calls GitHub's "latest release" endpoint, which only ever
// returns non-prerelease, non-draft releases - since this app's versioning
// workflow only cuts a GitHub Release on main-branch (non "-dev") tags, this
// naturally means "the latest promoted version".
func FetchLatest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, LatestReleaseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("release: requesting latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release: latest release request returned %s", resp.Status)
	}

	var parsed githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("release: decoding latest release response: %w", err)
	}

	return &Release{
		FetchedAt: time.Now(),
		Version:   strings.TrimPrefix(parsed.TagName, "v"),
		URL:       parsed.HTMLURL,
	}, nil
}

// IsNewer reports whether latest is a newer version than current, per this
// app's version scheme (MAJOR.MINOR.PATCH, optionally suffixed "-dev" for
// dev-branch builds - a "-dev" build is always considered older than the
// release it was built against, e.g. "1.2.3-dev" < "1.2.3"). Malformed
// version strings are treated as "not newer" rather than erroring, since this
// only ever drives a cosmetic dashboard badge.
func IsNewer(current, latest string) bool {
	c, ok := parseVersion(current)
	if !ok {
		return false
	}
	l, ok := parseVersion(latest)
	if !ok {
		return false
	}
	return compareVersions(l, c) > 0
}

type semver struct {
	major, minor, patch int
	dev                 bool
}

func parseVersion(v string) (semver, bool) {
	var s semver
	if strings.HasSuffix(v, "-dev") {
		s.dev = true
		v = strings.TrimSuffix(v, "-dev")
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return semver{}, false
		}
		nums[i] = n
	}
	s.major, s.minor, s.patch = nums[0], nums[1], nums[2]
	return s, true
}

// compareVersions returns -1/0/1 as a < b / a == b / a > b, treating a "-dev"
// build as older than the same non-dev version (e.g. 1.2.3-dev < 1.2.3).
func compareVersions(a, b semver) int {
	if a.major != b.major {
		return cmpInt(a.major, b.major)
	}
	if a.minor != b.minor {
		return cmpInt(a.minor, b.minor)
	}
	if a.patch != b.patch {
		return cmpInt(a.patch, b.patch)
	}
	if a.dev != b.dev {
		if a.dev {
			return -1
		}
		return 1
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
