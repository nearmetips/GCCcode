/* GCC Code panel. One page, three views; every action is a POST to the local server. */
"use strict";

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({"&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"}[c]));
const svg = (d, extra = "") => `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" ${extra}>${d}</svg>`;

const I = {
  pool: svg('<path d="M4 7.5 12 3l8 4.5v9L12 21l-8-4.5z"/><path d="M4 7.5 12 12l8-4.5M12 12v9"/>'),
  add: svg('<circle cx="12" cy="12" r="9"/><path d="M12 8v8M8 12h8"/>'),
  settings: svg('<path d="M4 6h10M18 6h2M4 12h2M10 12h10M4 18h12M20 18h0"/><circle cx="16" cy="6" r="2"/><circle cx="8" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>'),
  more: svg('<circle cx="5" cy="12" r="1.6" fill="currentColor"/><circle cx="12" cy="12" r="1.6" fill="currentColor"/><circle cx="19" cy="12" r="1.6" fill="currentColor"/>'),
  refresh: svg('<path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/>'),
  check: svg('<path d="M5 12.5 10 17l9-10"/>'),
  alert: svg('<path d="M12 9v4M12 17h0"/><path d="M10.3 3.9 2.5 18a2 2 0 0 0 1.7 3h15.6a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/>'),
  info: svg('<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h0"/>'),
  clock: svg('<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>'),
  globe: svg('<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>'),
  key: svg('<circle cx="8" cy="15" r="4"/><path d="M11 12l9-9M15 7l3 3M18 4l2 2"/>'),
  login: svg('<path d="M10 17l5-5-5-5M15 12H3M21 4v16"/>'),
  browser: svg('<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M7 6.5h0M10 6.5h0"/>'),
  shield: svg('<path d="M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z"/>'),
  pause: svg('<rect x="6" y="5" width="4" height="14" rx="1"/><rect x="14" y="5" width="4" height="14" rx="1"/>'),
  play: svg('<path d="M7 5v14l11-7z"/>'),
  trash: svg('<path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/>'),
  terminal: svg('<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3M12 15h5"/>'),
  x: svg('<path d="M6 6l12 12M18 6 6 18"/>'),
  external: svg('<path d="M14 4h6v6M20 4l-9 9M19 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h5"/>'),
  user: svg('<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>'),
  star: svg('<path d="M12 3l2.8 5.7 6.2.9-4.5 4.4 1.1 6.2L12 17.3 6.4 20.2l1.1-6.2L3 9.6l6.2-.9z"/>'),
  chart: svg('<path d="M4 20h16"/><rect x="6" y="11" width="3" height="6" rx=".8"/><rect x="11" y="6" width="3" height="11" rx=".8"/><rect x="16" y="13" width="3" height="4" rx=".8"/>'),
  folder: svg('<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>'),
};

const KIND_NAME = {claude: "Claude Code", codex: "Codex", gemini: "Gemini CLI", grok: "Grok CLI", agy: "Antigravity"};

const KIND_ORDER = ["claude", "codex", "gemini", "grok", "agy"];
const MONO = {claude: "Cl", codex: "Cx", gemini: "Ge", grok: "Gr", agy: "Ag"};
const LOGO = {
  claude: "linear-gradient(135deg,#e07a5f,#c25e45)",
  codex: "linear-gradient(135deg,#1f2a37,#0f172a)",
  gemini: "linear-gradient(135deg,#4285f4,#7b61ff)",
  grok: "linear-gradient(135deg,#3f3f46,#18181b)",
  agy: "linear-gradient(135deg,#8b5cf6,#6d28d9)",
};

let state = null;
let route = location.hash.replace("#", "") || "pool";
let watch = null;
let openMenu = null;
let usageDays = [1, 7, 30, 90].includes(+localStorage.getItem("gcc.usageDays")) ? +localStorage.getItem("gcc.usageDays") : 30;
const usageData = {};
let usageLoading = false;
let usageError = "";

/* ---------- plumbing ---------- */

function toast(msg, tone) {
  const box = $("toasts");
  const el = document.createElement("div");
  el.className = "toast" + (tone === "bad" ? " bad" : "");
  el.innerHTML = (tone === "bad" ? I.alert : I.check) + `<div>${esc(msg)}</div>`;
  el.onclick = () => el.remove();
  box.appendChild(el);
  setTimeout(() => el.remove(), Math.min(14000, 4500 + msg.length * 50));
}

const offline = () => state?.app ? "面板和应用断开了，重新打开应用试试。" : "连不上 GCC Code。确认 gcccode web 还在运行。";

async function post(url, body, quiet) {
  let r;
  try {
    r = await fetch(url, {method: "POST", headers: {"Content-Type": "application/json", "X-GCCCode": "1"}, body: JSON.stringify(body || {})});
  } catch (e) {
    toast(offline(), "bad");
    return null;
  }
  const text = await r.text();
  if (!r.ok) {
    if (!quiet) toast(text.trim() || "操作没有成功", "bad");
    return null;
  }
  let d = {};
  try { d = JSON.parse(text); } catch (e) {}
  if (d.message && !quiet) toast(d.message);
  return d;
}

async function load(background) {
  let d;
  try {
    d = await (await fetch("/api/state")).json();
  } catch (e) {
    if (!background) toast(offline(), "bad");
    return;
  }
  state = d;
  if (d.app) document.documentElement.classList.add("app");
  render(background);
  if (watch) checkWatch(d);
  if (route === "usage") loadUsage(true);
}

// loadUsage fetches the report for the current range. The server reads only
// what changed since last time, so refreshing is cheap.
async function loadUsage(background) {
  if (usageLoading) return;
  usageLoading = true;
  const days = usageDays;
  try {
    const r = await fetch(`/api/usage?days=${days}`);
    if (!r.ok) throw new Error((await r.text()).trim() || "读取失败");
    usageData[days] = await r.json();
    usageError = "";
  } catch (e) {
    if (!usageData[days]) usageError = e.message === "Failed to fetch" || e.message === "Load failed" ? offline() : e.message;
    if (!background) toast(usageError || offline(), "bad");
  }
  usageLoading = false;
  if (route === "usage") render(background);
  if (days !== usageDays && !usageData[usageDays]) loadUsage(background);
}

