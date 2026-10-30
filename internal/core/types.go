package core

import "time"

const (
	KindClaude = "claude"
	KindCodex  = "codex"
	KindGemini = "gemini"
	KindGrok   = "grok"
	// KindAgy is the Antigravity CLI, named after its command.
	KindAgy = "agy"
)

// Kinds lists every official client the pool can schedule.
var Kinds = []string{KindClaude, KindCodex, KindGemini, KindGrok, KindAgy}

// Account is one subscription login in the pool. For Claude the login itself
// lives in the official keychain entry keyed by ConfigDir; GCC Code keeps no
// copy, and only the gateway's subscription routing reads (and, after a
// refresh, rewrites) that entry. For Codex the auth.json snapshot lives in the vault.
// Gemini and Grok each get an isolated config home (GEMINI_CONFIG_DIR / GROK_HOME).
// Antigravity keeps its login in one fixed entry of the user's keychain, so
// only the login on this machine joins; its ConfigDir is the HOME agy runs in.
type Account struct {
	ID               string    `json:"id"`
	Kind             string    `json:"kind"`
	Label            string    `json:"label"`
	Email            string    `json:"email,omitempty"`
	Plan             string    `json:"plan,omitempty"`
	RemoteID         string    `json:"remote_id,omitempty"`
	ConfigDir        string    `json:"config_dir,omitempty"`
	Proxy            string    `json:"proxy,omitempty"`
	Disabled         bool      `json:"disabled,omitempty"`
	Quarantined      bool      `json:"quarantined,omitempty"`
	QuarantineReason string    `json:"quarantine_reason,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	// TokenAt is when a Claude account's setup-token was saved. Set means the
	// account signs in with that token instead of a keychain login.
	TokenAt time.Time `json:"token_at,omitzero"`
}

// Window is one rate-limit window as reported by the official client.
type Window struct {
	UsedPct float64 `json:"used_pct"`
	ResetAt int64   `json:"reset_at,omitempty"`
}

// Quota is the latest known usage of one account.
type Quota struct {
	AccountID string    `json:"account_id"`
	FiveHour  *Window   `json:"five_hour,omitempty"`
	Weekly    *Window   `json:"weekly,omitempty"`
	PlanType  string    `json:"plan_type,omitempty"`
	Limited   bool      `json:"limited,omitempty"`
	Source    string    `json:"source,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Occupancy marks an account as in use by a running session.
type Occupancy struct {
	PID       int       `json:"pid"`
	SessionID string    `json:"session_id,omitempty"`
	Since     time.Time `json:"since"`
}

// Launch is the session one gcccode launch is running, as the status line
// last reported it from inside that launch.
type Launch struct {
	SessionID  string    `json:"session_id"`
	Transcript string    `json:"transcript,omitempty"`
	At         time.Time `json:"at"`
}

// State is the scheduler's runtime state.
type State struct {
	CodexCurrent string               `json:"codex_current,omitempty"`
	CodexSince   time.Time            `json:"codex_since,omitempty"`
	Occupied     map[string]Occupancy `json:"occupied,omitempty"`
	Cooldown     map[string]int64     `json:"cooldown,omitempty"`
	Notified     map[string]int64     `json:"notified,omitempty"`
	SessionOwner map[string]string    `json:"session_owner,omitempty"`
	Launches     map[string]Launch    `json:"launches,omitempty"`
}

func (s *State) ensure() {
	if s.Launches == nil {
		s.Launches = map[string]Launch{}
	}
	if s.Occupied == nil {
		s.Occupied = map[string]Occupancy{}
	}
	if s.Cooldown == nil {
		s.Cooldown = map[string]int64{}
	}
	if s.Notified == nil {
		s.Notified = map[string]int64{}
	}
	if s.SessionOwner == nil {
		s.SessionOwner = map[string]string{}
	}
}

// Relay is the fallback used once every subscription account is exhausted.
type Relay struct {
	Enabled      bool   `json:"enabled"`
	Name         string `json:"name,omitempty"`
	AnthropicURL string `json:"anthropic_url,omitempty"`
	OpenAIURL    string `json:"openai_url,omitempty"`
	Token        string `json:"token,omitempty"`
	BuyURL       string `json:"buy_url,omitempty"`
}

// DefaultGatewayAddr is where the optional gateway listens unless told
// otherwise. 3425 is Magpie's, so stay off it.
const DefaultGatewayAddr = "127.0.0.1:3426"

// GatewayProvider is one upstream the optional gateway can fail over to. The
// list order is the priority order.
type GatewayProvider struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	// Protocol is what the upstream speaks: "openai" (the default, Chat
	// Completions), "anthropic" (Messages) or "gemini" (generateContent).
	Protocol string `json:"protocol,omitempty"`
	// Responses marks an OpenAI upstream that also accepts /v1/responses.
	Responses bool   `json:"responses,omitempty"`
	Model     string `json:"model,omitempty"` // when set, replaces the request's model
	Disabled  bool   `json:"disabled,omitempty"`
	// Subscription makes this upstream the pool's own logins of that kind
	// ("codex" or "claude") instead of a URL and API key.
	Subscription string   `json:"subscription,omitempty"`
	Accounts     []string `json:"accounts,omitempty"` // empty: every account of that kind
}

// Gateway is the optional phase-2 local gateway. It is off unless the user
// runs `gcccode gateway start`. Pooled subscription logins reach it only
// through providers marked Subscription.
type Gateway struct {
	Addr      string            `json:"addr,omitempty"`
	Token     string            `json:"token,omitempty"` // local bearer token agents must present
	Providers []GatewayProvider `json:"providers,omitempty"`
	// Use lists the official clients `gcccode use-gateway on` sends through
	// the gateway instead of the account pool.
	Use []string `json:"use,omitempty"`
}

// Settings are the user's preferences.
type Settings struct {
	Strategy     string            `json:"strategy"`
	Manual       map[string]string `json:"manual,omitempty"`
	LimitPct     float64           `json:"limit_pct"`
	WarnPct      float64           `json:"warn_pct"`
	AutoContinue bool              `json:"auto_continue"`
	WebAddr      string            `json:"web_addr"`
	ClaudeHome   string            `json:"claude_home,omitempty"`
	CodexHome    string            `json:"codex_home,omitempty"`
	GeminiHome   string            `json:"gemini_home,omitempty"`
	GrokHome     string            `json:"grok_home,omitempty"`
	AgyHome      string            `json:"agy_home,omitempty"`
	ClaudeBin    string            `json:"claude_bin,omitempty"`
	CodexBin     string            `json:"codex_bin,omitempty"`
	GeminiBin    string            `json:"gemini_bin,omitempty"`
	GrokBin      string            `json:"grok_bin,omitempty"`
	AgyBin       string            `json:"agy_bin,omitempty"`
	Relay        Relay             `json:"relay"`
	Gateway      Gateway           `json:"gateway"`
	Upsell       bool              `json:"upsell"`
	// StatusLinePrev is the statusLine the user had before `gcccode
	// integrate`. It keeps running alongside gcccode's and comes back on
	// `gcccode integrate off`.
	StatusLinePrev map[string]any `json:"statusline_prev,omitempty"`
}
