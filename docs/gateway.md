# 可选网关（二期）设计与范围

一期的主路径不变：官方 `claude` / `codex` / `gemini` / `grok` / `agy` 自己发请求，gcccode 只管选号、换号、接力、额度。
网关是**另一条、默认关闭**的路径，两者互不替换。

## 做了什么

| 项 | 状态 |
|----|------|
| 命令 | `gcccode gateway start｜stop｜status｜serve｜env`、`gateway provider add｜rm｜enable｜disable｜list` |
| 上游 | 两类：① 用户自己的 API Key，三种协议：`openai`（默认，地址带 `/v1`）、`anthropic`（地址不带 `/v1`）、`gemini`（地址不带 `/v1beta`）；② **订阅号池**（`--subscription codex｜claude`），直接用你登录的 Codex Plus / Claude 会员轮换，无需 Key。列表顺序就是优先级 |
| 订阅路由 | 见下文「订阅路由」。网关自己选号、换号、按额度冷却、刷新并写回各家原本存 token 的地方，并把 ChatGPT 订阅翻译给其他 Agent 用 |
| OpenAI 入站 | `POST /v1/chat/completions`、`GET /v1/models` → openai 上游；`POST /v1/responses` → 标了 `--responses` 的 openai 上游 |
| Anthropic 入站 | `POST /v1/messages`：anthropic 上游原样转发（带上 `anthropic-version` / `anthropic-beta`）；openai 上游**翻译**成 Chat Completions 再翻译回来，含 SSE 流式、工具调用、图片 |
| Gemini 入站 | `/v1beta/...` → gemini 上游原样转发；`?key=` 里的本机 token 会被去掉，上游 key 走 `x-goog-api-key` |
| `count_tokens` | 有 anthropic 上游就转发；只有 openai 上游时返回按字节数粗估的值，不报错 |
| 请求级 failover | 首字节返回 Agent 之前：连接失败、401/403、402、429、5xx、余额/额度类 400 换下一个上游（可以跨协议，比如 anthropic 上游没钱了换到翻译后的 openai 上游）；其余 4xx 原样返回，不重试 |
| 冷却 | 429 读 `Retry-After`（默认 60s）；额度/余额类 429、400 与 402 冷却 30 分钟；401/403 冷却 1 小时；5xx 与连接失败 30 秒 |
| 错误形状 | 网关自己的错误按 Agent 说的协议返回（OpenAI / Anthropic / Gemini 各自的 error JSON），翻译路径上的上游错误也会转成 Anthropic 形状 |
| 本机安全 | 只监听 loopback；校验 `Host`（防 DNS rebinding）；本机 token（首次使用自动生成，只存 gcccode），接受 `Authorization: Bearer`、`x-api-key`、`x-goog-api-key`、`?key=`；上游 Key 不会出现在 `status` 输出里，也不会把本机 token 发给上游 |
| 接入方式 | 两种：`gcccode use-gateway on` 让 gcccode 启动的官方客户端改走网关（见下文）；`gcccode gateway env` 按已配置的上游打印 `OPENAI_BASE_URL`/`OPENAI_API_KEY`、`ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`、`GOOGLE_GEMINI_BASE_URL`/`GEMINI_API_KEY`，由用户 `eval` 到当前 shell，给其他 Agent 用 |
| doctor | 显示网关状态、哪些官方客户端开了 use-gateway（以及开了但没有合适上游、启动会失败的）；当前 shell 的三个 base URL 变量里有指向网关的，就提示直接运行的 Agent 已绕开号池 |

### 各客户端需要的上游（直接运行、用 `gateway env` 的变量时）

