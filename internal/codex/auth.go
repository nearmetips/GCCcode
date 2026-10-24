// Package codex switches ChatGPT accounts for Codex by swapping the single
// shared ~/.codex/auth.json. Codex keeps its threads in SQLite inside that
// home, so it is never split per account; only the login file changes.
package codex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Auth is the parsed shape of Codex's auth.json.
type Auth struct {
	Raw         []byte
	Mode        string
	Email       string
	Plan        string
	UserID      string
	AccountID   string
	HasRefresh  bool
	LastRefresh time.Time
}

// RemoteID identifies the ChatGPT user and workspace a login belongs to.
func (a Auth) RemoteID() string {
	if a.UserID == "" && a.AccountID == "" {
		return ""
	}
	return a.UserID + ":" + a.AccountID
}

func LivePath(s core.Settings) string { return filepath.Join(core.CodexHome(s), "auth.json") }

func VaultPath(id string) string { return filepath.Join(core.CodexVaultDir(id), "auth.json") }

func ReadAuthFile(path string) (Auth, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Auth{}, err
	}
	return ParseAuth(b)
}

func ParseAuth(b []byte) (Auth, error) {
	var raw struct {
		Mode   string  `json:"auth_mode"`
		APIKey *string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			IDToken      string `json:"id_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
		LastRefresh string `json:"last_refresh"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Auth{}, fmt.Errorf("auth.json 不是合法 JSON: %w", err)
	}
	a := Auth{Raw: b, Mode: raw.Mode}
	if raw.LastRefresh != "" {
		a.LastRefresh, _ = time.Parse(time.RFC3339Nano, raw.LastRefresh)
	}
	if raw.Tokens == nil {
		if raw.APIKey != nil && *raw.APIKey != "" {
			a.Mode = "apikey"
			return a, nil
		}
		return a, errors.New("auth.json 里没有登录信息")
	}
	a.AccountID = raw.Tokens.AccountID
	a.HasRefresh = raw.Tokens.RefreshToken != ""
	if claims, ok := jwtClaims(raw.Tokens.IDToken); ok {
		a.Email, _ = claims["email"].(string)
		if au, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			a.Plan, _ = au["chatgpt_plan_type"].(string)
			a.UserID, _ = au["chatgpt_user_id"].(string)
			if a.UserID == "" {
				a.UserID, _ = au["user_id"].(string)
			}
			if id, _ := au["chatgpt_account_id"].(string); id != "" {
				a.AccountID = id
			}
		}
	}
	if a.Mode == "" {
		a.Mode = "chatgpt"
	}
	return a, nil
}

// jwtClaims decodes a JWT payload without verifying it; the values are only
// used to label accounts.
func jwtClaims(tok string) (map[string]any, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil, false
	}
	return m, true
}

// FindByRemote returns the vault account a login belongs to.
func FindByRemote(st *core.Store, a Auth) (*core.Account, bool) {
	rid := a.RemoteID()
	if rid == "" {
		return nil, false
	}
	for i := range st.Accounts {
		acc := &st.Accounts[i]
		if acc.Kind == core.KindCodex && acc.RemoteID == rid {
			return acc, true
		}
	}
	return nil, false
}

func saveVault(id string, data []byte) error {
	dir := core.CodexVaultDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := VaultPath(id)
	if old, err := os.ReadFile(p); err == nil && !bytes.Equal(old, data) {
		core.WriteFileAtomic(p+".bak", old, 0o600)
	}
	return core.WriteFileAtomic(p, data, 0o600)
}

