# GCC Code

One pool for Claude, Codex, Gemini, Grok, and Antigravity. When one account hits its limit, the next one continues the same chat.

The official `claude`, `codex`, `gemini`, `grok`, and `agy` binaries send every request. GCC Code only decides which account starts, when to switch, and how the history is carried over. It does not rewrite system prompts, spoof client headers, or refresh tokens itself.

The command is `gcccode`, not `gcc`, so it does not collide with the compiler.

中文说明：[README.md](README.md)

## How this differs from magpie

[magpie](https://github.com/yetone/magpie) exposes a subscription to every other agent and runs a local protocol gateway for per-request failover. Anthropic treats a foreign system prompt as third-party traffic, so that design has to drive the real `claude` binary and bridge tools back in.

GCC Code's main path does not do that: the official client uses its own login, an account stays pinned for a whole session, never rotated per request, and a spent window stays idle until it resets.

There is also an optional gateway (`gcccode gateway`, see "Optional gateway" below), off by default and independent of the pool. It uses API-key upstreams you add and can, when you opt in, turn several pooled Codex Plus / Claude logins into rotating upstreams and translate a ChatGPT subscription for other agents — running several subscriptions this way risks a ban, your call.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/nearmetips/GCCcode/main/install.sh | sh
```

`--proxy` and `--mirror` are supported. The SHA-256 still comes from the GitHub release, not from the mirror.

```sh
make build    # menu bar, needs cgo
make cli      # -tags nogui, no cgo
```

Homebrew and Scoop formulas live in [packaging/](packaging/). goreleaser fills in the checksums at release time.

## Desktop app (macOS)

If you would rather not touch a terminal at all, download `GCC-Code-<version>.dmg` from [Releases](https://github.com/nearmetips/GCCcode/releases) and drag it into Applications. Or build it yourself:

```sh
make app                        # builds dist/GCC Code.app (universal arm64 + Intel)
scripts/build-app.sh --install  # also copies it to ~/Applications
scripts/build-app.sh --dmg      # packs dist/GCC-Code-<version>.dmg
```

Double-click `GCC Code.app` and you get a native window: pool / add account / settings on the left, each account's quota bars and status on the right. Adding a Claude account happens entirely inside the window: click Add, it opens that account's own browser, you sign in and authorize, and the token is stored for you, with no terminal and no copy-paste. One click under Settings → Command line tool installs the `gcccode` command into `~/.local/bin`. Dark mode follows the system.

The same binary opens the window from a terminal with `gcccode app`. Linux, Windows and `-tags nogui` builds have no native window, so `gcccode app` falls back to the panel in your browser. A locally built .app is only ad-hoc signed; if macOS blocks the first launch, right-click → Open once.

## Use

With the command-line install, run one command:

```sh
gcccode web
```

The panel opens in your browser, same content as the desktop app. It brings the logins already on this machine into the pool with one click; adding or re-signing an account opens a terminal window and you sign in in the browser; a Claude account can also open its own browser or take a new token; removing an account, setting its proxy, choosing the strategy, and switching auto-continue and the Claude Code status line are all buttons. The panel listens on loopback only and refuses requests sent by other websites.

The same things as commands:

```sh
gcccode account add claude   # authorize in this account's own browser, get a one-year token
gcccode account add codex
gcccode account add gemini
gcccode account add grok
gcccode account add agy   # Antigravity: only the login already on this machine; signs in first if there is none
gcccode account import
gcccode accounts

gcccode claude
gcccode codex
gcccode gemini
gcccode grok
gcccode agy
gcccode use codex work
gcccode sessions     # recent sessions here: account and first request
gcccode resume       # newest session here, on an account with quota; or pass an id

gcccode integrate
eval "$(gcccode shell-init)"   # claude / codex / gemini / grok / agy all go through the pool
gcccode app          # the panel as a desktop window (macOS)
gcccode web          # opens the panel in the browser, 127.0.0.1:4725 by default; --no-open only serves it
gcccode tray
```

### Adding Claude subscriptions

Each Claude account signs in with its own long-lived token, and switching accounts means switching tokens:

1. Run `gcccode account add claude` (or press "添加一个号" in the panel). If you already have Claude accounts, it first asks which proxy this one uses.
2. gcccode runs the official `claude setup-token` and opens its authorization page only in **this account's own browser**: a separate Chrome (or Edge, Brave, Chromium) user data directory that shares no login with your everyday browser or with other accounts, and uses this account's proxy. Sign in to the Pro account there and authorize.
3. The terminal prints a token starting with `sk-ant-oat01-`, valid for about a year. Paste it back into the terminal and the account is added.

Why a browser of its own: the authorization page authorizes whichever account the browser is already signed in to. Authorizing account B in a browser signed in to A hands back A's token. If you sign in with an email link, copy the link into this account's browser instead of clicking it. Without a Chromium-based browser, gcccode asks you to create a new Chrome profile and open the authorization link there.

From then on, whichever account `gcccode claude` picks, its token is handed to Claude Code as `CLAUDE_CODE_OAUTH_TOKEN`, and a variable of the same name exported in your shell is ignored. So **do not** put the token in `.zshrc`. Tokens are stored in `~/.config/gcccode/claude-token/`, readable only by you.

```sh
gcccode account browser work         # open this account's browser again, e.g. to check usage on claude.ai
gcccode account login work           # renew: run setup-token again
gcccode account token work -         # already have a token: read it from stdin
gcccode account add claude --login   # full sign-in instead (kept in the keychain)
```

`gcccode accounts`, `doctor` and the panel show the days a token has left and remind you to renew within 30 days; an expired account is never picked. Removing an account deletes its local token and browser; revoke the token itself in claude.ai settings.

A long-lived token is limited to inference: quota tracking and the status line work, but Claude in Chrome and Remote Control do not. Add accounts that need those with `--login`.

Strategies are `smart` (spend the allowance that resets soonest), `pace` (weekly remainder per hour until reset), and `manual` (never substitute). Both `smart` and `pace` put an account within 10 points of the limit last, so a new session does not have to switch accounts minutes after it starts. `gcccode continue on` resumes on the next account after you leave a spent session; the default asks first. When every account is spent, the launcher, `gcccode accounts`, the status line and the panel name the account that comes back first and when.

`gcccode integrate` keeps the status line you already had: it still shows, with GCC Code's line below it, and `gcccode integrate off` puts it back. `shell-init` only takes over commands that start a conversation. Management commands such as `claude mcp`, `claude update`, `claude --version`, `codex login`, `gemini auth`, `grok login`, and `agy models` go to the official client untouched, and servers that `claude mcp add` writes to the main config reach every account.

## History survives a switch

Each Claude account gets its own `CLAUDE_CONFIG_DIR`. A long-lived token account only has its token handed to Claude Code at launch. For an account added with `--login`, Claude Code stores the login in a keychain item hashed from the directory, and GCC Code never reads it. `projects/`, `history.jsonl`, `CLAUDE.md`, skills and commands are symlinks to `~/.claude`, so the local transcript stays.

A second Claude subscription does not resume the first account's cloud session, which is how the vendor groups the two logins. The switch writes a local handoff and starts a new session. `--resume` is only used on the account that started the session. The handoff carries the original request (kept across repeated handoffs), the latest todo list, files the session edited, uncommitted `git status`, answers you gave to the agent's questions, and the recent turns, without rate-limit errors, injected client context, or subagent chatter.

`gcccode resume <id>`, `claude -c`, and `claude --resume <id>` go back to the account that started the session, in the directory it started in, and fall back to a handoff only when that account is unavailable. With several sessions open at once, each launch is matched to its own session through the status line, never to a neighbour's.

`machineID` and `userID` are not copied between profiles. A duplicated pair is cleared on the newer profile so Claude Code mints its own on next launch. Two or more Claude accounts must each have a distinct egress proxy; `gcccode claude` refuses to start until they do. The proxy must be `http://` or `https://`. Claude Code does not support SOCKS and would silently leave from the machine's own address, so `socks5://` is refused. gcccode can only compare addresses: two local ports still need to reach different exit nodes.

`settings.json` is copied, not linked, and relay, API-key and proxy variables are removed. Relay keys would bypass the subscription login; a shared `HTTPS_PROXY` would override every account's own proxy. An account without its own proxy still gets the one from `settings.json`.

Codex keeps threads in SQLite inside one `~/.codex`, so accounts share that directory and only `auth.json` is swapped, atomically. A switch is refused while Codex is running, because it writes the old token back on exit. The latest refresh token is saved into the vault first; an older live file cannot overwrite a newer one.

If the next account rejects the previous account's encrypted reasoning, GCC Code writes a handoff note from the local transcript and starts a fresh session with it. The old session stays on disk.

Gemini accounts each get a `GEMINI_CONFIG_DIR` (Claude-style isolation). Grok accounts each get a `GROK_HOME` so auth and sessions travel with the profile and accounts can run in parallel; skills/plugins link back to the main `~/.grok`. Cross-account resume uses a local handoff for both.

Antigravity (`agy`) takes only the login already on this machine for now. agy keeps its login in one fixed keychain entry that stays the same whatever config dir or HOME it runs with, so accounts cannot get a directory each the way Claude's do: signing in a second account would replace the first. `gcccode account add agy` therefore refuses when the pool already has an agy account. To switch Google accounts, run `gcccode account login <id>`, then `/logout` inside agy and sign in again.

`gcccode sessions` reads agy's own conversation index for titles and directories (needs `sqlite3` on the system). `gcccode resume <id>`, `agy --conversation <id>`, and `agy -c` resume a conversation. agy stores conversations in a binary format, so there is no handoff: when the quota runs out, the account cools down and GCC Code prints the command to resume once it resets. Antigravity signs in with Google only and does not go through the gateway. On macOS the keychain tells whether agy is signed in; on Linux and Windows a sign-in recorded in agy's log stands in, which is untested so far.

## Quota

Nothing extra is sent to the vendors. Claude Code's status-line JSON includes `rate_limits`, and Codex rollout files include `rate_limits` on `token_count` events. Gemini and Grok keep selecting with unknown quota until a limit or login error appears in the local transcript. Antigravity's log is read after agy exits; only error-level lines such as `RESOURCE_EXHAUSTED` or `quota reached` count, not server overload or image-generation limits. Near the limit you get one notification; at the limit the account cools down until the window resets.

## Usage page

The panel's "用量" (usage) page shows how many tokens each account actually used: today / 7 / 30 / 90 days, a daily chart coloured by client, then per account, per model, and the most recent sessions. Everything comes from the transcripts the official clients keep on this machine; nothing is requested or uploaded:

- Claude Code: the session jsonl files under `~/.claude/projects/` (`usage` input / output / cache read / cache write)
- Codex: rollout files under `~/.codex/sessions/` and `archived_sessions/` (deltas of the cumulative `token_count` events)
- Grok CLI: `sessions/*/updates.jsonl` `turn_completed` events in each account's home
- Gemini CLI: per-message `tokens` in `tmp/*/chats/session-*.json`
- Antigravity: keeps no token counts locally, so it cannot be counted; the page says so

A session is attributed to the account GCC Code launched it with; Codex sessions can also be matched by the account id in the file. Sessions started with the vendor's own command cannot be matched and are listed as "unattributed" rather than guessed. Scanning is incremental — each file is read from where it was left off — and cached in `~/.config/gcccode/usage-cache.json`; 1.4 GB of Codex transcripts take under a second the first time and a few milliseconds afterwards.

## Optional gateway (off by default)

A second path next to the pool: a loopback-only local gateway that accepts OpenAI, Anthropic and Gemini requests and forwards them to API-key upstreams you paste in, with **per-request failover**. The list order is the priority order. Before the first byte reaches the agent, a connection failure, 401/403, 402, 429, 5xx or an out-of-credit error moves the request to the next upstream, and the agent never sees the failed attempt.

```sh
gcccode gateway provider add claude   --protocol anthropic --url https://api.anthropic.com --key sk-ant-...
gcccode gateway provider add deepseek --url https://api.deepseek.com/v1 --key sk-... --model deepseek-chat
gcccode gateway provider add openai   --responses --url https://api.openai.com/v1 --key sk-...
gcccode gateway provider add gemini   --protocol gemini --url https://generativelanguage.googleapis.com --key AIza...
gcccode gateway provider add codexpool --subscription codex          # rotate across several Codex Plus logins
gcccode gateway provider add claudepool --subscription claude --accounts cl-1,cl-2
gcccode use-gateway on                 # gcccode claude / codex / gemini go through the gateway (started when needed)
gcccode use-gateway off                # back to official-direct on the pool
eval "$(gcccode gateway env)"          # for other agents: OPENAI_* / ANTHROPIC_* / GOOGLE_GEMINI_* for your upstreams
gcccode gateway status                 # per-upstream successes, failures, cooldown end
gcccode gateway start | stop           # manual control, 127.0.0.1:3426 by default
```

- Base URLs follow each SDK: openai includes `/v1`; anthropic and gemini leave the version path off.
- Claude Code (`ANTHROPIC_BASE_URL`): anthropic upstreams get the request unchanged; openai upstreams get Anthropic Messages **translated** to Chat Completions and back, including streaming, tool calls and images. In the example above, when the Anthropic API runs out of credit the request moves to DeepSeek through translation. Give an openai upstream `--model` if it doesn't recognise `claude-*` model names.
- Codex speaks the Responses API and reaches openai upstreams marked `--responses`, or a `--subscription codex` pool. Use `use-gateway on codex` rather than only setting `OPENAI_BASE_URL`, which a Codex signed in with ChatGPT may keep ignoring.
- Gemini CLI (`GOOGLE_GEMINI_BASE_URL`) reaches gemini upstreams, and only when it authenticates with an API key.
- Cooldowns: a 429 honours `Retry-After` (60 s without one); quota or balance 429s, 400s and 402 park the upstream for 30 minutes; 401/403 for an hour; 5xx and connection failures for 30 seconds. Request-shaped errors such as 400, 404 and 422 go back unchanged and are not retried.
- Local safety: loopback only, `Host` is checked, and the agent must present the local token generated on first use (`gateway env` prints it). Upstream keys live only in gcccode's `settings.json`; the local token never goes upstream and `status` never prints a key.
- Gateway or official-direct, not both, and `use-gateway` decides. When it is on, `gcccode claude` / `codex` / `gemini` (and the shell-init wrappers of the same names) skip account selection and switching and connect to the gateway; `--account` still goes to the pool. Only gcccode's own settings change, never a client's config file: Claude and Gemini run on gateway-only config dirs that hold no login (Claude still shares history, skills and MCP), and Codex gets a provider defined on its command line, leaving `~/.codex/auth.json` alone. When it is off, these commands drop gateway variables from the shell and stay official-direct. `gcccode doctor` shows the current state.
- **Subscription routing (several Plus / Pro logins):** `--subscription codex｜claude` turns the pool's own logged-in subscriptions into upstreams; the gateway picks an account, rotates on quota, and refreshes and writes tokens back where each client keeps them — this is how you combine several Codex Plus plans into one route (the CPA/magpie multi-account rotation, built in). Codex switches live on exhaustion; a Claude conversation stays on one account and, once spent, continues in a new session via a handoff file, and **each Claude account needs its own proxy**. A ChatGPT subscription can also be translated for other agents. Running several subscriptions this way may breach the vendor terms and risk a ban — your call; details in [docs/gateway.md](docs/gateway.md).

## Relay fallback

When every subscription is unavailable, GCC Code can point the official client at a relay such as a [gcccode.com](https://gcccode.com/) plan. That is a normal API configuration, not a fake subscription login.

```sh
gcccode relay set --anthropic https://your-relay.example --openai https://your-relay.example/v1 --token sk-...
gcccode claude --relay
```

The Codex key stays in the child environment. `config.toml` is restored afterward, and is left alone if you edited it during the session. Set `upsell` to `false` in the settings file to hide the buy link. `gcccode account proxy <id> <url>` pins an egress proxy to one account. A disabled organization or a dead login quarantines that account.

## Computer use (LCU)

[LCU](https://github.com/amontlabs/lcu) takes Codex's computer use out of the ChatGPT desktop app and serves it to Claude Code and Codex CLI as an MCP server: read windows, click, type, take screenshots, and with the Chrome option, drive tabs.

LCU registers only in the default `~/.claude` and refuses to set up when `CLAUDE_CONFIG_DIR` is set. Every pooled Claude account has its own directory, so installing LCU from inside a GCC Code session fails. Install it through GCC Code instead:

```sh
gcccode lcu install            # download, verify, install, then register with Claude Code and Codex
gcccode lcu                    # where it is and which pooled accounts carry it
gcccode lcu doctor             # any other subcommand goes to lcu as is: doctor, status, apps, update
```

Arguments after `install` or `setup` go to `lcu setup`, for example `--chrome`, `--audio`, or `--session direct` on Linux. The download takes `--proxy` and `--mirror`; the checksum still comes from GitHub.

The registration lives in the main home: the `lcu` server in `~/.claude.json`, lifecycle hooks and permission rules in `settings.json`, and the `skills/lcu-approve` approval mod. When an account starts, GCC Code syncs the first two into that account's directory, and the mod comes along through the `skills/` symlink. An MCP server removed from the main config is removed from the account too. Relay sessions get only LCU's entries on top of the relay. Codex accounts already share `~/.codex`.

Prerequisites are LCU's own: an Apple Silicon Mac or Linux with an X11 desktop, the official ChatGPT desktop app (no sign-in needed), and Python 3.12+. On Linux the runtime install needs root, so GCC Code only downloads and verifies the release, then prints the `sudo` command for you to run.

## Risk

Nothing here can promise an account will not be banned. The vendor terms restrict sharing. This tool is for your own subscriptions: one account per session, official login, and no attempt to punch through a limit.

## What was checked

Against Claude Code 2.1.143: `CLAUDE_CONFIG_DIR` changes the keychain item to `Claude Code-credentials-` plus the first 8 hex chars of SHA-256 of that directory, and the status line receives 5-hour and 7-day limits.

Against local Codex rollouts: `primary` is the 5-hour window, `secondary` is the weekly window, with `plan_type`.

Tests cover profile symlinks, parallel sessions matched to their own launch, resume returning to the session's directory and account, management commands skipping the pool, an existing status line kept and restored, local-scope MCP servers reaching accounts, relay settings stripped from subscription profiles, auth swap and token write-back, stale-token refusal, quota parsing, handoff content and noise filtering for both transcript formats, the original goal surviving repeated handoffs, all three strategies with nearly spent accounts last, the earliest-recovery notice, reset times read from limit messages (cooling down only the window that ran out), `sessions` merging both clients and skipping Codex subagent threads, SOCKS and shared `settings.json` proxies being refused, LCU's registration reaching account and relay directories, MCP servers removed from the main config leaving accounts, and release checksum verification.

A second real login is still required to watch cross-account `claude --resume`, a live encrypted-reasoning rejection, and whether the Codex desktop app must be restarted. The code assumes the desktop app must be reopened, and falls back to a handoff file when resume is rejected.

For LCU, downloading and verifying 0.9.5 from GitHub ran for real; registration and sync were tested against a fake `lcu` that behaves like the real one. A real screenshot from a pooled Claude session has not been run yet.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
