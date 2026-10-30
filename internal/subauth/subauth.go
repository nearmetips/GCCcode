// Package subauth hands the gateway a working access token for a pooled
// subscription login, refreshing it shortly before it expires. Tokens stay
// where the official clients keep them (the Codex vault and ~/.codex, the
// Claude keychain entry), so a refresh is never lost and a refresh token is
// never spent twice.
package subauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
)

// Token endpoints and the official clients' public OAuth client ids.
var (
	CodexTokenURL  = "https://auth.openai.com/oauth/token"
	ClaudeTokenURL = "https://platform.claude.com/v1/oauth/token"
)

const (
	codexClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	claudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// refreshMargin is how long before expiry a token is renewed, so a
	// long answer never starts on a token that dies halfway.
	refreshMargin = 5 * time.Minute
)

// Codex is what a request to the ChatGPT backend needs.
type Codex struct{ Access, AccountID string }

// Claude is what a request to Anthropic needs.
type Claude struct{ Access string }

// ReloginError means the login itself is dead: only signing in again helps.
type ReloginError struct{ Reason string }

func (e *ReloginError) Error() string { return e.Reason }

// BusyError means the token expired while a pooled session holds the
// account; that session's own client renews it, and the gateway waits.
type BusyError struct{}

func (BusyError) Error() string { return "这个号正在号池会话里用，token 由那边续期" }

// Source refreshes tokens. One refresh per account runs at a time.
type Source struct {
	// HTTP is the client a refresh for acc goes through, so it leaves from
	// the same egress as that account's requests.
	HTTP func(acc core.Account) *http.Client
	Now  func() time.Time

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func New(httpFor func(core.Account) *http.Client) *Source {
	return &Source{HTTP: httpFor, Now: time.Now, locks: map[string]*sync.Mutex{}}
}

func (s *Source) lock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	return l
}

func fresh(exp, now time.Time) bool { return exp.IsZero() || exp.Sub(now) > refreshMargin }

// Codex returns acc's token, renewing it when it is close to expiry or when
// force says the upstream just refused it.
func (s *Source) Codex(ctx context.Context, settings core.Settings, acc core.Account, force bool) (Codex, error) {
	l := s.lock(acc.ID)
	l.Lock()
	defer l.Unlock()
	raw, err := codexCurrent(settings, acc)
	if err != nil {
		return Codex{}, err
	}
	t, err := codex.ReadTokens(raw)
	if err != nil {
		return Codex{}, &ReloginError{err.Error()}
	}
	if !force && fresh(t.Expiry(), s.Now()) {
		return Codex{Access: t.Access, AccountID: t.AccountID}, nil
	}
	if t.Refresh == "" {
		return Codex{}, &ReloginError{"登录里没有 refresh_token"}
	}
	got, err := s.refreshCodex(ctx, acc, t.Refresh)
	if err != nil {
		return Codex{}, err
	}
	next, err := codex.WithTokens(raw, got, s.Now())
	if err != nil {
		return Codex{}, err
	}
	if err := saveCodex(settings, acc, t.Refresh, next); err != nil {
		return Codex{}, fmt.Errorf("token 已刷新但没能保存：%w", err)
	}
	nt, err := codex.ReadTokens(next)
	if err != nil {
		return Codex{}, err
	}
	return Codex{Access: nt.Access, AccountID: nt.AccountID}, nil
}

// codexCurrent is the newest copy of acc's login: ~/.codex/auth.json while
// Codex is signed in as acc and has rotated tokens since, else the vault.
func codexCurrent(settings core.Settings, acc core.Account) ([]byte, error) {
	vault, verr := os.ReadFile(codex.VaultPath(acc.ID))
	if live, err := os.ReadFile(codex.LivePath(settings)); err == nil {
		la, err := codex.ParseAuth(live)
		if err == nil && la.RemoteID() != "" && la.RemoteID() == acc.RemoteID {
			if verr != nil {
				return live, nil
			}
			va, _ := codex.ParseAuth(vault)
			if !bytes.Equal(live, vault) && !la.LastRefresh.Before(va.LastRefresh) {
				_, err := core.Mutate(func(*core.Store) error { return codex.SaveVault(acc.ID, live) })
				return live, err
			}
		}
	}
	if verr != nil {
		return nil, &ReloginError{"账号库里没有这个号的登录"}
	}
	return vault, nil
}