function paint(el, html) {
  if (el.dataset.html !== html) { el.innerHTML = html; el.dataset.html = html; }
}

const names = () => Object.fromEntries((state?.clients || []).map(c => [c.kind, c.name]));
const accountOf = (id) => (state?.accounts || []).find(a => a.id === id);
const clientOf = (kind) => (state?.clients || []).find(c => c.kind === kind);

function go(r) {
  route = r;
  location.hash = r;
  render();
  $("page").closest(".main").scrollTop = 0;
}

/* ---------- formatting ---------- */

function pct(w) { return Math.max(0, Math.min(100, Math.round(w.used_pct))); }
// tone colours what is left: red when nearly spent, amber when under a third.
function tone(used) { return used >= 90 ? "bad" : used >= 67 ? "warn" : ""; }
function fmtReset(unix) {
  if (!unix) return "";
  const t = new Date(unix > 1e12 ? unix : unix * 1000);
  const now = new Date();
  const hm = t.getHours().toString().padStart(2, "0") + ":" + t.getMinutes().toString().padStart(2, "0");
  if (t.toDateString() === now.toDateString()) return hm + " 重置";
  return `${t.getMonth() + 1}/${t.getDate()} ${hm} 重置`;
}
function quotaRow(k, w) {
  if (!w) return `<div class="q unknown"><span class="k">${k}</span><div class="bar"></div><span class="v">还不知道</span></div>`;
  const p = pct(w);
  const reset = fmtReset(w.reset_at);
  return `<div class="q"><span class="k">${k}</span><div class="bar"><i class="${tone(p)}" style="width:${100 - p}%"></i></div><span class="v"><b>${100 - p}%</b> 剩余${reset ? " · " + esc(reset) : ""}</span></div>`;
}
function statusPill(a) {
  const r = a.reason || "";
  if (a.quarantined) return `<span class="pill bad">已隔离</span>`;
  if (a.disabled) return `<span class="pill mute">已停用</span>`;
  if (r === "可用") return `<span class="pill ok">可用</span>`;
  if (r === "使用中") return `<span class="pill busy">使用中</span>`;
  if (r === "未登录") return `<span class="pill mute">未登录</span>`;
  if (r.startsWith("额度用完")) return `<span class="pill warn">额度用完</span>`;
  return `<span class="pill warn">${esc(r)}</span>`;
}
function initials(a) {
  const s = (a.label || a.id || "?").trim();
  const m = s.match(/^[A-Za-z0-9]+/);
  if (m) return m[0].slice(0, 2).toUpperCase();
  return s.slice(0, 1);
}
// fmtTok shows a token count the way people say it: 1.2M, 345K.
function fmtTok(n) {
  n = Number(n) || 0;
  if (n < 1000) return String(n);
  if (n < 1e6) return (n / 1e3).toFixed(n < 1e4 ? 1 : 0) + "K";
  if (n < 1e9) return (n / 1e6).toFixed(n < 1e7 ? 2 : 1) + "M";
  return (n / 1e9).toFixed(2) + "B";
}
function fmtNum(n) { return (Number(n) || 0).toLocaleString("zh-CN"); }
function fmtTime(unix) {
  if (!unix) return "";
  const t = new Date(unix * 1000), now = new Date();
  const hm = t.getHours().toString().padStart(2, "0") + ":" + t.getMinutes().toString().padStart(2, "0");
  if (t.toDateString() === now.toDateString()) return "今天 " + hm;
  const y = new Date(now); y.setDate(now.getDate() - 1);
  if (t.toDateString() === y.toDateString()) return "昨天 " + hm;
  return `${t.getMonth() + 1}/${t.getDate()} ${hm}`;
}
function fmtDay(iso) { const [, m, d] = iso.split("-"); return `${+m}/${+d}`; }

/* ---------- render ---------- */

function render(background) {
  if (!state) return;
  if (background && (openMenu || $("modal-root").firstChild)) return;
  const d = state;
  const counts = {pool: d.accounts.length};
  paint($("nav"), [
    ["pool", "号池", I.pool, counts.pool],
    ["usage", "用量", I.chart],
    ["add", "添加账号", I.add],
    ["settings", "设置", I.settings],
  ].map(([r, t, ic, n]) => `<button type="button" class="${route === r ? "active" : ""}" data-go="${r}">${ic}<span>${t}</span>${n ? `<span class="count">${n}</span>` : ""}</button>`).join(""));
  paint($("foot"), `<div><b>v${esc(d.version)}</b>${d.app ? " · 桌面版" : ""}</div><div>只调度官方客户端，登录都留在你自己的电脑上</div>`);
  const page = $("page");
  const html = route === "add" ? renderAdd(d) : route === "settings" ? renderSettings(d) : route === "usage" ? renderUsage(d) : renderPool(d);
  paint(page, html);
  if (route === "settings") syncSettings(d);
  if (route === "usage" && !usageData[usageDays] && !usageLoading && !usageError) loadUsage();
}

/* ---------- usage ---------- */

