package agy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// fakeSqlite makes the sqlite3 lookup return a script that prints rows.
func fakeSqlite(t *testing.T, rows string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sqlite3")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat <<'EOF'\n"+rows+"\nEOF\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := sqlite
	sqlite = func() (string, error) { return bin, nil }
	t.Cleanup(func() { sqlite = old })
}

func conversation(t *testing.T, home, id string, mod time.Time) {
	t.Helper()
	dir := conversationsDir(home)
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, id+".db")
	os.WriteFile(path, []byte("SQLite format 3\x00"), 0o600)
	os.Chtimes(path, mod, mod)
}

func index(home string) {
	os.WriteFile(filepath.Join(cliDir(home), "conversation_summaries.db"), nil, 0o600)
}

func TestRecentReadsTheIndexAndSkipsSubagents(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	now := time.Now()
	conversation(t, home, "c-old", now.Add(-2*time.Hour))
	conversation(t, home, "c-new", now.Add(-time.Hour))
	conversation(t, home, "c-sub", now)
	index(home)
	fakeSqlite(t, `[
{"conversation_id":"c-old","title":"","preview":"fix the  login\nbug","last_modified_time":"`+now.Add(-2*time.Hour).Format("2006-01-02 15:04:05.999999999-07:00")+`","workspace_uris":"[\"file://`+filepath.ToSlash(work)+`\"]","parent_conversation_id":"","nesting_depth":0},
{"conversation_id":"c-new","title":"Add tests","preview":"","last_modified_time":"`+now.Add(-time.Hour).Format(time.RFC3339Nano)+`","workspace_uris":"file:///elsewhere","parent_conversation_id":"","nesting_depth":0},
{"conversation_id":"c-sub","title":"subagent","preview":"","last_modified_time":"`+now.Format(time.RFC3339Nano)+`","workspace_uris":"","parent_conversation_id":"c-new","nesting_depth":1}
]`)
	s := core.DefaultSettings()
	all := Recent(s, home, "", 0)
	if len(all) != 2 || all[0].ID != "c-new" || all[1].ID != "c-old" {
		t.Fatalf("recent %+v", all)
	}
	if all[1].Title != "fix the login bug" || all[1].Cwd != work {
		t.Fatalf("title/cwd from preview and workspace: %+v", all[1])
	}
	here := Recent(s, home, work, 0)
	if len(here) != 1 || here[0].ID != "c-old" {
		t.Fatalf("recent in %s: %+v", work, here)
	}
	if latest, ok := Latest(s, home, "", now.Add(-90*time.Minute)); !ok || latest.ID != "c-new" {
		t.Fatalf("latest skipped the subagent? %+v %v", latest, ok)
	}
}

func TestStorageUpgradeRewritesDoNotCountAsUse(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	used := now.Add(-72 * time.Hour)
	conversation(t, home, "c-idle", now)
	conversation(t, home, "c-this-run", now)
	index(home)
	fakeSqlite(t, `[
{"conversation_id":"c-idle","title":"Old work","preview":"","last_modified_time":"`+used.Format(time.RFC3339Nano)+`","workspace_uris":"","parent_conversation_id":"","nesting_depth":0}
]`)
	s := core.DefaultSettings()
	if got := Recent(s, home, "", 0); len(got) != 1 || !got[0].Mod.Equal(used) {
		t.Fatalf("an idle conversation should keep the time agy last used it: %+v", got)
	}
	if got, ok := Latest(s, home, "", now.Add(-time.Minute)); !ok || got.ID != "c-this-run" {
		t.Fatalf("latest picked a rewritten idle conversation: %+v %v", got, ok)
	}
}

func TestFindSessionWithoutAnIndexOrSqlite(t *testing.T) {
	home := t.TempDir()
	conversation(t, home, "legacy", time.Now())
	old := sqlite
	sqlite = func() (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { sqlite = old })
	s := core.DefaultSettings()
	if sess, ok := FindSession(s, home, "legacy"); !ok || sess.Path == "" {
		t.Fatalf("legacy conversation not found by id: %+v", sess)
	}
	if _, ok := FindSession(s, home, "../legacy"); ok {
		t.Fatal("path-like ids must not resolve")
	}
	if got, ok := Latest(s, home, "/anywhere", time.Now().Add(-time.Minute)); !ok || got.ID != "legacy" {
		t.Fatalf("latest without index = %+v %v", got, ok)
	}
}

// TestRealSqliteReadsAgysIndex runs the query against agy's own schema.
func TestRealSqliteReadsAgysIndex(t *testing.T) {
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not installed")
	}
	home := t.TempDir()
	conversation(t, home, "real-1", time.Now())
	db := filepath.Join(cliDir(home), "conversation_summaries.db")
	schema := "CREATE TABLE `conversation_summaries` (`conversation_id` text,`title` text NOT NULL DEFAULT \"\",`preview` text NOT NULL DEFAULT \"\",`step_count` integer NOT NULL DEFAULT 0,`last_modified_time` datetime NOT NULL,`workspace_uris` text NOT NULL,`status` text NOT NULL DEFAULT \"\",`source` text NOT NULL DEFAULT \"\",`project_id` text NOT NULL DEFAULT \"\",`agent_name` text NOT NULL DEFAULT \"\",`parent_conversation_id` text NOT NULL DEFAULT \"\",`nesting_depth` integer NOT NULL DEFAULT 0,`battle_id` text NOT NULL DEFAULT \"\",`winning_conversation_id` text NOT NULL DEFAULT \"\",`not_fully_idle` numeric NOT NULL DEFAULT false,`killed` numeric NOT NULL DEFAULT false,`last_user_input_time` datetime NOT NULL,`last_user_input_step_index` integer NOT NULL DEFAULT -1,`app_data_dir` text NOT NULL DEFAULT \"\",PRIMARY KEY (`conversation_id`));" +
		"PRAGMA journal_mode=WAL;" +
		"INSERT INTO conversation_summaries (conversation_id,title,last_modified_time,workspace_uris,last_user_input_time) VALUES ('real-1','Refactor it','2026-10-12 12:30:00.123+08:00','[\"file:///tmp/proj\"]','2026-10-12 12:30:00+08:00');"
	if out, err := exec.Command(bin, db, schema).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v %s", err, out)
	}
	got := Recent(core.DefaultSettings(), home, "", 0)
	if len(got) != 1 || got[0].Title != "Refactor it" || got[0].Cwd != "/tmp/proj" {
		t.Fatalf("recent from a real index: %+v", got)
	}
}
