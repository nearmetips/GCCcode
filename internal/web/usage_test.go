package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/usage"
)

func TestUsageReportsTheRequestedRange(t *testing.T) {
	stubActions(t)
	usage.Reset()
	// Point every client home at an empty dir so the test never reads the
	// developer's own transcripts.
	if _, err := core.Mutate(func(s *core.Store) error {
		s.Settings.ClaudeHome = t.TempDir()
		s.Settings.CodexHome = t.TempDir()
		s.Settings.GrokHome = t.TempDir()
		s.Settings.GeminiHome = t.TempDir()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleUsage(rec, httptest.NewRequest(http.MethodGet, "/api/usage?days=7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var rep usage.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Days != 7 || len(rep.Daily) != 7 {
		t.Fatalf("days %d, daily %d", rep.Days, len(rep.Daily))
	}
	// The pool's account shows up even with nothing used yet.
	if len(rep.Accounts) != 1 || rep.Accounts[0].ID != "cl-1" {
		t.Fatalf("accounts %+v", rep.Accounts)
	}
	for _, bad := range []string{"0", "abc", "400"} {
		rec := httptest.NewRecorder()
		handleUsage(rec, httptest.NewRequest(http.MethodGet, "/api/usage?days="+bad, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("days=%s: %d", bad, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	handleUsage(rec, httptest.NewRequest(http.MethodPost, "/api/usage", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
}