function renderUsage(d) {
  const u = usageData[usageDays];
  const head = `<div class="page-head"><div><h1>用量</h1><p>每个号实际用掉了多少。数据来自官方客户端留在这台电脑上的记录，不会上传。</p></div>
    <div class="actions"><div class="seg">${[[1, "今天"], [7, "7 天"], [30, "30 天"], [90, "90 天"]].map(([n, t]) => `<button type="button" class="${usageDays === n ? "on" : ""}" data-days="${n}">${t}</button>`).join("")}</div>
    <button type="button" class="btn" data-reload-usage>${I.refresh}<span>刷新</span></button></div></div>`;
  if (!u && usageError) return head + `<div class="card empty"><div class="orb bad">${I.alert}</div><h3>读不出用量</h3><p>${esc(usageError)}</p><button type="button" class="btn" data-reload-usage>${I.refresh}<span>再试一次</span></button></div>`;
  if (!u) return head + `<div class="card loading"><div class="spinner"></div><p>正在读这台电脑上的会话记录…</p></div>`;
  const t = u.totals;
  if (!t.calls) {
    const what = usageDays === 1 ? "今天" : `这 ${usageDays} 天`;
    return head + `<div class="card empty"><div class="orb">${I.chart}</div><h3>${what}还没有用量</h3><p>用 Claude Code、Codex、Grok 或 Gemini CLI 聊过以后，这里会按天、按号、按模型统计 token。</p>${d.accounts.length ? "" : `<button type="button" class="btn primary" data-go="add">${I.add}<span>先添加一个号</span></button>`}</div>${usageNotes(u)}`;
  }
  const cache = t.cache_read + t.cache_write;
  const summary = `<div class="summary">
    <div class="card stat"><div class="v">${fmtTok(t.input + t.output + cache)}</div><div class="k">token 总量</div><div class="s">输入 ${fmtTok(t.input)} · 输出 ${fmtTok(t.output)} · 缓存 ${fmtTok(cache)}</div></div>
    <div class="card stat"><div class="v">${fmtTok(t.output)}</div><div class="k">模型写出来的</div><div class="s">输出 token，最花额度的部分</div></div>
    <div class="card stat"><div class="v">${fmtNum(t.calls)}</div><div class="k">次模型调用</div><div class="s">平均每次 ${fmtTok(Math.round((t.input + t.output + cache) / t.calls))}</div></div>
    <div class="card stat"><div class="v">${fmtNum(u.sessions)}</div><div class="k">个会话</div><div class="s">${u.days === 1 ? "今天聊过的" : `这 ${u.days} 天里聊过的`}</div></div>
  </div>`;
  return head + summary + usageChart(u) + usageAccounts(u) + usageModels(u) + usageRecent(u) + usageNotes(u);
}

const total = (x) => x.input + x.output + x.cache_read + x.cache_write;

function usageChart(u) {
  const days = u.daily;
  const kinds = KIND_ORDER.filter(k => days.some(dd => dd.kinds[k] && total(dd.kinds[k]) > 0));
  if (days.length === 1) {
    // One day: a bar chart would be a single block. Show how the day splits by client.
    const dd = days[0], sum = total(dd.tokens) || 1;
    const segs = kinds.map(k => `<i class="k-${k}" style="flex:${total(dd.kinds[k])}" title="${esc(KIND_NAME[k] || k)} ${fmtTok(total(dd.kinds[k]))}"></i>`).join("");
    const legend = kinds.map(k => `<span><i class="k-${k}"></i>${KIND_NAME[k] || k} <b>${Math.round(total(dd.kinds[k]) / sum * 100)}%</b> · ${fmtTok(total(dd.kinds[k]))} · ${fmtNum(dd.kinds[k].calls)} 次</span>`).join("");
    return `<div class="card"><div class="card-head"><div><h2>今天用在了哪里</h2><p>按客户端分。</p></div></div>
      <div class="card-body"><div class="split big">${segs}</div><div class="legend big">${legend}</div></div></div>`;
  }
  const max = Math.max(1, ...days.map(dd => total(dd.tokens)));
  const every = days.length <= 7 ? 1 : days.length <= 31 ? 5 : 15;
  const bars = days.map((dd, i) => {
    const sum = total(dd.tokens);
    const segs = kinds.map(k => {
      const v = dd.kinds[k] ? total(dd.kinds[k]) : 0;
      return v ? `<i class="k-${k}" style="flex:${v}"></i>` : "";
    }).join("");
    const tip = sum ? `${fmtDay(dd.date)} · ${fmtTok(sum)} token · ${fmtNum(dd.tokens.calls)} 次\n` + kinds.filter(k => dd.kinds[k]).map(k => `${KIND_NAME[k] || k} ${fmtTok(total(dd.kinds[k]))}`).join("\n") : `${fmtDay(dd.date)} · 没有用量`;
    const label = (i % every === every - 1 || days.length <= 7 || i === days.length - 1) ? fmtDay(dd.date) : "";
    return `<div class="col" title="${esc(tip)}"><div class="stack" style="height:${sum ? Math.max(2, Math.round(sum / max * 100)) : 0}%">${segs}</div><span class="x">${label}</span></div>`;
  }).join("");
  const legend = kinds.map(k => `<span><i class="k-${k}"></i>${KIND_NAME[k] || k}</span>`).join("");
  return `<div class="card"><div class="card-head"><div><h2>每天用了多少</h2><p>按客户端分色，把鼠标放到柱子上看明细。</p></div><div class="legend">${legend}</div></div>
    <div class="card-body chart-body"><div class="ymax">${fmtTok(max)}</div><div class="chart">${bars}</div></div></div>`;
}

function usageAccounts(u) {
  const rows = u.accounts.filter(a => a.kind !== "agy" || total(a.tokens) > 0);
  if (!rows.length) return "";
  const max = Math.max(1, ...rows.map(a => total(a.tokens)));
  const sum = rows.reduce((s, a) => s + total(a.tokens), 0) || 1;
  const html = rows.map(a => {
    const v = total(a.tokens);
    const share = Math.round(v / sum * 100);
    const name = a.unattributed ? `没对上号的 ${KIND_NAME[a.kind] || a.kind} 会话` : a.label;
    const sub = a.unattributed ? (a.why || "不是从 GCC Code 启动的，不知道用的哪个号") : [KIND_NAME[a.kind] || a.kind, a.email && a.email !== a.label ? a.email : "", a.plan || ""].filter(Boolean).join(" · ");
    const left = [a.five_hour ? `5 小时剩 ${100 - pct(a.five_hour)}%` : "", a.weekly ? `周剩 ${100 - pct(a.weekly)}%` : ""].filter(Boolean).join(" · ");
    return `<div class="urow${v ? "" : " idle"}${a.unattributed ? " un" : ""}">
      <div class="avatar ${esc(a.kind)} sm">${a.unattributed ? "?" : esc(initials(a))}</div>
      <div class="who"><div class="label" title="${esc(name)}">${esc(name)}</div><div class="sub" title="${esc(sub)}">${esc(sub)}</div></div>
      <div class="share"><div class="bar"><i class="k-${esc(a.kind)}" style="width:${v ? Math.max(1.5, v / max * 100) : 0}%"></i></div><span>${v ? share + "%" : ""}</span></div>
      <div class="num"><b>${v ? fmtTok(v) : "—"}</b><span>${v ? `输出 ${fmtTok(a.tokens.output)}` : "这段时间没用过"}</span></div>
      <div class="num"><b>${v ? fmtNum(a.tokens.calls) + " 次" : ""}</b><span>${v ? `${a.sessions} 个会话` : ""}</span></div>
      <div class="num last"><b>${v ? esc(fmtTime(a.last_at)) : ""}</b><span>${v ? (left || (a.unattributed ? "" : "剩余额度还不知道")) : ""}</span></div>
    </div>`;
  }).join("");
  return `<div class="card"><div class="card-head"><div><h2>按账号</h2><p>每个号分别用了多少，以及它现在还剩多少额度。</p></div></div><div class="card-body table">${html}</div></div>`;
}

