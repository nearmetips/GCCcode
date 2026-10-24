package codex

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func authJSON(email, user, account, refresh, last string) []byte {
	raw := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": jwt(map[string]any{
				"email": email,
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_user_id":    user,
					"chatgpt_account_id": account,
					"chatgpt_plan_type":  "plus",
				},
			}),
			"access_token":  "access",
			"refresh_token": refresh,
			"account_id":    account,
		},
		"last_refresh": last,
	}
	b, _ := json.Marshal(raw)
	return b
}

func TestParseAndSwap(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	listProcesses = func() []string { return nil }
	t.Cleanup(func() { listProcesses = Running })

	home := t.TempDir()
	st := &core.Store{Settings: core.DefaultSettings(), Quota: map[string]core.Quota{}}
	st.Settings.CodexHome = home
	st.State.Occupied = map[string]core.Occupancy{}
	st.State.Cooldown = map[string]int64{}
	st.State.Notified = map[string]int64{}

	older := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	newer := time.Now().UTC().Format(time.RFC3339Nano)
	a := authJSON("a@example.com", "user-a", "acc-a", "refresh-a", older)
	b := authJSON("b@example.com", "user-b", "acc-b", "refresh-b", newer)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), a, 0o600); err != nil {
		t.Fatal(err)
	}
	first, isNew, err := SyncBack(st)
	if err != nil || !isNew || first.Email != "a@example.com" || first.Plan != "plus" {
		t.Fatalf("import a: %+v new=%v err=%v", first, isNew, err)
	}
	second, _, err := Import(st, mustParse(t, b), "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := Activate(st, second.ID, false); err != nil {
		t.Fatal(err)
	}
	live, err := ReadAuthFile(LivePath(st.Settings))
	if err != nil || live.Email != "b@example.com" {
		t.Fatalf("live = %s %v", live.Email, err)
	}
	kept, err := ReadAuthFile(VaultPath(first.ID))
	if err != nil || kept.Email != "a@example.com" {
		t.Fatalf("vault a was overwritten: %s %v", kept.Email, err)
	}
	rotated := authJSON("b@example.com", "user-b", "acc-b", "refresh-b2", newer)
	if err := os.WriteFile(LivePath(st.Settings), rotated, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SyncBack(st); err != nil {
		t.Fatal(err)
	}
	back, _ := ReadAuthFile(VaultPath(second.ID))
	if !back.HasRefresh || string(back.Raw) != string(rotated) {
		t.Fatal("rotated refresh token was not written back")
	}
}

func TestRejectsStaleWriteBack(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	home := t.TempDir()
	st := &core.Store{Settings: core.DefaultSettings()}
	st.Settings.CodexHome = home
	os.MkdirAll(home, 0o700)
	newer := time.Now().UTC().Format(time.RFC3339Nano)
	older := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, _, err := Import(st, mustParse(t, authJSON("a@example.com", "user-a", "acc-a", "new", newer)), ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), authJSON("a@example.com", "user-a", "acc-a", "old", older), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SyncBack(st); err == nil {
		t.Fatal("stale auth.json overwrote the vault")
	}
}

func mustParse(t *testing.T, b []byte) Auth {
	t.Helper()
	a, err := ParseAuth(b)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
