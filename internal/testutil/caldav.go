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

package testutil

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

// WritePrincipalResponse, WriteHomeSetResponse, and WriteCalendarListResponse
// are minimal, hand-rolled CalDAV PROPFIND response fixtures (per RFC 4791
// shapes), shared between internal/caldav and internal/scheduler's tests so
// the two packages' fake CalDAV servers can't silently drift out of sync
// with each other (see CLAUDE.md rounds 4-6 for the class of real production
// bug that came from exactly this kind of wire-format mismatch).

func WritePrincipalResponse(w http.ResponseWriter, principal string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(207)
	fmt.Fprintf(w, `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/</D:href>
    <D:propstat>
      <D:prop><D:current-user-principal><D:href>%s</D:href></D:current-user-principal></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`, xmlEscape(principal))
}

func WriteHomeSetResponse(w http.ResponseWriter, principal, homeSet string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(207)
	fmt.Fprintf(w, `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:response>
    <D:href>%s</D:href>
    <D:propstat>
      <D:prop><C:calendar-home-set><D:href>%s</D:href></C:calendar-home-set></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`, xmlEscape(principal), xmlEscape(homeSet))
}

func WriteCalendarListResponse(w http.ResponseWriter, homeSet string, calendars map[string]string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(207)
	fmt.Fprint(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`)
	fmt.Fprintf(w, `<D:response><D:href>%s</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, xmlEscape(homeSet))
	for path, name := range calendars {
		fmt.Fprintf(w, `<D:response>
  <D:href>%s</D:href>
  <D:propstat>
    <D:prop>
      <D:resourcetype><D:collection/><C:calendar/></D:resourcetype>
      <D:displayname>%s</D:displayname>
    </D:prop>
    <D:status>HTTP/1.1 200 OK</D:status>
  </D:propstat>
</D:response>`, xmlEscape(path), xmlEscape(name))
	}
	fmt.Fprint(w, `</D:multistatus>`)
}

// WriteEmptyCalendarQueryResponse returns a valid but empty REPORT response
// (no VEVENT children) - sufficient for tests that only care about the sync
// completing successfully, not about specific event data.
func WriteEmptyCalendarQueryResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(207)
	fmt.Fprint(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"></D:multistatus>`)
}

func xmlEscape(s string) string {
	var buf strings.Builder
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
