# GCC Code

Claude / Codex / Gemini / Grok / Antigravity 五个官方客户端，当成一个号池来用。一个号额度用完，换下一个接着做，聊天记录还在。

请求只由官方的 `claude`、`codex`、`gemini`、`grok`、`agy` 发出。GCC Code 做三件事：这次用哪个号、什么时候换、换号之后历史怎么接上。它不改系统提示，不伪造请求头，也不自己去刷新 token。

命令叫 `gcccode`，不用 `gcc`，免得和系统里的编译器撞名。

English: [README.en.md](README.en.md)

 

GCC Code 主路径不走那条路：官方客户端只用它自己的登录，号按会话固定，不按请求轮换；额度到了就停到重置时间，不硬顶。

另有一个可选、默认关闭的本机网关（`gcccode gateway`，见下文「可选网关」），与号池并行，不替换「官方客户端发请求」的主路径。它接你自己的 API Key 上游，也能（需显式开启）把号池里多个 Codex Plus / Claude 会员做成轮换上游，并把 ChatGPT 订阅翻译给其他 Agent——多开订阅号有封号风险，自行判断。

## 安装

```sh
curl -fsSL https://raw.githubusercontent.com/nearmetips/GCCcode/main/install.sh | sh
```

墙内可以加代理和镜像。校验和始终从发布页取，不从镜像取。

```sh
curl -fsSL https://raw.githubusercontent.com/nearmetips/GCCcode/main/install.sh | sh -s -- \
  --proxy http://127.0.0.1:7890 --mirror https://mirror.example/github.com
```

也可以自己编译。带菜单栏的构建需要 cgo；`-tags nogui` 是纯命令行。

```sh
make build    # 菜单栏
make cli      # 纯命令行
```

Homebrew 和 Scoop 的配方在 [packaging/](packaging/)。发布时由 goreleaser 填上校验和。

## 桌面应用（macOS）

