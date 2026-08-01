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

package release

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLatestParsesTagAndURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRelease{
			TagName: "v1.3.0",
			HTMLURL: "https://github.com/mscreations/hhq/releases/tag/v1.3.0",
		})
	}))
	defer srv.Close()

	origURL := LatestReleaseURL
	LatestReleaseURL = srv.URL
	defer func() { LatestReleaseURL = origURL }()

	rel, err := FetchLatest(t.Context())
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if rel.Version != "1.3.0" {
		t.Errorf("Version = %q, want %q", rel.Version, "1.3.0")
	}
	if rel.URL != "https://github.com/mscreations/hhq/releases/tag/v1.3.0" {
		t.Errorf("URL = %q", rel.URL)
	}
}

func TestFetchLatestTagPicksHighestVersionAcrossPages(t *testing.T) {
	// Page 1 is full (perPage tags) so FetchLatestTag must fetch page 2 to
	// find the actual highest tag - this is the regression case for a
	// pagination bug that only returns whatever happens to be on page 1.
	page1 := make([]githubTag, 100)
	for i := range page1 {
		page1[i] = githubTag{Name: fmt.Sprintf("v0.%d.0", i)}
	}
	page2 := []githubTag{
		{Name: "v1.4.2-dev"},
		{Name: "v1.4.1-dev"},
		{Name: "v1.3.0"}, // a promoted release tag mixed in with dev tags
		{Name: "not-a-version"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1":
			_ = json.NewEncoder(w).Encode(page1)
		case "2":
			_ = json.NewEncoder(w).Encode(page2)
		default:
			_ = json.NewEncoder(w).Encode([]githubTag{})
		}
	}))
	defer srv.Close()

	origURL, origWeb := LatestTagsURL, RepoWebURL
	LatestTagsURL = srv.URL
	RepoWebURL = "https://github.com/mscreations/hhq"
	defer func() { LatestTagsURL, RepoWebURL = origURL, origWeb }()

	rel, err := FetchLatestTag(t.Context())
	if err != nil {
		t.Fatalf("FetchLatestTag: %v", err)
	}
	if rel.Version != "1.4.2-dev" {
		t.Errorf("Version = %q, want %q", rel.Version, "1.4.2-dev")
	}
	if rel.URL != "https://github.com/mscreations/hhq/tree/v1.4.2-dev" {
		t.Errorf("URL = %q", rel.URL)
	}
}

func TestFetchLatestTagLinksPromotedReleaseTagToReleasesPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			_ = json.NewEncoder(w).Encode([]githubTag{})
			return
		}
		_ = json.NewEncoder(w).Encode([]githubTag{{Name: "v1.2.0"}})
	}))
	defer srv.Close()

	origURL, origWeb := LatestTagsURL, RepoWebURL
	LatestTagsURL = srv.URL
	RepoWebURL = "https://github.com/mscreations/hhq"
	defer func() { LatestTagsURL, RepoWebURL = origURL, origWeb }()

	rel, err := FetchLatestTag(t.Context())
	if err != nil {
		t.Fatalf("FetchLatestTag: %v", err)
	}
	if rel.URL != "https://github.com/mscreations/hhq/releases/tag/v1.2.0" {
		t.Errorf("URL = %q", rel.URL)
	}
}

func TestFetchLatestTagErrorsWhenNoVersionShapedTagsExist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]githubTag{{Name: "not-a-version"}})
	}))
	defer srv.Close()

	origURL := LatestTagsURL
	LatestTagsURL = srv.URL
	defer func() { LatestTagsURL = origURL }()

	if _, err := FetchLatestTag(t.Context()); err == nil {
		t.Fatal("expected an error when no tags parse as a version, got nil")
	}
}

func TestRepoAPIURLs(t *testing.T) {
	cases := []struct {
		repoWebURL      string
		wantRelease     string
		wantTags        string
	}{
		{
			"https://github.com/mscreations/billtracker-plugin",
			"https://api.github.com/repos/mscreations/billtracker-plugin/releases/latest",
			"https://api.github.com/repos/mscreations/billtracker-plugin/tags",
		},
		{
			// trailing slash should be trimmed rather than producing a
			// doubled slash in the derived API URL.
			"https://github.com/mscreations/billtracker-plugin/",
			"https://api.github.com/repos/mscreations/billtracker-plugin/releases/latest",
			"https://api.github.com/repos/mscreations/billtracker-plugin/tags",
		},
	}
	for _, c := range cases {
		releaseURL, tagsURL := RepoAPIURLs(c.repoWebURL)
		if releaseURL != c.wantRelease {
			t.Errorf("RepoAPIURLs(%q) release = %q, want %q", c.repoWebURL, releaseURL, c.wantRelease)
		}
		if tagsURL != c.wantTags {
			t.Errorf("RepoAPIURLs(%q) tags = %q, want %q", c.repoWebURL, tagsURL, c.wantTags)
		}
	}
}