// saveCodex stores a refreshed login in the vault, and in ~/.codex/auth.json
// too while that file still holds acc with the refresh token just spent:
// Codex rereads the file before refreshing, so it picks up the new one
// instead of failing on a used token.
func saveCodex(settings core.Settings, acc core.Account, spent string, next []byte) error {
	_, err := core.Mutate(func(*core.Store) error {
		if err := codex.SaveVault(acc.ID, next); err != nil {
			return err
		}
		live, err := os.ReadFile(codex.LivePath(settings))
		if err != nil {
			return nil
		}
		la, err := codex.ParseAuth(live)
		if err != nil || la.RemoteID() != acc.RemoteID {
			return nil
		}
		if lt, err := codex.ReadTokens(live); err == nil && lt.Refresh == spent {
			return core.WriteFileAtomic(codex.LivePath(settings), next, 0o600)
		}
		return nil
	})
	return err
}

func (s *Source) refreshCodex(ctx context.Context, acc core.Account, refresh string) (codex.Tokens, error) {
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token", "refresh_token": refresh})
	var out struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := s.post(ctx, acc, CodexTokenURL, body, &out); err != nil {
		return codex.Tokens{}, err
	}
	if out.AccessToken == "" {
		return codex.Tokens{}, errors.New("刷新 token 的回复里没有 access_token")
	}
	return codex.Tokens{IDToken: out.IDToken, Access: out.AccessToken, Refresh: out.RefreshToken}, nil
}

// Claude returns acc's token. busy says a pooled Claude Code session holds
// the account: an expired token is then left for that session to renew,
// since two refreshers of one login would spend each other's tokens.
func (s *Source) Claude(ctx context.Context, settings core.Settings, acc core.Account, busy, force bool) (Claude, error) {
	l := s.lock(acc.ID)
	l.Lock()
	defer l.Unlock()
	if claude.UsesToken(acc) {
		tok, ok := claude.ReadToken(acc.ID)
		if !ok || force {
			return Claude{}, &ReloginError{"这个号的长期 token 不能用了（过期或被撤销）。重新拿一个：gcccode account login " + acc.ID}
		}
		return Claude{Access: tok}, nil
	}
	c, err := claude.ReadCredentials(settings, acc.ConfigDir)
	if err != nil {
		return Claude{}, &ReloginError{err.Error()}
	}
	now := s.Now()
	if !force && fresh(c.ExpiresAt, now) {
		return Claude{Access: c.AccessToken}, nil
	}
	if busy {
		if !force && (c.ExpiresAt.IsZero() || now.Before(c.ExpiresAt)) {
			return Claude{Access: c.AccessToken}, nil
		}
		return Claude{}, BusyError{}
	}
	if c.RefreshToken == "" {
		return Claude{}, &ReloginError{"登录里没有 refresh token"}
	}
	body, _ := json.Marshal(map[string]string{
		"grant_type": "refresh_token", "refresh_token": c.RefreshToken,
		"client_id": claudeClientID, "scope": strings.Join(c.Scopes, " "),
	})
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := s.post(ctx, acc, ClaudeTokenURL, body, &out); err != nil {
		return Claude{}, err
	}
	if out.AccessToken == "" {
		return Claude{}, errors.New("刷新 token 的回复里没有 access_token")
	}
	next := c
	next.AccessToken, next.RefreshToken = out.AccessToken, out.RefreshToken
	if out.ExpiresIn > 0 {
		next.ExpiresAt = s.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	if out.Scope != "" {
		next.Scopes = strings.Fields(out.Scope)
	}
	if now, err := claude.ReadCredentials(settings, acc.ConfigDir); err == nil && now.RefreshToken != c.RefreshToken {
		// Claude Code renewed it meanwhile; its copy is the one to keep.
		return Claude{Access: now.AccessToken}, nil
	}
	if err := claude.WriteCredentials(settings, acc.ConfigDir, next); err != nil {
		return Claude{}, fmt.Errorf("token 已刷新但没能写回：%w", err)
	}
	return Claude{Access: next.AccessToken}, nil
}

// post sends a token request and decodes the answer, sorting refusals into
// a dead login (ReloginError) or a passing failure.
func (s *Source) post(ctx context.Context, acc core.Account, url string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := http.DefaultClient
	if s.HTTP != nil {
		client = s.HTTP(acc)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("刷新 token 时连接失败：%w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		code := errorCode(b)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
			code == "invalid_grant" || strings.HasPrefix(code, "refresh_token_") {
			return &ReloginError{fmt.Sprintf("登录已失效（%s），需要重新登录", firstNonEmpty(code, resp.Status))}
		}
		return fmt.Errorf("刷新 token 失败：HTTP %d %s", resp.StatusCode, code)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return errors.New("刷新 token 的回复不是合法 JSON")
	}
	return nil
}

// errorCode reads an OAuth error code, given as {"error":"x"} or
// {"error":{"code":"x"}}.
func errorCode(b []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &e) != nil || len(e.Error) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(e.Error, &s) == nil {
		return strings.ToLower(s)
	}
	var o struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	if json.Unmarshal(e.Error, &o) == nil {
		return strings.ToLower(firstNonEmpty(o.Code, o.Type))
	}
	return ""
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
