package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/agy"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gemini"
	"github.com/nearmetips/DailiCode/internal/grok"
	"github.com/nearmetips/DailiCode/internal/handoff"
)

// sessionRow is one resumable session of either client.
type sessionRow struct {
	Kind  string
	ID    string
	Path  string
	Cwd   string
	Mod   time.Time
	Owner string
	Title string
}

// recentSessions lists sessions the user said something in, newest first,
// at most limit when limit > 0, only those started in dir unless dir is "".
func recentSessions(st *core.Store, dir string, limit int) []sessionRow {
	var rows []sessionRow
	for _, s := range claude.Recent(st.Settings, dir, 0) {
		rows = append(rows, sessionRow{Kind: core.KindClaude, ID: s.ID, Path: s.Path, Cwd: s.Cwd, Mod: s.Mod, Owner: st.State.SessionOwner[s.ID]})
	}
	for _, s := range codex.Recent(st.Settings, dir, 0) {
		rows = append(rows, sessionRow{Kind: core.KindCodex, ID: s.ID, Path: s.Path, Cwd: s.Cwd, Mod: s.Mod, Owner: codex.Owner(st, s)})
	}
	for _, a := range st.ByKind(core.KindGemini) {
		for _, s := range gemini.Recent(st.Settings, a.ConfigDir, dir, 0) {
			owner := a.ID
			if st.State.SessionOwner[s.ID] != "" {
				owner = st.State.SessionOwner[s.ID]
			}
			rows = append(rows, sessionRow{Kind: core.KindGemini, ID: s.ID, Path: s.Path, Cwd: s.Cwd, Mod: s.Mod, Owner: owner, Title: s.Title})
		}
	}
	// Also scan the main Gemini home for imported / single-account use.
	for _, s := range gemini.Recent(st.Settings, "", dir, 0) {
		dup := false
		for _, r := range rows {
			if r.Kind == core.KindGemini && r.ID == s.ID {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		rows = append(rows, sessionRow{Kind: core.KindGemini, ID: s.ID, Path: s.Path, Cwd: s.Cwd, Mod: s.Mod, Owner: st.State.SessionOwner[s.ID], Title: s.Title})
	}
	for _, a := range st.ByKind(core.KindGrok) {
		for _, s := range grok.Recent(st.Settings, a.ConfigDir, dir, 0) {
			owner := a.ID
			if st.State.SessionOwner[s.ID] != "" {
				owner = st.State.SessionOwner[s.ID]
			}
			title := s.Summary
			rows = append(rows, sessionRow{Kind: core.KindGrok, ID: s.ID, Path: s.Path, Cwd: s.Cwd, Mod: s.Mod, Owner: owner, Title: title})
		}
	}
	for _, s := range grok.Recent(st.Settings, "", dir, 0) {
		dup := false
		for _, r := range rows {
			if r.Kind == core.KindGrok && r.ID == s.ID {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		rows = append(rows, sessionRow{Kind: core.KindGrok, ID: s.ID, Path: s.Path, Cwd: s.Cwd, Mod: s.Mod, Owner: st.State.SessionOwner[s.ID], Title: s.Summary})
	}
	agyOwner := ""
	if a, ok := agy.AccountByDir(st, core.AgyHome(st.Settings)); ok {
		agyOwner = a.ID
	}
	for _, s := range agy.Recent(st.Settings, "", dir, 0) {
		// The conversation file is SQLite, not a transcript Title can read.
		title := or(s.Title, "（没有标题）")
		rows = append(rows, sessionRow{Kind: core.KindAgy, ID: s.ID, Path: s.Path, Cwd: s.Cwd, Mod: s.Mod, Owner: agyOwner, Title: title})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Mod.After(rows[j].Mod) })
	var out []sessionRow
	for _, r := range rows {
		if r.Title == "" {
			r.Title = handoff.Title(r.Path)
		}
		if r.Title == "" {
			continue
		}
		out = append(out, r)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

func cmdSessions(args []string) error {
	limit, all := 10, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-a", "--all":
			all = true
		case "-n":
			if i+1 >= len(args) {
				return fmt.Errorf("-n 后面要跟条数")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				return fmt.Errorf("-n 后面要跟正整数")
			}
			limit = n
			i++
		default:
			return fmt.Errorf("用法：gcccode sessions [--all] [-n 10]")
		}
	}
	st, err := core.Load()
	if err != nil {
		return err
	}
	dir := ""
	if !all {
		dir, _ = os.Getwd()
	}
	rows := recentSessions(st, dir, limit)
	if len(rows) == 0 && dir != "" {
		fmt.Println("这个目录没有会话记录，下面是所有目录最近的会话。")
		fmt.Println()
		dir = ""
		rows = recentSessions(st, "", limit)
	}
	if len(rows) == 0 {
		fmt.Println("本地还没有会话记录。")
		return nil
	}
	for _, r := range rows {
		head := r.Mod.Local().Format("01-02 15:04") + "  " + r.Kind
		if name := accountName(st, r.Owner); name != "" {
			head += " · " + name
		}
		if dir == "" && r.Cwd != "" {
			head += " · " + tildePath(r.Cwd)
		}
		fmt.Println(head)
		fmt.Println("  " + r.Title)
		fmt.Println("  gcccode resume " + r.ID)
		fmt.Println()
	}
	if dir != "" {
		fmt.Println("看所有目录：gcccode sessions --all")
	}
	return nil
}

// latestHere is the newest session started in the current directory.
func latestHere(st *core.Store) (sessionRow, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return sessionRow{}, err
	}
	rows := recentSessions(st, cwd, 1)
	if len(rows) == 0 {
		return sessionRow{}, fmt.Errorf("这个目录没有会话记录。gcccode sessions --all 看所有目录的会话")
	}
	return rows[0], nil
}

func accountName(st *core.Store, id string) string {
	if id == "" {
		return ""
	}
	if a, ok := st.Account(id); ok && a.Label != "" {
		return a.Label
	}
	return id
}

func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
