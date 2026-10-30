package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/usage"
)

// handleUsage answers GET /api/usage?days=N with the usage report for the
// last N local days (default 30).
func handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > usage.MaxDays {
			http.Error(w, "days 要在 1 到 365 之间", http.StatusBadRequest)
			return
		}
		days = n
	}
	st, err := core.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, usage.Build(st, days, time.Now()))
}
