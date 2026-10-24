package claude

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Credentials is the OAuth login Claude Code keeps for one account dir. Only
// the gateway's subscription routing reads it; GCC Code stores no copy.
type Credentials struct {
	Raw          []byte // the whole stored document, other logins included
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
}

type oauthDoc struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
}

// errNoKeychain means this system has no keychain to look in.
var errNoKeychain = errors.New("no keychain")

func credentialsService(s core.Settings, dir string) string {
	if IsMainHome(s, dir) {
		return KeychainService("")
	}
	return KeychainService(dir)
}

func credentialsFile(dir string) string { return filepath.Join(dir, ".credentials.json") }

// ReadCredentials reads dir's login where Claude Code keeps it: the keychain
// on macOS, otherwise (or when the keychain has none) .credentials.json.
func ReadCredentials(s core.Settings, dir string) (Credentials, error) {
	raw, err := keychainRead(credentialsService(s, dir))
	if err != nil || len(raw) == 0 {
		if raw, err = os.ReadFile(credentialsFile(dir)); err != nil {
			return Credentials{}, errors.New("找不到这个号的登录，先 gcccode account login 重新登录")
		}
	}
	var doc struct {
		OAuth *oauthDoc `json:"claudeAiOauth"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.OAuth == nil || doc.OAuth.AccessToken == "" {
		return Credentials{}, errors.New("这个号的登录信息里没有会员 token，先 gcccode account login 重新登录")
	}
	c := Credentials{Raw: raw, AccessToken: doc.OAuth.AccessToken, RefreshToken: doc.OAuth.RefreshToken, Scopes: doc.OAuth.Scopes}
	if doc.OAuth.ExpiresAt > 0 {
		c.ExpiresAt = time.UnixMilli(doc.OAuth.ExpiresAt)
	}
	return c, nil
}

// WriteCredentials puts a refreshed login back where ReadCredentials found
// it, keeping everything else in the document.
func WriteCredentials(s core.Settings, dir string, c Credentials) error {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(c.Raw, &doc); err != nil {
		return err
	}
	var oauth map[string]any
	if err := json.Unmarshal(doc["claudeAiOauth"], &oauth); err != nil || oauth == nil {
		return errors.New("登录信息里没有 claudeAiOauth")
	}
	oauth["accessToken"] = c.AccessToken
	if c.RefreshToken != "" {
		oauth["refreshToken"] = c.RefreshToken
	}
	if !c.ExpiresAt.IsZero() {
		oauth["expiresAt"] = c.ExpiresAt.UnixMilli()
	}
	if len(c.Scopes) > 0 {
		oauth["scopes"] = c.Scopes
	}
	var err error
	if doc["claudeAiOauth"], err = json.Marshal(oauth); err != nil {
		return err
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	service := credentialsService(s, dir)
	if have, err := keychainRead(service); err == nil && len(have) > 0 {
		if err := keychainWrite(service, out); err != nil {
			return err
		}
		if back, err := keychainRead(service); err != nil || !bytes.Equal(back, out) {
			return errors.New("写回钥匙串后读回来的内容不一致")
		}
		return nil
	}
	if _, err := os.Stat(credentialsFile(dir)); err != nil {
		return errors.New("找不到原来的登录位置，没有写回")
	}
	return core.WriteFileAtomic(credentialsFile(dir), out, 0o600)
}

// DeviceID is the userID Claude Code minted for dir, which it sends as the
// device in every subscription request.
func DeviceID(s core.Settings, dir string) string { return readDevice(ConfigJSON(s, dir)).UserID }

// keychainUser is the account attribute Claude Code files its entries under.
func keychainUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

var keychainRead = func(service string) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, errNoKeychain
	}
	out, err := exec.Command("security", "find-generic-password", "-a", keychainUser(), "-w", "-s", service).Output()
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(out), nil
}

// securityLineMax is the longest line `security -i` takes on stdin, the same
// limit Claude Code uses before it falls back to arguments.
const securityLineMax = 4032

// keychainWrite updates the entry the way Claude Code does: through
// `security -i` on stdin with the value hex-encoded, so the token does not
// show up in a process list.
var keychainWrite = func(service string, data []byte) error {
	if runtime.GOOS != "darwin" {
		return errNoKeychain
	}
	user, value := keychainUser(), hex.EncodeToString(data)
	line := `add-generic-password -U -a "` + user + `" -s "` + service + `" -X "` + value + `" `
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader(line + "\n")
	if len(line) > securityLineMax {
		cmd = exec.Command("security", "add-generic-password", "-U", "-a", user, "-s", service, "-X", value)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("写回钥匙串失败：%v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