function usageModels(u) {
  if (!u.models.length) return "";
  const max = Math.max(1, ...u.models.map(m => total(m.tokens)));
  const html = u.models.map(m => {
    const t = m.tokens, v = total(t);
    const seg = (cls, n, label) => n ? `<i class="${cls}" style="flex:${n}" title="${label} ${fmtTok(n)}"></i>` : "";
    return `<div class="urow model">
      <div class="who"><div class="label mono" title="${esc(m.model)}">${esc(m.model)}</div><div class="sub">${esc(KIND_NAME[m.kind] || m.kind)}</div></div>
      <div class="split" style="width:${Math.max(4, v / max * 100)}%">${seg("in", t.input, "输入")}${seg("out", t.output, "输出")}${seg("cr", t.cache_read, "缓存命中")}${seg("cw", t.cache_write, "缓存写入")}</div>
      <div class="num"><b>${fmtTok(v)}</b><span>输出 ${fmtTok(t.output)} · ${fmtNum(t.calls)} 次</span></div>
    </div>`;
  }).join("");
  return `<div class="card"><div class="card-head"><div><h2>按模型</h2><p>条子的长度是总量，颜色是构成。</p></div>
    <div class="legend"><span><i class="in"></i>输入</span><span><i class="out"></i>输出</span><span><i class="cr"></i>缓存命中</span><span><i class="cw"></i>缓存写入</span></div></div>
    <div class="card-body table">${html}</div></div>`;
}

function usageRecent(u) {
  if (!u.recent.length) return "";
  const html = u.recent.map(s => `<div class="urow sess">
      <div class="avatar ${esc(s.kind)} sm">${MONO[s.kind] || "?"}</div>
      <div class="who"><div class="label" title="${esc(s.title || s.id)}">${esc(s.title || "（没有标题）")}</div>
        <div class="sub">${[s.label || (KIND_NAME[s.kind] || s.kind), s.cwd ? `<span class="path" title="${esc(s.cwd)}">${esc(s.cwd)}</span>` : ""].filter(Boolean).join(" · ")}</div></div>
      <div class="num"><b>${fmtTok(total(s.tokens))}</b><span>${fmtNum(s.tokens.calls)} 次</span></div>
      <div class="num last"><b>${esc(fmtTime(s.at))}</b></div>
    </div>`).join("");
  return `<div class="card"><div class="card-head"><div><h2>最近的会话</h2><p>最近聊过的会话，花了多少。</p></div></div><div class="card-body table">${html}</div></div>`;
}

function usageNotes(u) {
  if (!u.notes?.length) return "";
  return `<div class="fine">${u.notes.map(n => `<p>${I.info}<span>${esc(n)}</span></p>`).join("")}</div>`;
}

function renderPool(d) {
  const n = names();
  const usable = d.accounts.filter(a => a.reason === "可用" || a.reason === "使用中").length;
  const busy = d.accounts.filter(a => a.reason === "使用中").length;
  const spent = d.accounts.filter(a => (a.reason || "").startsWith("额度用完")).length;
  const off = d.accounts.length - usable - spent;
  let body = "";
  if (!d.accounts.length) {
    body = `<div class="card empty"><div class="orb">${I.pool}</div><h3>号池还是空的</h3><p>把你的 Claude、ChatGPT 等会员号放进来，一个号额度用完就自动换下一个。</p><button type="button" class="btn primary" data-go="add">${I.add}<span>添加第一个号</span></button></div>`;
  } else {
    body += `<div class="summary">
      <div class="card stat"><div class="v">${d.accounts.length}</div><div class="k">号池里的号</div></div>
      <div class="card stat ok"><div class="v">${usable}</div><div class="k">现在能用${busy ? `，${busy} 个使用中` : ""}</div></div>
      <div class="card stat ${spent ? "warn" : ""}"><div class="v">${spent}</div><div class="k">额度用完，等重置</div></div>
      <div class="card stat"><div class="v">${off}</div><div class="k">停用、隔离或未登录</div></div>
    </div>`;
    for (const kind of KIND_ORDER) {
      const list = d.accounts.filter(a => a.kind === kind);
      if (!list.length) continue;
      body += `<div class="section-label">${esc(n[kind] || kind)} <span class="n">· ${list.length} 个号</span></div><div class="grid">${list.map(accountCard).join("")}</div>`;
    }
  }
  const waiting = (d.waiting || []).map(w => {
    const [kind, ...rest] = w.split("：");
    return `<div class="banner warn">${I.alert}<div><b>${esc(n[kind] || kind)}</b>：${esc(rest.join("："))}</div></div>`;
  }).join("");
  return `<div class="page-head"><div><h1>号池</h1><p>每个号的额度和状态。启动 Claude、Codex 时，GCC Code 从这里挑一个有额度的号。</p></div>
    <div class="actions"><button type="button" class="btn" data-reload>${I.refresh}<span>刷新</span></button><button type="button" class="btn primary" data-go="add">${I.add}<span>添加账号</span></button></div></div>
    ${waiting}${body}`;
}

