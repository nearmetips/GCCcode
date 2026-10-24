package codex

import (
	"encoding/json"
	"errors"
	"time"
)

// Tokens are the secrets inside a ChatGPT login. Only the gateway's
// subscription routing reads them; nothing else in GCC Code does.
type Tokens struct {
	IDToken   string
	Access    string
	Refresh   string
	AccountID string
}

// ReadTokens pulls the tokens out of an auth.json.
func ReadTokens(raw []byte) (Tokens, error) {
	var f struct {
		Tokens *struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || f.Tokens == nil || f.Tokens.AccessToken == "" {
		return Tokens{}, errors.New("auth.json 里没有 ChatGPT 登录的 token")
	}
	t := Tokens{IDToken: f.Tokens.IDToken, Access: f.Tokens.AccessToken, Refresh: f.Tokens.RefreshToken, AccountID: f.Tokens.AccountID}
	if a, err := ParseAuth(raw); err == nil && a.AccountID != "" {
		t.AccountID = a.AccountID
	}
	return t, nil
}

// Expiry is when the access token stops working; zero when it does not say.
func (t Tokens) Expiry() time.Time {
	claims, ok := jwtClaims(t.Access)
	if !ok {
		return time.Time{}
	}
	if exp, ok := claims["exp"].(float64); ok && exp > 0 {
		return time.Unix(int64(exp), 0)
	}
	return time.Time{}
}

// WithTokens is raw with the tokens a refresh returned put in and
// last_refresh set to now. Empty fields keep their old value, and every other
// field of the file is kept as it was.
func WithTokens(raw []byte, t Tokens, now time.Time) ([]byte, error) {
	var f map[string]json.RawMessage
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	var tok map[string]any
	if err := json.Unmarshal(f["tokens"], &tok); err != nil || tok == nil {
		return nil, errors.New("auth.json 里没有 tokens")
	}
	for k, v := range map[string]string{"id_token": t.IDToken, "access_token": t.Access, "refresh_token": t.Refresh} {
		if v != "" {
			tok[k] = v
		}
	}
	var err error
	if f["tokens"], err = json.Marshal(tok); err != nil {
		return nil, err
	}
	if f["last_refresh"], err = json.Marshal(now.UTC().Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	return json.MarshalIndent(f, "", "  ")
}

// SaveVault stores data as account id's login, keeping the previous copy.
func SaveVault(id string, data []byte) error { return saveVault(id, data) }