| 客户端 | 变量 | 需要的上游 |
|--------|------|-----------|
| Claude Code | `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` | anthropic 上游、openai 上游（翻译；上游不认 `claude-*` 模型名时给它设 `--model`），或 `--subscription claude` 订阅号池 |
| Codex | 建议用 `use-gateway on codex`，见下文；已登录 ChatGPT 的 Codex 只设 `OPENAI_BASE_URL` 时可能仍用 ChatGPT 登录 | 标了 `--responses` 的 openai 上游，或 `--subscription codex` 订阅号池 |
| Gemini CLI | `GOOGLE_GEMINI_BASE_URL` + `GEMINI_API_KEY` | gemini 上游；Gemini CLI 要用 API Key 方式认证，用 Google 账号登录时它不走这个变量 |
| 其他 OpenAI 兼容 Agent | `OPENAI_BASE_URL` + `OPENAI_API_KEY` | openai 上游 |

## use-gateway：官方客户端走网关

```sh
gcccode use-gateway on              # 所有接得上的客户端；也可以写 on claude gemini
gcccode use-gateway status
gcccode use-gateway off             # 全部恢复直连官方；也可以写 off codex
```

- 只改 gcccode 自己的 `settings.json`（`gateway.use`），不改任何客户端的配置文件。`on` 时网关没在运行就顺手启动；
  之后 `gcccode claude` 等发现网关停了也会自己拉起来。
- 打开后 `gcccode claude` / `codex` / `gemini`（以及 shell-init 包出来的同名命令）**不选号、不换号、不续接**：
  限流、余额由网关在上游之间切换。用 `--account` 指定账号时仍走号池。
- 打开前会检查有没有合适的上游：Claude Code 要 anthropic / openai 上游或 claude 订阅号池；Codex 要标了 `--responses` 的 openai 上游或 codex 订阅号池；
  Gemini CLI 要 gemini 上游。Grok 不支持；Antigravity 只用 Google 账号登录，也不支持。
- 各客户端怎么接：

| 客户端 | 配置目录 | 怎么指向网关 |
|--------|----------|-------------|
| Claude Code | 网关专用的 `CLAUDE_CONFIG_DIR`（`<gcccode 目录>/gateway/claude`），历史、skills、MCP 照样软链回主目录，不存任何登录 | `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`；首次启动跳过引导页 |
| Codex | 共用 `~/.codex`，不换 `auth.json` | 命令行 `-c` 临时定义一个 `gcccode` provider（`wire_api="responses"`，key 从 `GCCCODE_GATEWAY_KEY` 读），ChatGPT 登录既不用也不会发给网关；什么都不写进 `~/.codex` |
| Gemini CLI | 网关专用的 `GEMINI_CONFIG_DIR`（`<gcccode 目录>/gateway/gemini`），认证方式设成 `gemini-api-key` | `GOOGLE_GEMINI_BASE_URL` + `GEMINI_API_KEY` |

- 都会把 `127.0.0.1,localhost,::1` 加进 `NO_PROXY`，本机请求不绕代理。
- 续接：Claude 会话属于号池里某个订阅账号时，不在网关上接着它，而是写一份交接记录开新会话（和换号时一样）；
  没有归属的会话直接 `--resume`。

## 订阅路由（多个 Plus / Pro 号）

把号池里已登录的订阅号做成上游，解决「GPT 没有甜点位套餐、想拿多个 Plus 并起来路由」这类需求（相当于把 CPA/magpie 的多号轮换内建进来）。**官方客户端照常自己发请求，网关只替换这一条请求用的登录凭证，不改写请求本身。**

```sh
gcccode gateway provider add codexpool --subscription codex          # 覆盖号池里所有 Codex 账号
gcccode gateway provider add claudepool --subscription claude --accounts cl-1,cl-2
gcccode use-gateway on codex        # 让 gcccode codex 走网关、自动轮换
```