func TestFetchLatestFromRepoParsesTagAndURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRelease{
			TagName: "v2.1.0",
			HTMLURL: "https://github.com/mscreations/billtracker-plugin/releases/tag/v2.1.0",
		})
	}))
	defer srv.Close()

	rel, err := FetchLatestFromRepo(t.Context(), srv.URL)
	if err != nil {
		t.Fatalf("FetchLatestFromRepo: %v", err)
	}
	if rel.Version != "2.1.0" {
		t.Errorf("Version = %q, want %q", rel.Version, "2.1.0")
	}
}

func TestFetchLatestTagFromRepoParsesHighestDevTag(t *testing.T) {
	var sawTagsRequest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTagsRequest = true
		_ = json.NewEncoder(w).Encode([]githubTag{{Name: "v1.4.0-dev"}})
	}))
	defer srv.Close()

	rel, err := FetchLatestTagFromRepo(t.Context(), srv.URL, "https://github.com/mscreations/billtracker-plugin")
	if err != nil {
		t.Fatalf("FetchLatestTagFromRepo: %v", err)
	}
	if !sawTagsRequest {
		t.Fatal("expected a request to the tags endpoint")
	}
	if rel.Version != "1.4.0-dev" {
		t.Errorf("Version = %q, want %q", rel.Version, "1.4.0-dev")
	}
	if rel.URL != "https://github.com/mscreations/billtracker-plugin/tree/v1.4.0-dev" {
		t.Errorf("URL = %q", rel.URL)
	}
}

func TestCheckForUpdateSelectsReleasesEndpointForNonDevVersion(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v2.1.0", HTMLURL: "https://example.com/v2.1.0"})
	}))
	defer srv.Close()

	// RepoAPIURLs only rewrites a "https://github.com/" prefix - a plain
	// httptest URL is left untouched aside from the appended API suffix, so
	// this exercises CheckForUpdate's real, non-mocked URL derivation
	// without needing to hit the real github.com.
	rel, err := CheckForUpdate(t.Context(), srv.URL, "2.0.0")
	if err != nil {
		t.Fatalf("CheckForUpdate: %v", err)
	}
	if gotPath != "/releases/latest" {
		t.Errorf("request path = %q, want /releases/latest", gotPath)
	}
	if rel.Version != "2.1.0" {
		t.Errorf("Version = %q, want %q", rel.Version, "2.1.0")
	}
}

func TestCheckForUpdateSelectsTagsEndpointForDevVersion(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode([]githubTag{{Name: "v2.1.0-dev"}})
	}))
	defer srv.Close()

	rel, err := CheckForUpdate(t.Context(), srv.URL, "2.0.0-dev")
	if err != nil {
		t.Fatalf("CheckForUpdate: %v", err)
	}
	if gotPath != "/tags" {
		t.Errorf("request path = %q, want /tags", gotPath)
	}
	if rel.Version != "2.1.0-dev" {
		t.Errorf("Version = %q, want %q", rel.Version, "2.1.0-dev")
	}
}

func TestPluginCacheGetSetIsolatedByID(t *testing.T) {
	c := &PluginCache{}
	if _, ok := c.Get("billtracker"); ok {
		t.Fatal("expected nothing cached yet")
	}

	c.Set("billtracker", &Release{Version: "2.0.0"})
	c.Set("weather-plugin", &Release{Version: "1.0.0"})

	got, ok := c.Get("billtracker")
	if !ok || got.Version != "2.0.0" {
		t.Fatalf("Get(billtracker) = %+v, %v", got, ok)
	}
	got, ok = c.Get("weather-plugin")
	if !ok || got.Version != "1.0.0" {
		t.Fatalf("Get(weather-plugin) = %+v, %v", got, ok)
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want             bool
	}{
		{"1.2.0", "1.3.0", true},
		{"1.3.0", "1.2.0", false},
		{"1.2.0", "1.2.0", false},
		{"1.2.3-dev", "1.2.3", true},
		{"1.2.3", "1.2.3-dev", false},
		{"1.2.3-dev", "1.2.4-dev", true},
		{"bogus", "1.2.0", false},
		{"1.2.0", "bogus", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}
