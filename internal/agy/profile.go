// Package agy brings the Antigravity CLI into the pool. agy keeps its login
// in one fixed entry of the user's keychain, the same entry for every HOME
// that reaches that keychain, so only the login already on this machine
// joins the pool for now.
package agy

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

const tokenName = "jetski-standalone-oauth-token"

// TokenPath is agy's file storage for a login, used when the keychain is
// bypassed or unreachable.
func TokenPath(home string) string { return filepath.Join(home, ".gemini", tokenName) }

// keychainHas reports whether agy's keychain entry exists for home. It reads
// the entry's attributes only, never the stored login. Other systems keep the
// entry in a credential store that cannot be checked without reading the
// secret, so agy's log of a sign-in stands in for it there.
var keychainHas = func(home string) bool {
	if runtime.GOOS != "darwin" {
		return lastSignIn(home) != ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", "gemini", "-a", "antigravity")
	cmd.Env = append(withoutHome(os.Environ()), "HOME="+home)
	return cmd.Run() == nil
}

func withoutHome(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "HOME=") {
			out = append(out, e)
		}
	}
	return out
}

// Identity is the Google account agy is signed in as.
type Identity struct {
	Email  string
	UserID string
}

// Ready reports whether agy has a login in home.
func Ready(home string) bool {
	_, ok := readTokenFile(home)
	return ok || keychainHas(home)
}

// ReadIdentity reports whether agy has a login in home, and as whom. The
// email comes from agy's log of its last sign-in, which follows the keychain
// login; the token file may hold an older one.
func ReadIdentity(home string) (Identity, bool) {
	file, fileOK := readTokenFile(home)
	if !fileOK && !keychainHas(home) {
		return Identity{}, false
	}
	idty := Identity{Email: lastSignIn(home)}
	if fileOK && (idty.Email == "" || strings.EqualFold(file.Email, idty.Email)) {
		idty = file
	}
	return idty, true
}

func readTokenFile(home string) (Identity, bool) {
	b, err := os.ReadFile(TokenPath(home))
	if err != nil {
		return Identity{}, false
	}
	var f struct {
		Token struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"token"`
		IDToken string `json:"id_token"`
	}
	if json.Unmarshal(b, &f) != nil || f.Token.RefreshToken == "" && f.Token.AccessToken == "" {
		return Identity{}, false
	}
	var claims struct {
		Email string `json:"email"`
		Sub   string `json:"sub"`
	}
	if parts := strings.Split(f.IDToken, "."); len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "=")); err == nil {
			_ = json.Unmarshal(payload, &claims)
		}
	}
	return Identity{Email: claims.Email, UserID: claims.Sub}, true
}

var signIn = regexp.MustCompile(`applyAuthResult: email=([^,\s]+)`)

// lastSignIn is the email of the newest sign-in agy logged in home.
func lastSignIn(home string) string {
	dir := filepath.Join(cliDir(home), "log")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type logFile struct {
		path string
		mod  time.Time
	}
	var logs []logFile
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			logs = append(logs, logFile{filepath.Join(dir, e.Name()), fi.ModTime()})
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].mod.After(logs[j].mod) })
	for i, l := range logs {
		if i == 20 {
			break
		}
		f, err := os.Open(l.path)
		if err != nil {
			continue
		}
		email := ""
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			if m := signIn.FindStringSubmatch(sc.Text()); m != nil {
				email = m[1]
			}
		}
		f.Close()
		if email != "" {
			return email
		}
	}
	return ""
}

// ProxyProblem is why agy should not use proxy as written, or "".
func ProxyProblem(proxy string) string {
	p := strings.ToLower(strings.TrimSpace(proxy))
	if p == "" {
		return ""
	}
	if !strings.Contains(p, "://") {
		return "代理地址要带协议，例如 http://127.0.0.1:7890"
	}
	for _, scheme := range []string{"http://", "https://", "socks5://", "socks5h://"} {
		if strings.HasPrefix(p, scheme) {
			return ""
		}
	}
	return "Antigravity 出口请用 http://、https:// 或 socks5:// 代理"
}

// AccountByDir maps a HOME back to a pool account.
func AccountByDir(st *core.Store, dir string) (*core.Account, bool) {
	if dir == "" {
		return nil, false
	}
	clean := filepath.Clean(dir)
	for i := range st.Accounts {
		a := &st.Accounts[i]
		if a.Kind == core.KindAgy && filepath.Clean(a.ConfigDir) == clean {
			return a, true
		}
	}
	return nil, false
}
