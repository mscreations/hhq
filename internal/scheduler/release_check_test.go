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

package scheduler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/release"
)

// TestCheckReleaseUsesReleasesEndpointForPromotedBuild is a regression test
// for the original (pre-dev-aware) behavior: a running version with no
// "-dev" suffix must still poll /releases/latest, not the tags API.
func TestCheckReleaseUsesReleasesEndpointForPromotedBuild(t *testing.T) {
	releasesHit, tagsHit := false, false

	releasesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releasesHit = true
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.2.0", "html_url": "https://example.com/v1.2.0"})
	}))
	defer releasesSrv.Close()
	tagsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tagsHit = true
		_ = json.NewEncoder(w).Encode([]map[string]string{})
	}))
	defer tagsSrv.Close()

	origReleases, origTags := release.LatestReleaseURL, release.LatestTagsURL
	release.LatestReleaseURL, release.LatestTagsURL = releasesSrv.URL, tagsSrv.URL
	defer func() { release.LatestReleaseURL, release.LatestTagsURL = origReleases, origTags }()

	s := &Scheduler{
		Cfg:     &config.Config{ReleaseCheckInterval: time.Hour},
		Release: &release.Cache{},
		Version: "1.1.0",
	}
	s.checkRelease(t.Context())

	if !releasesHit {
		t.Error("expected /releases/latest to be hit for a promoted-build version")
	}
	if tagsHit {
		t.Error("did not expect the tags API to be hit for a promoted-build version")
	}
	got, ok := s.Release.Get()
	if !ok || got.Version != "1.2.0" {
		t.Errorf("Release cache = %+v, ok=%v", got, ok)
	}
}

// TestCheckReleaseUsesTagsEndpointForDevBuild is the actual feature this
// covers: a running "-dev" version has no GitHub Release to compare against
// (only main-branch promotions get one - see version-main.yml), so it must
// poll the tags API instead of /releases/latest.
func TestCheckReleaseUsesTagsEndpointForDevBuild(t *testing.T) {
	releasesHit, tagsHit := false, false

	releasesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releasesHit = true
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.2.0", "html_url": "https://example.com/v1.2.0"})
	}))
	defer releasesSrv.Close()
	tagsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tagsHit = true
		if r.URL.Query().Get("page") != "1" {
			_ = json.NewEncoder(w).Encode([]map[string]string{})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "v1.2.4-dev"}})
	}))
	defer tagsSrv.Close()

	origReleases, origTags := release.LatestReleaseURL, release.LatestTagsURL
	release.LatestReleaseURL, release.LatestTagsURL = releasesSrv.URL, tagsSrv.URL
	defer func() { release.LatestReleaseURL, release.LatestTagsURL = origReleases, origTags }()

	s := &Scheduler{
		Cfg:     &config.Config{ReleaseCheckInterval: time.Hour},
		Release: &release.Cache{},
		Version: "1.2.3-dev",
	}
	s.checkRelease(t.Context())

	if releasesHit {
		t.Error("did not expect /releases/latest to be hit for a dev-build version")
	}
	if !tagsHit {
		t.Error("expected the tags API to be hit for a dev-build version")
	}
	got, ok := s.Release.Get()
	if !ok || got.Version != "1.2.4-dev" {
		t.Errorf("Release cache = %+v, ok=%v", got, ok)
	}
}
