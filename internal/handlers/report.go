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