function accountCard(a) {
  const id = esc(a.id);
  const n = names();
  const sub = [a.email && a.email !== a.label ? a.email : "", a.plan || ""].filter(Boolean).join(" · ") || n[a.kind] || a.kind;
  let primary = `<button type="button" class="btn sm" data-act="use" data-id="${id}">${I.star}<span>用这个号</span></button>`;
  if (a.quarantined) primary = `<button type="button" class="btn sm primary" data-act="unquarantine" data-id="${id}">${I.shield}<span>解除隔离</span></button>`;
  else if (a.disabled) primary = `<button type="button" class="btn sm" data-act="enable" data-id="${id}">${I.play}<span>启用</span></button>`;
  else if (a.reason === "未登录") primary = `<button type="button" class="btn sm primary" data-act="login" data-id="${id}">${I.login}<span>去登录</span></button>`;
  const chips = [];
  if (a.token) {
    const days = a.token_days;
    const t = days < 0 ? "bad" : days <= 30 ? "warn" : "";
    chips.push(`<span class="chip ${t}">${I.key}<span>${days < 0 ? "token 已过期" : `token 还剩 ${days} 天`}</span></span>`);
  }
  if (a.proxy) chips.push(`<span class="chip">${I.globe}<span title="${esc(a.proxy)}">${esc(a.proxy)}</span></span>`);
  const note = a.proxy_note ? `<div class="note">${I.alert}<div>${esc(a.proxy_note)}</div><button type="button" class="btn sm" data-act="proxy" data-id="${id}">设代理</button></div>` : "";
  const tokenNote = a.token && a.token_days <= 30 ? `<div class="note">${I.clock}<div>${a.token_days < 0 ? "这个号的 token 过期了，续期后才能再用。" : "这个号的 token 快到期了，抽空续期一下。"}</div><button type="button" class="btn sm" data-act="login" data-id="${id}">续期</button></div>` : "";
  const claudeOnly = a.kind !== "claude" ? "" : `
      <button type="button" data-act="browser" data-id="${id}">${I.browser}打开专用浏览器</button>
      <button type="button" data-act="token" data-id="${id}">${I.key}${a.token ? "粘贴新 token" : "改用 token 登录"}</button>`;
  const menu = `<div class="menu-wrap"><button type="button" class="btn icon ghost" data-menu="${id}" title="更多">${I.more}</button>
    <div class="menu" hidden data-menu-for="${id}">
      <button type="button" data-act="login" data-id="${id}">${I.login}${a.token ? "续期 token" : "重新登录"}</button>${claudeOnly}
      <button type="button" data-act="proxy" data-id="${id}">${I.globe}${a.proxy ? "改代理" : "设代理"}</button>
      <hr>
      ${a.disabled ? `<button type="button" data-act="enable" data-id="${id}">${I.play}启用</button>` : `<button type="button" data-act="disable" data-id="${id}">${I.pause}停用</button>`}
      <button type="button" class="danger" data-act="remove" data-id="${id}">${I.trash}从号池移除</button>
    </div></div>`;
  const dim = a.disabled || a.quarantined || a.reason === "未登录";
  return `<div class="card acct${dim ? " dim" : ""}" data-card="${id}">
    <div class="top"><div class="avatar ${esc(a.kind)}">${esc(initials(a))}</div><div class="who"><div class="label" title="${esc(a.label)}">${esc(a.label)}</div><div class="sub" title="${esc(sub)}">${esc(sub)}</div></div>${statusPill(a)}</div>
    <div class="quota">${quotaRow("5 小时", a.five_hour)}${quotaRow("每周", a.weekly)}</div>
    ${chips.length ? `<div class="chips">${chips.join("")}</div>` : ""}
    ${note}${tokenNote}
    <div class="foot"><span class="reason" title="${esc(a.reason)}">${esc(a.reason)}</span><div style="display:flex;gap:6px;align-items:center">${primary}${menu}</div></div>
  </div>`;
}

function renderAdd(d) {
  const cards = d.clients.map(c => {
    const kind = esc(c.kind);
    let status = c.installed ? (c.count ? `号池里有 ${c.count} 个号` : "已安装，还没有号") : "这台电脑上没装";
    const add = c.can_add ? `<button type="button" class="btn ${c.count ? "" : "primary"}" data-add="${kind}">${I.add}<span>${c.count ? "再加一个" : "添加"}</span></button>` : "";
    const found = c.found ? `<div class="found"><span>这台电脑已登录 <b>${esc(c.found_email || c.name)}</b>，还没放进号池</span><button type="button" class="btn sm primary" data-import="${kind}">放进号池</button></div>` : "";
    let hint = c.note ? esc(c.note) : "";
    if (!c.installed) hint = `没找到 ${esc(c.name)}。${c.install_url ? `<a href="${esc(c.install_url)}" target="_blank" rel="noopener">看安装说明</a>，` : ""}装好后回到这里点刷新。`;
    else if (!hint && c.kind !== "claude") hint = "点「添加」会弹出一个终端窗口，跟着提示在浏览器里登录一次就行。";
    return `<div class="card client${c.installed ? "" : " off"}">
      <div class="top"><div class="logo" style="background:${LOGO[c.kind] || "#64748b"}">${MONO[c.kind] || "?"}</div><div class="t"><div class="n">${esc(c.name)}</div><div class="s">${status}</div></div>${add}</div>
      ${found}${hint ? `<div class="hint">${hint}</div>` : ""}
    </div>`;
  }).join("");
  return `<div class="page-head"><div><h1>添加账号</h1><p>每个会员号只要加一次。加好以后，启动时自动挑有额度的号。</p></div>
    <div class="actions"><button type="button" class="btn" data-reload>${I.refresh}<span>刷新</span></button></div></div>
    <div class="banner info">${I.info}<div>Claude 号在这里直接加：给每个号开一个专用浏览器登录授权，拿到一年期 token，换号就是换 token。其他客户端会弹终端窗口走官方登录。</div></div>
    <div class="clients">${cards}</div>`;
}