不想碰终端的话，直接用桌面版：到 [Releases](https://github.com/nearmetips/GCCcode/releases) 下载 `GCC-Code-<版本>.dmg`，拖进「应用程序」即可。也可以自己打包：

```sh
make app                        # 生成 dist/GCC Code.app（arm64 + Intel 通用）
scripts/build-app.sh --install  # 顺手装进 ~/Applications
scripts/build-app.sh --dmg      # 打成 dist/GCC-Code-<版本>.dmg
```

双击 `GCC Code.app` 就是一个原生窗口：左边号池 / 添加账号 / 设置，右边是每个号的额度条和状态。加 Claude 号整个流程都在窗口里走完——点「添加」，它打开这个号的专用浏览器，你登录、点授权，token 自动收好，不用开终端也不用复制粘贴。在「设置 → 命令行工具」点一下，就把 `gcccode` 命令装到 `~/.local/bin`，终端里也能用。深色模式跟随系统。

同一个二进制也能从终端起窗口：`gcccode app`。Linux、Windows 和 `-tags nogui` 的构建没有原生窗口，`gcccode app` 会退回到在浏览器里打开面板。本地编译的 .app 只做了自签名，第一次打开如果被拦，右键 → 打开一次即可。

## 使用

装的是命令行的话，只敲一句：

```sh
gcccode web
```

浏览器会打开本机面板，内容和桌面版一样。这台电脑上已经登录的号一键放进号池；加新号、重新登录会弹出一个终端窗口，在浏览器里登录就行；Claude 号还能打开它的专用浏览器、粘贴新 token；移除、给号设代理、换选号方式、开关自动续接和 Claude Code 状态栏都能点；「用量」页看每个号实际用了多少 token。面板只听本机地址，别的网站发来的请求一律拒绝。

下面是对应的命令：

```sh
gcccode account add claude     # 在这个号专用的浏览器里授权，拿一年期 token
gcccode account add codex
gcccode account add gemini
gcccode account add grok
gcccode account add agy        # Antigravity：只收本机已登录的这一个号，没登录就先带你登录
gcccode account import         # 把这台机器上已经登录的号收进来
gcccode accounts               # 每个号的额度、重置时间、状态

gcccode claude                 # 自动选一个有额度的号，启动官方 Claude Code
gcccode codex
gcccode gemini
gcccode grok
gcccode agy
gcccode use codex 工作号        # 立刻换成这个 ChatGPT 号（桌面版要重新打开）
gcccode sessions               # 这个目录最近的会话：开在哪个号上、第一句说了什么
gcccode resume                 # 在还有额度的号上续接这个目录最近的会话；也可以写会话 id

gcccode integrate              # 额度进 Claude Code 状态栏，原来的状态栏照常显示；integrate off 撤销
eval "$(gcccode shell-init)"   # 之后直接敲 claude / codex / gemini / grok / agy 也走号池
gcccode app                    # 桌面窗口版面板（macOS）
gcccode web                    # 本机面板，自动打开浏览器，默认 127.0.0.1:4725；--no-open 只启动不打开
gcccode tray                   # 菜单栏
```

### 加 Claude 会员号

每个 Claude 号都用自己的长期 token 登录，换号就是换 token：

1. `gcccode account add claude`（或面板里点「添加一个号」）。有别的 Claude 号时，会先问这个号走哪个代理。
2. gcccode 运行官方的 `claude setup-token`，授权页只在**这个号专用的浏览器**里打开。这是一个单独的 Chrome（也支持 Edge、Brave、Chromium）用户目录，和你平时用的浏览器、和其他号的浏览器都不共用登录状态，也走这个号的代理。在里面登录这个 Pro 号，点授权。
3. 终端会打印一串 `sk-ant-oat01-` 开头的 token，有效期约一年。把它粘贴回终端就加好了。

为什么要专用浏览器：授权页认的是浏览器里已经登录的那个号。在已登录 A 号的浏览器里给 B 号授权，拿到的其实是 A 号的 token。用邮箱验证码登录的话，邮件里的链接要复制到这个专用浏览器里打开，别直接点。没装 Chromium 系浏览器时，gcccode 会让你在 Chrome 里新建一个个人资料，把授权链接粘贴到那里打开。

之后 `gcccode claude` 选中哪个号，就把哪个号的 token 设成 `CLAUDE_CODE_OAUTH_TOKEN` 交给 Claude Code，shell 里自己 export 的同名变量会被忽略。所以**不要**把 token 写进 `.zshrc`。token 存在 `~/.config/gcccode/claude-token/`，只有你自己能读。

```sh
gcccode account browser 工作号        # 再打开这个号的专用浏览器，比如去 claude.ai 看用量
gcccode account login 工作号          # 续期：重新走一遍 setup-token
gcccode account token 工作号 -        # 已经有 token 了，从标准输入换上
gcccode account add claude --login    # 改用完整登录（存在钥匙串里）
```

`gcccode accounts`、`doctor` 和面板会显示 token 还剩几天，剩 30 天以内会提醒续期；过期的号不会被选中。移除号时，本机的 token 和专用浏览器一起删掉，token 本身要到 claude.ai 的设置里撤销。

长期 token 只有对话权限：额度照常统计、状态栏照常显示，但 Claude in Chrome 和 Remote Control 用不了。需要这两个功能的号，用 `--login` 加。

状态栏一行类似：`工作号 · 5h 42% · 15:00 重置 · 周 18% · 下一个 备用号`

`shell-init` 只接管开会话的命令。`claude mcp`、`claude update`、`claude --version`、`codex login`、`gemini auth`、`grok login`、`agy models` 这类管理命令照旧交给官方客户端，不选号；`claude mcp add` 加到主目录的服务器会同步进每个号。

选号策略：

- `smart`（默认）：还有余量的号里，先用最快重置的那个，免得额度重置时被清掉
- `pace`：按「每周剩余额度 ÷ 距离重置的小时数」排序
- `manual`：只用 `gcccode use` 指定的号，不可用就停，不会偷偷换

`smart` 和 `pace` 都会把离限额不到 10 个百分点的号排到最后：新会话开上去几分钟就得换号，Claude 换号只能靠接力文件，不如一开始就开在余量充足的号上。

```sh
gcccode strategy pace
gcccode continue on     # 额度用完并退出后，自动在下一个号上续接。默认会先问一句
```

所有号都用完时，`gcccode claude`（以及 `codex` / `gemini` / `grok` / `agy`）、`gcccode accounts`、状态栏和面板都会告诉你哪个号最早恢复、几点、还要等多久。会话不会丢，恢复后 `gcccode resume` 接着做。

## 换号为什么不丢记录

Claude 每个号一个 `CLAUDE_CONFIG_DIR`。长期 token 号只在启动时把 token 交给 Claude Code。用 `--login` 加的号，登录存在这个目录对应的钥匙串条目里，GCC Code 不读、不存。`projects/`、`history.jsonl`、`CLAUDE.md`、skills、commands 都软链接回你的 `~/.claude`，本地会话还在。

两个 Claude 会员之间不续同一个云端会话。那样官方会把两个号看成一组。换号时用本地记录生成接力文件，在下一个号上开新会话接着做。同一个号自己的会话仍然 `--resume`。

接力文件里有：任务目标（第一句真实需求，连续接力也不会丢）、最后一次更新的待办、这个会话改过的文件、`git status` 里没提交的改动、你在提问里做过的选择，以及最近的对话。限额报错、客户端注入的上下文、子代理之间的消息不会混进去；上一个号没来得及回应的那条消息会单独标出来。

`gcccode resume <id>`、`claude -c`、`claude --resume <id>` 会先回到开这个会话的那个号，并在会话原来的目录里续接；那个号不可用时才改用接力文件。同时开着几个会话时，靠状态栏记下的会话 id 认领，不会把隔壁窗口的会话当成自己的。

`.claude.json` 里的 `machineID` 和 `userID` 也不复制。如果两个号写成了同一个，后加的那个会清掉，下次启动由 Claude Code 自己重新生成。两个及以上的 Claude 会员必须各自设出口代理，没分开就拒绝启动。代理只能是 `http://` 或 `https://`：Claude Code 不支持 SOCKS，填了会被忽略、改走本机出口，所以 `socks5://` 会被直接拒绝。

```sh
gcccode account proxy claude-1 http://127.0.0.1:7890
gcccode account proxy claude-2 http://127.0.0.1:7891
```

GCC Code 只能比较代理地址。两个端口最后要落到不同的出口节点上，例如在 Clash 里给 7891 单独配一个节点，否则还是同一个 IP。

`settings.json` 是复制过去的，并且会去掉 `ANTHROPIC_BASE_URL`、`ANTHROPIC_AUTH_TOKEN` 这类中转或 API Key，以及 `HTTPS_PROXY` 这类代理变量。留着前者，这个号不会走它自己的会员登录；留着后者，它会盖掉每个号各自的代理。没单独设代理的号仍然沿用 `settings.json` 里的那个。

Codex 的线程在 `~/.codex` 的 SQLite 里，不能拆成多个目录。所以所有 ChatGPT 号共用一个目录，换号时原子替换 `auth.json`。历史本来就在一起。同一时间只能有一个 ChatGPT 号在用，而且 Codex 正在运行时拒绝换号：它退出时会把旧 token 写回去。

Codex 刷新 token 时会改 `auth.json`。切走之前先写回号池。号池里的副本更新，才允许覆盖；更旧的文件会被拒绝，避免把已经作废的 refresh token 存回去。

如果新号读不了旧号留下的加密推理内容，GCC Code 会从本地记录生成一份接力文件，在新号上开新会话接着做。旧会话还在。

Gemini 每个号一个 `GEMINI_CONFIG_DIR`（仿 Claude）。OAuth / `.env` / settings 落在该目录；skills、extensions 等按需软链回主配置目录。换号时同样用本地接力，不把云端会话接到另一个 Google 账号上。

Grok 每个号一个 `GROK_HOME`（也仿 Claude，不走 Codex 那种换 `auth.json`）。`auth.json`、sessions、`config.toml` 都在这个 home 下，会话自然跟号走，多个号可以并行。skills / plugins / docs 软链回主 `~/.grok`。

Antigravity（`agy`）暂时只收本机已经登录的那一个号。agy 把登录存在系统钥匙串里一个固定的条目里，换配置目录、换 HOME 都还是同一个条目，没法像 Claude 那样一个号一个目录：第二个号一登录就会顶掉第一个。所以号池里已经有 agy 号时，`gcccode account add agy` 会直接拒绝。要换 Google 账号，用 `gcccode account login <id>`，在 agy 里输入 `/logout` 再重新登录。

`gcccode sessions` 读 agy 自己的会话索引，列出标题和目录（需要系统里有 `sqlite3`）。`gcccode resume <id>`、`agy --conversation <id>`、`agy -c` 都能续接。agy 的会话记录是二进制格式，生成不了接力文件；额度用完时只把这个号标成冷却，并告诉你恢复后用哪条命令续接。Antigravity 只能用 Google 账号登录，不走网关。macOS 上用钥匙串判断有没有登录；Linux 和 Windows 上看 agy 日志里有没有登录记录，这两个系统还没实测。

## 额度从哪来

不额外请求官方接口。

- Claude Code 状态栏收到的 JSON 里有 `rate_limits.five_hour` 和 `seven_day`
- Codex 会话文件的 `token_count` 事件里有 5 小时窗口和每周窗口
- Gemini / Grok 若官方没给出窗口数字，选号时按「额度未知」照常调度；会话里出现限额 / 登录失效报错时再冷却或隔离
- Antigravity 不给窗口数字，GCC Code 在 agy 退出后读它的日志：错误级别的 `RESOURCE_EXHAUSTED`、`quota reached` 这类才算额度用完，服务器忙、生图限额不算

快到限额时通知一次。到了限额就冷却到重置时间，选号时跳过。

## 用量页

面板里的「用量」页算的是每个号实际用掉了多少 token：今天 / 7 天 / 30 天 / 90 天，每天的柱状图按客户端分色，再按账号、按模型列出来，最后是最近的会话各花了多少。数据全部来自官方客户端留在本机的会话记录，不发任何请求，也不上传：

- Claude Code：`~/.claude/projects/` 里的会话 jsonl，`usage` 字段的输入 / 输出 / 缓存读写
- Codex：`~/.codex/sessions/` 和 `archived_sessions/` 里的 rollout 文件，`token_count` 事件的累计值做差
- Grok CLI：每个号目录下 `sessions/*/updates.jsonl` 的 `turn_completed`
- Gemini CLI：`tmp/*/chats/session-*.json` 里每条消息的 `tokens`
- Antigravity：本机不留 token 数，统计不到，页面会注明

会话归到哪个号，看的是它从 GCC Code 启动时记下的归属；Codex 会话还能靠文件里的账号 ID 对上。直接用官方命令启动的会话对不上号，单独列成「没对上号的会话」，不会乱猜。扫描是增量的，每个文件只读上次之后新增的部分，结果缓存在 `~/.config/gcccode/usage-cache.json`；本机 1.4 GB 的 Codex 记录第一次扫不到 1 秒，之后几毫秒。

## 可选网关（默认关闭）

号池之外的另一条路：本机起一个只监听 loopback 的网关，接 OpenAI、Anthropic、Gemini 三种协议的请求，转给你自己粘贴的 API Key 上游，**请求级 failover**。上游按列表顺序当优先级；首字节还没返回给 Agent 之前遇到连接失败、401/403、402、429、5xx、余额不足，就换下一个，Agent 看不到失败的那次。

```sh
gcccode gateway provider add claude   --protocol anthropic --url https://api.anthropic.com --key sk-ant-...
gcccode gateway provider add deepseek --url https://api.deepseek.com/v1 --key sk-... --model deepseek-chat
gcccode gateway provider add openai   --responses --url https://api.openai.com/v1 --key sk-...
gcccode gateway provider add gemini   --protocol gemini --url https://generativelanguage.googleapis.com --key AIza...
gcccode gateway provider add codexpool --subscription codex          # 用号池里多个 Codex Plus 轮换
gcccode gateway provider add claudepool --subscription claude --accounts cl-1,cl-2
gcccode use-gateway on                 # gcccode claude / codex / gemini 改走网关（需要时自动启动网关）
gcccode use-gateway off                # 恢复直连官方、走号池
eval "$(gcccode gateway env)"          # 给其他 Agent：按上游打印 OPENAI_* / ANTHROPIC_* / GOOGLE_GEMINI_* 变量
gcccode gateway status                 # 每个上游的成败次数、冷却到几点
gcccode gateway start | stop           # 手动起停，默认 127.0.0.1:3426
```

- 地址写法和各家 SDK 一致：openai 带 `/v1`，anthropic 和 gemini 不带版本路径。
- Claude Code（`ANTHROPIC_BASE_URL`）：anthropic 上游原样转发；openai 上游会把 Anthropic Messages **翻译**成 Chat Completions 再翻译回来，流式、工具调用、图片都支持。上面的例子里 Claude 官方 API 没余额了，会自动换到翻译后的 DeepSeek。openai 上游不认 `claude-*` 模型名时，添加时带 `--model`。
- Codex 用的是 Responses 接口，只会转给标了 `--responses` 的 openai 上游；还没有 Responses → Chat 的翻译。接 Codex 建议用 `use-gateway on codex`，不要只设 `OPENAI_BASE_URL`：已登录 ChatGPT 的 Codex 可能仍用 ChatGPT 登录。
- Gemini CLI（`GOOGLE_GEMINI_BASE_URL`）转给 gemini 上游，需要它用 API Key 方式认证。
- 冷却：429 读 `Retry-After`（没有就 60 秒）；额度、余额类的 429、400 和 402 冷却 30 分钟；401/403 冷却 1 小时；5xx 和连接失败 30 秒。400、404、422 这类请求本身的错误原样返回，不重试。
- 本机安全：只监听 loopback，校验 `Host`，Agent 必须带首次使用时自动生成的本机 token（`gateway env` 会给出）。上游 Key 只存在 gcccode 的 `settings.json`，本机 token 不会发给上游，`status` 也不会输出 Key。
- 网关和「官方直连」二选一，由 `use-gateway` 决定：打开后 `gcccode claude` / `codex` / `gemini`（包括 shell-init 包出来的同名命令）不选号、不换号，直接接网关；用 `--account` 指定账号时仍走号池。它只改 gcccode 自己的设置，不改客户端配置文件：Claude、Gemini 用网关专用的配置目录（不存任何登录，Claude 的历史、skills、MCP 照样共享），Codex 用命令行临时定义的 provider，不碰 `~/.codex/auth.json`。没打开时，这些命令会去掉 shell 里指向网关的变量，照旧直连官方。`gcccode doctor` 会显示当前状态。
- **订阅路由（多个 Plus / Pro）**：`--subscription codex｜claude` 把号池里已登录的订阅号做成上游，网关自己选号、按额度换号、刷新并写回各家原本存 token 的地方——解决「GPT 没有甜点位套餐、想把多个 Plus 并起来路由」这类需求（内建了 CPA/magpie 的多号轮换）。Codex 额度用完当场换号；Claude 进行中的会话固定在一个号上、额度用完退出后用交接文件换号新开会话，且**多个 Claude 号要各自设代理**。ChatGPT 订阅还能翻译给其他 Agent 用。多开订阅号可能触犯服务条款、有封号风险，自行判断；细节见 [docs/gateway.md](docs/gateway.md)。

## 中转兜底

订阅号都不可用时，可以改走你自己的中转，例如 [gcccode.com](https://gcccode.com/) 的套餐。这是普通的 API 配置，不是伪装会员。

```sh
gcccode relay set \
  --anthropic https://your-relay.example \
  --openai https://your-relay.example/v1 \
  --token sk-...
gcccode claude --relay
```

Codex 的 key 只放在本次进程的环境变量里，不写进 `config.toml`。会话结束会把 `config.toml` 改回去；如果你中途改过这个文件，它不会被覆盖。

面板上的「再加一个号」默认链到 gcccode.com，可以在 `~/.config/gcccode/settings.json` 里把 `upsell` 设成 `false`，或改 `relay.buy_url`。

每个号还可以固定出口代理，换号时不会带着上一个号的网络环境：

```sh
gcccode account proxy claude-1 http://127.0.0.1:7890
```

官方客户端如果报出组织被禁用、登录失效，这个号会被隔离，直到 `gcccode account unquarantine` 或重新登录。

## Computer use（LCU）

[LCU](https://github.com/amontlabs/lcu) 把 ChatGPT 桌面版里 Codex 的 computer use 单独拿出来，作为 MCP 服务器给 Claude Code 和 Codex CLI 用：读窗口、点击、打字、截图，开了 Chrome 选项还能操作标签页。

LCU 只往默认的 `~/.claude` 注册，看到 `CLAUDE_CONFIG_DIR` 就拒绝安装。号池里每个 Claude 号都有自己的目录，所以在 gcccode 开的会话里直接装 LCU 会失败。用 gcccode 来装：

```sh
gcccode lcu install            # 下载、校验、安装，再注册到 Claude Code 和 Codex
gcccode lcu                    # 装在哪，号池里哪些号已经带上
gcccode lcu doctor             # 其余子命令原样交给 lcu：doctor、status、apps、update
```

`install` 和 `setup` 后面的参数交给 `lcu setup`，例如 `--chrome`、`--audio`、Linux 上的 `--session direct`。下载支持 `--proxy` 和 `--mirror`，校验和同样只从 GitHub 取。

注册写在主目录：`~/.claude.json` 里的 `lcu` 服务器、`settings.json` 里的生命周期 hook 和权限规则、`skills/lcu-approve` 审批插件。用某个号启动时，gcccode 把前两样同步进那个号的目录，审批插件跟着 `skills/` 软链接过去。主目录里删掉的 MCP 服务器，号目录里也会删掉。走中转时只额外带上 LCU 这几项。Codex 本来就共用 `~/.codex`，不用额外处理。

前提和 LCU 一样：Apple Silicon 的 Mac，或者有 X11 桌面的 Linux；装好官方 ChatGPT 桌面版（不用登录）和 Python 3.12+。Linux 上装运行时要 root，gcccode 只下载和校验，然后打印出要你自己执行的 `sudo` 命令。

## 风险

没有工具能保证不封号。服务条款限制把账号交给别人用。这个项目是给自己的多个会员用的：一个会话固定一个号，登录走官方命令，限额到了就等重置。不要用它共享、转卖或者绕过限额。

## 已核对和还没在真机上走完的部分

在 Claude Code 2.1.143 里核对过：

- 设置 `CLAUDE_CONFIG_DIR` 后，钥匙串条目名带目录 SHA-256 的前 8 位，不同目录的登录互不覆盖
- 状态栏 JSON 带 5 小时和 7 天额度

在本机的 Codex 会话文件里核对过：`rate_limits.primary` 是 5 小时窗口，`secondary` 是每周窗口，并带 `plan_type`。

自动测试覆盖：目录隔离和历史软链接、并行会话按启动记录认领、续接回到原目录和原来的号、管理命令不经过选号、原有状态栏保留和还原、项目级 MCP 同步进号目录、中转配置不会漏进会员目录、`auth.json` 切换和 token 写回、旧 token 不能覆盖新 token、额度解析、接力文件的目标/待办/改动/去噪（Claude 和 Codex 两种记录格式）、连续接力保留原始目标、三种选号策略和快用完的号排到最后、全部用完时的最早恢复时间、从限额消息里读出恢复时间（只冷却真正用完的那个窗口）、`sessions` 合并两种客户端并跳过 Codex 子代理线程、SOCKS 代理和 `settings.json` 里的共享代理被拦下、LCU 注册同步进号目录和中转目录、主目录删掉的 MCP 服务器从号目录清掉、LCU 发布包的校验。

还需要你自己有第二个号才能做的真机步骤：跨号 `claude --resume`、Codex 换号后续接是否被加密推理拒绝、桌面版换号后是否必须重启。代码按「必须重启桌面版」和「拒绝就走接力文件」来处理。

LCU 这边，从 GitHub 下载并校验 0.9.5 走过真机；注册和同步只用模仿 LCU 行为的假 `lcu` 测过。在号池里真的让 Claude 截一次图，还没走过。

## 开发

```sh
make test
GCCCODE_HOME=/tmp/gcccode-dev bin/gcccode doctor
```

配置在 `~/.config/gcccode/`（可用 `GCCCODE_HOME` 或 `XDG_CONFIG_HOME` 改位置）。Codex 的 `auth.json` 副本权限是 `0600`。面板只监听回环地址。

## 许可

Apache License 2.0。见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。