// Import stores a login in the vault as a new account, or refreshes the
// account it already belongs to. It returns the account and whether it is new.
func Import(st *core.Store, a Auth, label string) (*core.Account, bool, error) {
	if a.Mode == "apikey" {
		return nil, false, errors.New("这是 API Key 登录，不是 ChatGPT 会员登录")
	}
	if !a.HasRefresh {
		return nil, false, errors.New("auth.json 里缺少 refresh_token，无法长期保存")
	}
	if acc, ok := FindByRemote(st, a); ok {
		if err := writeBack(acc.ID, a); err != nil {
			return nil, false, err
		}
		acc.Email, acc.Plan = a.Email, a.Plan
		return acc, false, nil
	}
	if label == "" {
		label = a.Email
	}
	id := st.NewID(core.KindCodex, label)
	acc := core.Account{
		ID: id, Kind: core.KindCodex, Label: label, Email: a.Email, Plan: a.Plan,
		RemoteID: a.RemoteID(), CreatedAt: time.Now(),
	}
	if err := saveVault(id, a.Raw); err != nil {
		return nil, false, err
	}
	st.Accounts = append(st.Accounts, acc)
	return &st.Accounts[len(st.Accounts)-1], true, nil
}

// writeBack keeps the vault copy current with the tokens Codex rotated while
// it ran. A live file older than the vault copy is refused rather than
// allowed to overwrite a newer refresh token.
func writeBack(id string, live Auth) error {
	if v, err := ReadAuthFile(VaultPath(id)); err == nil {
		if bytes.Equal(v.Raw, live.Raw) {
			return nil
		}
		if !v.LastRefresh.IsZero() && !live.LastRefresh.IsZero() && live.LastRefresh.Before(v.LastRefresh) {
			return fmt.Errorf("~/.codex/auth.json 比账号库里的 %s 更旧，已拒绝覆盖", id)
		}
	}
	return saveVault(id, live.Raw)
}

// SyncBack saves the live login into the vault. A login the vault has never
// seen (the user ran `codex login` by hand) is imported as a new account.
func SyncBack(st *core.Store) (*core.Account, bool, error) {
	live, err := ReadAuthFile(LivePath(st.Settings))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if live.Mode == "apikey" || !live.HasRefresh {
		return nil, false, nil
	}
	acc, isNew, err := Import(st, live, "")
	if err != nil {
		return nil, false, err
	}
	if st.State.CodexCurrent != acc.ID {
		st.State.CodexCurrent = acc.ID
		st.State.CodexSince = time.Now()
	}
	return acc, isNew, nil
}

// Activate installs an account's login as ~/.codex/auth.json. The current
// login is written back to the vault first, and the file it replaces is
// kept in the backups dir.
func Activate(st *core.Store, id string, force bool) error {
	acc, ok := st.Account(id)
	if !ok || acc.Kind != core.KindCodex {
		return fmt.Errorf("没有这个 Codex 账号：%s", id)
	}
	if procs := listProcesses(); len(procs) > 0 && !force {
		return &RunningError{Procs: procs}
	}
	target, err := ReadAuthFile(VaultPath(id))
	if err != nil {
		return fmt.Errorf("读取账号库里的 %s: %w", id, err)
	}
	if !target.HasRefresh {
		return fmt.Errorf("账号库里的 %s 缺少 refresh_token，请用 gcccode account login %s 重新登录", id, id)
	}
	if _, _, err := SyncBack(st); err != nil {
		return fmt.Errorf("写回当前登录失败，已中止切换: %w", err)
	}
	live := LivePath(st.Settings)
	if old, err := os.ReadFile(live); err == nil {
		if bytes.Equal(old, target.Raw) {
			st.State.CodexCurrent = id
			return nil
		}
		backupLive(old)
	}
	if err := core.WriteFileAtomic(live, target.Raw, 0o600); err != nil {
		return err
	}
	st.State.CodexCurrent = id
	st.State.CodexSince = time.Now()
	return nil
}

func backupLive(data []byte) {
	dir := filepath.Join(core.BackupDir(), "codex-auth")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	name := filepath.Join(dir, "auth-"+time.Now().Format("20060102-150405.000")+".json")
	core.WriteFileAtomic(name, data, 0o600)
	entries, _ := os.ReadDir(dir)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	for _, e := range entries[min(len(entries), 20):] {
		os.Remove(filepath.Join(dir, e.Name()))
	}
}

// RunningError lists the Codex processes that block a switch.
type RunningError struct{ Procs []string }

func (e *RunningError) Error() string {
	return "Codex 正在运行，换号前请先退出（否则它会把旧账号的 token 写回）：\n  " + strings.Join(e.Procs, "\n  ")
}