function renderSettings(d) {
  const claude = clientOf("claude");
  const relay = d.relay;
  const cli = d.cli || {};
  return `<div class="page-head"><div><h1>设置</h1><p>怎么换号、要不要自动续接，以及命令行工具。</p></div></div>
  <div class="card"><div class="card-head"><div><h2>怎么换号</h2><p>启动 Claude、Codex 时，从号池里挑号的方式。</p></div></div>
    <div class="card-body options" id="strategy">
      <label class="option"><input type="radio" name="strategy" value="smart"><div><b>自动（推荐）</b><span>每次启动时，挑额度最快恢复的那个号，先把快重置的额度用掉。</span></div></label>
      <label class="option"><input type="radio" name="strategy" value="pace"><div><b>匀速</b><span>按每周额度平均着用，不容易一下子把某个号用光。</span></div></label>
      <label class="option"><input type="radio" name="strategy" value="manual"><div><b>手动</b><span>只用你在号池里点了「用这个号」的那个，用不了也不换。</span></div></label>
    </div></div>
  <div class="card"><div class="card-body" style="padding-top:4px;padding-bottom:4px">
    <div class="row"><div class="t"><b>自动续接</b><span>号的额度用完、退出客户端后，不再询问，直接换下一个号接着刚才的会话。</span></div><label class="switch"><input type="checkbox" id="continue"><i></i></label></div>
    <div class="row" id="statusline-row" ${claude?.installed ? "" : "hidden"}><div class="t"><b>在 Claude Code 底部显示额度</b><span>显示当前用的是哪个号、还剩多少额度。不开的话看不到 Claude 号的额度，只能盲选。</span></div><label class="switch"><input type="checkbox" id="statusline"><i></i></label></div>
  </div></div>
  <div class="card"><div class="card-body" style="padding-top:4px;padding-bottom:4px">
    <div class="row"><div class="t"><b>命令行工具</b><span>${cli.installed ? `终端里的 <code>gcccode</code> 就是这个版本：${esc(cli.path)}` : `在终端里敲 <code>gcccode claude</code> 就能按号池启动。${d.app ? "点右边装好，会放到 ~/.local/bin 并加进 PATH。" : "当前终端里的 gcccode 不是这个版本。"}`}</span></div>
      ${cli.installed ? `<span class="pill ok">已安装</span>` : `<button type="button" class="btn" data-cli>${I.terminal}<span>安装</span></button>`}</div>
    <div class="row"><div class="t"><b>中转兜底</b><span>所有会员号都用不了时，改走你买的中转服务。${relay.ready ? "已配置" + (relay.name ? "：" + esc(relay.name) : "") : "还没配置，要用的话在终端运行 <code>gcccode relay set</code>。"}</span></div>
      ${relay.ready || relay.enabled ? `<label class="switch"><input type="checkbox" id="relay" ${relay.enabled ? "checked" : ""}><i></i></label>` : `<span class="pill mute">未配置</span>`}</div>
  </div></div>
  ${d.upsell ? `<div class="card"><div class="card-body row"><div class="t"><b>额度不够用？</b><span>可以再加一个会员号。</span></div><a class="btn" href="${esc(d.buy_url)}" target="_blank" rel="noopener">${I.external}<span>去看看</span></a></div></div>` : ""}
  <div class="card"><div class="card-body"><dl class="kv"><dt>版本</dt><dd>v${esc(d.version)}${d.app ? " 桌面版" : ""}</dd><dt>原则</dt><dd>只调度官方客户端，不伪装、不绕限额。登录都留在你自己的电脑上。</dd><dt>项目</dt><dd><a href="https://github.com/nearmetips/DailiCode" target="_blank" rel="noopener">github.com/nearmetips/DailiCode</a></dd></dl></div></div>`;
}

function syncSettings(d) {
  for (const el of document.querySelectorAll('input[name="strategy"]')) {
    el.checked = el.value === d.strategy;
    el.closest(".option").classList.toggle("on", el.checked);
  }
  if ($("continue")) $("continue").checked = d.auto_continue;
  if ($("statusline")) $("statusline").checked = d.status_line;
}

/* ---------- modal ---------- */

function modal({title, sub, body, foot, wire, wide}) {
  closeModal();
  const root = $("modal-root");
  root.innerHTML = `<div class="overlay"><div class="modal" role="dialog" aria-modal="true" ${wide ? 'style="width:min(600px,100%)"' : ""}>
    <div class="m-head"><h3>${esc(title)}</h3>${sub ? `<p>${sub}</p>` : ""}</div>
    <div class="m-body">${body}</div>
    ${foot ? `<div class="m-foot">${foot}</div>` : ""}</div></div>`;
  const overlay = root.firstElementChild;
  overlay.addEventListener("mousedown", (e) => { if (e.target === overlay) closeModal(); });
  const first = overlay.querySelector("input,textarea,button.primary,button");
  if (first) setTimeout(() => first.focus(), 30);
  if (wire) wire(overlay.querySelector(".modal"));
  return overlay.querySelector(".modal");
}
function closeModal() { $("modal-root").innerHTML = ""; }
document.addEventListener("keydown", (e) => { if (e.key === "Escape") { closeModal(); hideMenus(); } });

function confirmModal({title, sub, body, ok, danger}) {
  return new Promise((resolve) => {
    modal({title, sub, body, foot: `<button type="button" class="btn" data-x>取消</button><button type="button" class="btn ${danger ? "danger" : "primary"}" data-ok>${esc(ok || "确定")}</button>`,
      wire(m) {
        m.querySelector("[data-x]").onclick = () => { closeModal(); resolve(false); };
        m.querySelector("[data-ok]").onclick = () => { closeModal(); resolve(true); };
      }});
  });
}

function proxyModal(a) {
  modal({
    title: `${a.label} 的出口代理`,
    sub: "多个 Claude 号要各走一个代理，并且在代理软件里把它们分到不同节点，否则会被认成一组。",
    body: `<label class="field"><span>代理地址</span><input class="input" id="f-proxy" placeholder="http://127.0.0.1:7891" value="${esc(a.proxy || "")}" spellcheck="false"><small>就是 Clash、V2Ray 这类代理软件里的 HTTP 端口。只能是 http:// 或 https://，Claude Code 不支持 SOCKS。</small></label>`,
    foot: `${a.proxy ? `<button type="button" class="btn left" data-off>不用代理</button>` : ""}<button type="button" class="btn" data-x>取消</button><button type="button" class="btn primary" data-ok>保存</button>`,
    wire(m) {
      const save = async (v) => { if (await post("/api/proxy", {id: a.id, proxy: v})) { closeModal(); load(); } };
      m.querySelector("[data-x]").onclick = closeModal;
      m.querySelector("[data-ok]").onclick = () => save($("f-proxy").value.trim());
      m.querySelector("[data-off]")?.addEventListener("click", () => save(""));
      $("f-proxy").addEventListener("keydown", (e) => { if (e.key === "Enter") save($("f-proxy").value.trim()); });
    },
  });
}

