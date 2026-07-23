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

package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/mscreations/hhq/internal/report"
)

// WeeklyReportDownload lets a logged-in parent generate and download the
// current week's report on demand, in addition to the automatic email sent
// by internal/scheduler at week's end.
func (a *App) WeeklyReportDownload(w http.ResponseWriter, r *http.Request) {
	weekStart := startOfWeek(time.Now())
	if v := r.URL.Query().Get("week_start"); v != "" {
		if parsed, err := time.Parse("2006-01-02", v); err == nil {
			weekStart = parsed
		}
	}

	instances, err := a.ChoreInstances.ListForWeek(r.Context(), weekStart)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	pdfBytes, err := report.BuildWeeklyPDF(weekStart, instances)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("chore-report-%s.pdf", weekStart.Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Write(pdfBytes)
}