- **Codex Plus**：`POST /v1/responses` 转发到 `https://chatgpt.com/backend-api/codex/responses`，带上该账号的 `Authorization` 和 `chatgpt-account-id`，原样透传 Codex 的请求头。额度用完（429 / `usage_limit_reached`）或 401（刷新后仍失败）**当场换下一个号**；换号时会去掉输入里的加密 `reasoning` 项（它只在原账号可验证）。
- **Claude Pro**：只接 Claude Code（按 `claude-cli` UA 识别），转发到 `https://api.anthropic.com/v1/messages?beta=true`，加 `oauth-2025-04-20`、去掉 `x-api-key`，并把 `metadata.user_id` 里的 `device_id` / `account_uuid` 改成该号自己的。**进行中的会话绑定在同一个号上**（换号会被 Anthropic 判成异常）；额度用完返回 429，`gcccode claude` 退出后按 AutoContinue/确认，写交接文件用另一个号**新开会话**续接。
- **多个 Claude 号必须各自设代理**（`gcccode account proxy <id> <url>`），否则同一出口会被判成一组，添加时就会拒绝。
- **ChatGPT 订阅给其他 Agent**：非 Codex 的 Agent 发来的 `POST /v1/chat/completions` 或 `POST /v1/messages`，翻译成 Responses 再走 Codex 号池（流式、工具调用、图片），回复翻译回对应协议。这条路径用网关自己的身份，不冒充 Codex 客户端。
- **令牌归属**：Codex 以 `~/.codex/auth.json`（当前登录的就是该号时）或账号库里的快照为准，刷新后写回账号库，必要时也写回 `~/.codex`（Codex 刷新前会重读，避免用到作废的 refresh token）；Claude 用长期 token 的号（`claude setup-token` 拿到的，约一年有效）直接带上这个 token，从不刷新，上游拒绝时标成「需要重新登录」，提示用 `gcccode account login <id>` 续期；用 `--login` 加的号每次用前重读钥匙串 / `.credentials.json`，临近过期才刷新并写回；号池里正跑着该号的会话时，交给那个客户端自己续期，网关不并发刷新。快到期前（5 分钟）才刷新，刷新走该号自己的代理出口。
- 选号用号池的调度策略（smart / pace / manual）；每个号单独冷却、单独额度；`gateway status` 按账号显示已用百分比、成功/失败、冷却与「需要重新登录」。
- **风险**：多开、超量轮换订阅号可能触犯 OpenAI / Anthropic 的服务条款，账号有被封风险，自行判断。添加订阅上游时会打印这条提示。

## 和号池互斥

- API Key 上游不读也不改号池里任何订阅账号、不动 `~/.codex` 里的登录和各账号的 `CLAUDE_CONFIG_DIR`；只有上面的订阅上游才会用（并在刷新后写回）这些登录。
- 没开 use-gateway 时，`gcccode claude` / `codex` / `gemini` 会去掉指向网关的 base URL 变量，**直连官方、走号池**。
  Claude 与 Gemini 本来就会清掉这些变量；Codex 只清指向本机网关的 `OPENAI_BASE_URL` / `OPENAI_API_KEY`，用户自己设的其他中转不动。
- `gateway env` 是给其他 Agent 的；它最后一行会打印对应的 `unset ...`。

## 没做什么（以及为什么）

- **把 Claude 订阅给非 Claude Code 的 Agent**：按产品决定先不做（只有 Claude Code 能骑 Claude 订阅）；其他 Agent 想用订阅走 ChatGPT（Codex）那条翻译路径。
- **Gemini / Grok 订阅路由**：只做了 Codex 与 Claude；Gemini 入站仍只转发给 gemini API Key 上游。
- **按回合智能路由模型**（Magpie 的 turn classifier）：不在范围内。
- **流中途失败的 failover**：响应头一旦发给 Agent 就不能再换上游，这是“首字节前 failover”的固有边界；
  翻译路径上，上游流中途出错会变成对应协议的 `error` 事件。

## 待真实账号验证

- Codex Plus、Claude Pro 的转发已用**真实 Codex / Claude Code 二进制**对着假上游端到端跑通（凭证替换、请求头、SSE 透传、额度写入、会话绑定都验证过）。
- 对接**真实** chatgpt.com / api.anthropic.com 时的请求头细节（尤其 Codex 自定义 provider 下透传的头是否被后端全盘接受），需要用真实账号各发一条短请求冒烟确认；可用 `GCCCODE_GATEWAY_TEST_CODEX_URL` / `GCCCODE_GATEWAY_TEST_CLAUDE_URL`（仅限 loopback）指向本地桩来复现。