function tokenModal(a) {
  modal({
    title: `给 ${a.label} 换上新 token`,
    sub: "粘贴 claude setup-token 最后打印的那一长串。一定要是在这个号专用的浏览器里授权拿到的。",
    body: `<label class="field"><span>token</span><textarea class="input" id="f-token" placeholder="sk-ant-oat01-…" spellcheck="false"></textarea><small>整行 export CLAUDE_CODE_OAUTH_TOKEN=… 粘进来也认得。token 只存在这台电脑上。</small></label>`,
    foot: `<button type="button" class="btn left" data-browser>${I.browser}<span>打开它的专用浏览器</span></button><button type="button" class="btn" data-x>取消</button><button type="button" class="btn primary" data-ok>保存</button>`,
    wire(m) {
      m.querySelector("[data-x]").onclick = closeModal;
      m.querySelector("[data-browser]").onclick = () => post("/api/browser", {id: a.id});
      m.querySelector("[data-ok]").onclick = async () => { if (await post("/api/token", {id: a.id, token: $("f-token").value})) { closeModal(); load(); } };
    },
  });
}

/* ---------- Claude wizard ---------- */

let setupPoll = 0;

function claudeWizard(prefill) {
  clearInterval(setupPoll);
  const others = (state?.accounts || []).filter(a => a.kind === "claude").length;
  const v = prefill || {label: "", proxy: "", token: ""};
  modal({
    title: "添加 Claude 号",
    sub: "会打开一个只给这个号用的浏览器。在里面登录、点授权，token 自动收好。",
    body: `
      <label class="field"><span>给这个号起个名字</span><input class="input" id="w-label" placeholder="比如它的邮箱，或者「工作号」" value="${esc(v.label)}"></label>
      <label class="field"><span>出口代理${others ? "" : "（可选）"}</span><input class="input" id="w-proxy" placeholder="http://127.0.0.1:7891" value="${esc(v.proxy)}" spellcheck="false">
        <small>${others ? `你已经有 ${others} 个 Claude 号。多个号走同一个出口 IP 会被认成一组，这个号最好单独走一个代理端口，并在代理软件里分到不同节点。` : "第一个号可以不填。以后加第二个号时，两个号要各走一个代理。"}</small></label>
      <div class="steps">
        <div class="step"><span class="n">1</span><div>点「开始授权」，<b>专用浏览器</b>会打开 Claude 的授权页。它和你平时用的浏览器完全分开，登录状态不会串号。</div></div>
        <div class="step"><span class="n">2</span><div>在那个窗口里<b>登录这个 Claude Pro 号</b>，点 Authorize。用邮箱登录的话，邮件里的链接要复制到那个窗口里打开。</div></div>
        <div class="step"><span class="n">3</span><div>回到这里，token 已经自动收好，大约一年有效。</div></div>
      </div>
      <div style="margin-top:14px"><button type="button" class="linkish" id="w-have">已经有这个号的 token？直接粘贴</button></div>
      <label class="field" id="w-token-field" hidden style="margin-top:10px"><span>token</span><textarea class="input" id="w-token" placeholder="sk-ant-oat01-…" spellcheck="false">${esc(v.token)}</textarea></label>`,
    foot: `<button type="button" class="btn ghost left" data-terminal>${I.terminal}<span>改用终端</span></button><button type="button" class="btn" data-x>取消</button><button type="button" class="btn primary" data-ok>开始授权</button>`,
    wide: true,
    wire(m) {
      const ok = m.querySelector("[data-ok]");
      m.querySelector("[data-x]").onclick = closeModal;
      m.querySelector("[data-terminal]").onclick = async () => { closeModal(); if (await post("/api/login", {kind: "claude"})) startWatch("claude"); };
      $("w-have").onclick = () => { const f = $("w-token-field"); f.hidden = !f.hidden; ok.querySelector("span")?.remove(); ok.textContent = f.hidden ? "开始授权" : "保存"; if (!f.hidden) $("w-token").focus(); };
      if (v.token) $("w-have").click();
      ok.onclick = async () => {
        const body = {label: $("w-label").value.trim(), proxy: $("w-proxy").value.trim(), token: $("w-token-field").hidden ? "" : $("w-token").value};
        ok.disabled = true;
        const r = await post("/api/claude/setup", body, true);
        ok.disabled = false;
        if (!r) return;
        if (r.state === "done") { wizardResult(true, r.message); load(); return; }
        wizardWaiting(r, body);
      };
    },
  });
}

function wizardWaiting(r, body) {
  modal({
    title: `正在添加 ${r.label}`,
    body: `<div class="waiting"><div class="spinner"></div><p style="margin:0 0 6px;font-weight:600">等你在专用浏览器里授权</p><p style="margin:0;color:var(--ink-2)">${esc(r.message)}</p></div>
      <div class="steps" style="margin-top:16px">
        <div class="step"><span class="n">1</span><div>在刚打开的浏览器窗口里<b>登录这个 Claude Pro 号</b>。</div></div>
        <div class="step"><span class="n">2</span><div>点 <b>Authorize</b>（授权）。页面提示可以关闭后，回到这里。</div></div>
      </div>`,
    foot: `<button type="button" class="btn" data-x>取消</button>`,
    wire(m) {
      m.querySelector("[data-x]").onclick = async () => { clearInterval(setupPoll); await post("/api/claude/setup/cancel", {}, true); closeModal(); toast("已取消。"); };
    },
  });
  clearInterval(setupPoll);
  setupPoll = setInterval(async () => {
    let s;
    try { s = await (await fetch("/api/claude/setup/status")).json(); } catch (e) { return; }
    if (!s.active || s.state === "running") return;
    clearInterval(setupPoll);
    if (s.state === "done") { wizardResult(true, s.message); load(); }
    else wizardResult(false, s.message, body);
  }, 1500);
}

function wizardResult(ok, message, body) {
  const [head, ...rest] = String(message || "").split("\n");
  modal({
    title: ok ? "加好了" : "没加成",
    body: `<div class="result ${ok ? "ok" : "bad"}"><div class="ic">${ok ? I.check : I.alert}</div><p>${esc(head)}</p>${rest.length ? `<pre>${esc(rest.join("\n"))}</pre>` : ""}</div>`,
    foot: ok
      ? `<button type="button" class="btn primary" data-x>完成</button>`
      : `<button type="button" class="btn ghost left" data-terminal>${I.terminal}<span>改用终端</span></button><button type="button" class="btn" data-x>关闭</button><button type="button" class="btn primary" data-retry>再试一次</button>`,
    wire(m) {
      m.querySelector("[data-x]").onclick = () => { closeModal(); if (ok) go("pool"); };
      m.querySelector("[data-retry]")?.addEventListener("click", () => claudeWizard(body));
      m.querySelector("[data-terminal]")?.addEventListener("click", async () => { closeModal(); if (await post("/api/login", {kind: "claude"})) startWatch("claude"); });
    },
  });
}

/* ---------- login watch (terminal flows) ---------- */

function signature(d, kind) {
  return JSON.stringify(d.accounts.filter(a => a.kind === kind).map(a => [a.id, a.email, a.reason]));
}
function startWatch(kind) {
  if (watch) clearInterval(watch.timer);
  watch = {kind, before: signature(state, kind), until: Date.now() + 5 * 60 * 1000};
  watch.timer = setInterval(() => load(true), 3000);
}
function checkWatch(d) {
  if (signature(d, watch.kind) !== watch.before) {
    toast("号池更新了。状态是「可用」就成了。");
    clearInterval(watch.timer);
    watch = null;
    if (route !== "pool") go("pool");
  } else if (Date.now() > watch.until) {
    clearInterval(watch.timer);
    watch = null;
  }
}

/* ---------- menus ---------- */

function hideMenus() {
  for (const m of document.querySelectorAll(".menu")) m.hidden = true;
  openMenu = null;
}

/* ---------- events ---------- */

document.addEventListener("click", async (e) => {
  const t = e.target.closest("button, a");
  if (!t || !t.closest("#page, #nav")) { if (!e.target.closest(".menu-wrap")) hideMenus(); return; }
  if (t.dataset.menu !== undefined) {
    const m = document.querySelector(`.menu[data-menu-for="${CSS.escape(t.dataset.menu)}"]`);
    const show = m.hidden;
    hideMenus();
    m.hidden = !show;
    openMenu = show ? t.dataset.menu : null;
    return;
  }
  if (!e.target.closest(".menu")) hideMenus();
  if (t.dataset.go) { go(t.dataset.go); return; }
  if (t.hasAttribute("data-reload")) { load(); toast("已刷新。"); return; }
  if (t.dataset.days) {
    usageDays = +t.dataset.days;
    localStorage.setItem("gcc.usageDays", usageDays);
    usageError = "";
    render();
    if (!usageData[usageDays]) loadUsage(); else loadUsage(true);
    return;
  }
  if (t.hasAttribute("data-reload-usage")) { usageError = ""; render(); loadUsage(); return; }
  if (t.hasAttribute("data-cli")) { if (await post("/api/cli", {})) load(); return; }
  if (t.dataset.add) {
    if (t.dataset.add === "claude") { claudeWizard(); return; }
    if (await post("/api/login", {kind: t.dataset.add})) startWatch(t.dataset.add);
    return;
  }
  if (t.dataset.import) { if (await post("/api/import", {kind: t.dataset.import})) load(); return; }
  const a = accountOf(t.dataset.id);
  if (!t.dataset.act || !a) return;
  hideMenus();
  switch (t.dataset.act) {
  case "use":
    if (await post("/api/use", {kind: a.kind, id: a.id}, true)) { toast(`以后 ${names()[a.kind] || a.kind} 固定用 ${a.label}。想恢复自动挑号，到「设置」里选「自动」。`); load(); }
    break;
  case "unquarantine":
    if (await post("/api/unquarantine", {id: a.id}, true)) { toast(`已解除 ${a.label} 的隔离。`); load(); }
    break;
  case "enable":
  case "disable":
    if (await post("/api/enable", {id: a.id, enabled: t.dataset.act === "enable"})) load();
    break;
  case "login":
    if (a.kind === "claude" && a.token) { tokenRenew(a); break; }
    if (await post("/api/login", {id: a.id})) startWatch(a.kind);
    break;
  case "proxy": proxyModal(a); break;
  case "browser": await post("/api/browser", {id: a.id}); break;
  case "token": tokenModal(a); break;
  case "remove":
    if (await confirmModal({title: `从号池移除 ${a.label}？`, sub: "只是以后不再用这个号，聊天记录不会删除。" + (a.token ? "本机存的 token 和它的专用浏览器也会删掉。" : ""), body: "", ok: "移除", danger: true})) {
      if (await post("/api/remove", {id: a.id})) load();
    }
    break;
  }
});

// tokenRenew: a token account signs in again by taking a fresh token. The
// terminal flow does that interactively; the panel offers paste or terminal.
function tokenRenew(a) {
  modal({
    title: `给 ${a.label} 续期`,
    sub: "重新拿一个一年期 token。两种方式都行。",
    body: `<div class="steps">
      <div class="step"><span class="n">1</span><div><b>在终端里续期</b>：会弹出终端窗口，在它的专用浏览器里重新授权，token 自动换上。</div></div>
      <div class="step"><span class="n">2</span><div><b>直接粘贴</b>：你已经在别处运行过 claude setup-token，把打印出来的 token 粘进来。</div></div></div>`,
    foot: `<button type="button" class="btn" data-x>取消</button><button type="button" class="btn" data-paste>${I.key}<span>粘贴 token</span></button><button type="button" class="btn primary" data-term>${I.terminal}<span>在终端里续期</span></button>`,
    wire(m) {
      m.querySelector("[data-x]").onclick = closeModal;
      m.querySelector("[data-paste]").onclick = () => tokenModal(a);
      m.querySelector("[data-term]").onclick = async () => { closeModal(); if (await post("/api/login", {id: a.id})) startWatch(a.kind); };
    },
  });
}

document.addEventListener("change", async (e) => {
  const el = e.target;
  if (el.name === "strategy") {
    if (await post("/api/strategy", {strategy: el.value}, true)) { toast("换号方式已改好，下次启动时生效。"); state.strategy = el.value; syncSettings(state); }
    else syncSettings(state);
  } else if (el.id === "continue") {
    if (!await post("/api/continue", {enabled: el.checked})) load();
  } else if (el.id === "statusline") {
    if (!await post("/api/statusline", {enabled: el.checked})) load();
  } else if (el.id === "relay") {
    if (await post("/api/relay", {enabled: el.checked})) load(); else load();
  }
});

window.addEventListener("hashchange", () => { const r = location.hash.replace("#", "") || "pool"; if (r !== route) { route = r; render(); } });

load();
setInterval(() => load(true), 15000);
