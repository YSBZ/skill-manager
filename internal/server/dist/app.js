"use strict";
const TOKEN = document.querySelector('meta[name="sm-token"]').content;
const $ = (s, el = document) => el.querySelector(s);
const ce = (t, props = {}) => Object.assign(document.createElement(t), props);

// Instant custom tooltip — the native `title` reveal is too slow. Shown only
// for descriptions that are actually truncated, follows the cursor, and is
// pointer-events:none so it never interferes with hover/click detection.
const tip = ce("div", { className: "tip hidden" });
document.body.append(tip);
function placeTip(x, y) {
  const pad = 14;
  let left = x + pad, top = y + pad;
  if (left + tip.offsetWidth > window.innerWidth - 8) left = x - tip.offsetWidth - pad;
  if (top + tip.offsetHeight > window.innerHeight - 8) top = y - tip.offsetHeight - pad;
  tip.style.left = Math.max(8, left) + "px";
  tip.style.top = Math.max(8, top) + "px";
}
document.addEventListener("mouseover", (e) => {
  const el = e.target.closest && e.target.closest(".skill-desc");
  if (!el || el.scrollHeight <= el.clientHeight + 1) return; // not truncated → no tip
  tip.textContent = el.textContent;
  tip.classList.remove("hidden");
  placeTip(e.clientX, e.clientY);
});
document.addEventListener("mousemove", (e) => {
  if (!tip.classList.contains("hidden")) placeTip(e.clientX, e.clientY);
});
document.addEventListener("mouseout", (e) => {
  if (e.target.closest && e.target.closest(".skill-desc")) tip.classList.add("hidden");
});

async function api(method, path, body, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Authorization", "Bearer " + TOKEN);
  const opts = { method, headers };
  if (body !== undefined) {
    headers.set("Content-Type", "application/json");
    opts.body = JSON.stringify(body);
  }
  const r = await fetch(path, opts);
  if (r.status === 304 && options.withMeta) {
    return { status: 304, data: null, etag: r.headers.get("ETag") || "" };
  }
  const txt = await r.text();
  let data = null;
  if (txt) { try { data = JSON.parse(txt); } catch { data = txt; } }
  if (!r.ok) {
    const payload = data && typeof data === "object" ? data : null;
    const code = payload && (payload.error_code || payload.code);
    const message = (payload && (payload.error || payload.error_code)) || r.statusText || "HTTP " + r.status;
    throw Object.assign(new Error(message), { status: r.status, code, error_code: code });
  }
  return options.withMeta
    ? { status: r.status, data, etag: r.headers.get("ETag") || "" }
    : data;
}

// openExternal opens a URL in the system browser. In the plain browser daemon
// window.open works; in the desktop WKWebView it no-ops (returns null), so we
// fall back to the backend opener (browser.Open on the host).
async function openExternal(url) {
  if (!url) return;
  let win = null;
  try { win = window.open(url, "_blank", "noopener"); } catch { /* WKWebView throws/no-ops */ }
  if (win) return;
  try {
    await api("POST", "/api/open", { url });
  } catch (e) {
    banner("打开链接失败：" + e.message, true);
  }
}

const nodeDependencyUI = window.SkillManagerNodeDependency?.createController(document, openExternal) || null;
window.SkillManagerNodeDependencyUI = nodeDependencyUI;

// Any external http(s) link — including the static ones in the in-app guide —
// must open in the system browser, never navigate THIS view. In the desktop
// WKWebView a plain <a href> click replaces the whole app with the target site
// and there is no browser chrome to go back (the reported bug). Delegate every
// such click to openExternal; same behavior in the plain browser (opens a tab).
document.addEventListener("click", (e) => {
  const a = e.target.closest && e.target.closest("a[href]");
  if (!a) return;
  const href = a.getAttribute("href") || "";
  if (/^https?:\/\//i.test(href)) {
    e.preventDefault();
    openExternal(href);
  }
});

// confirmModal replaces the native confirm() with an in-page dialog. Returns a
// Promise<bool>. okText/danger customize the confirm button.
function confirmModal(msg, okText, danger) {
  return new Promise((resolve) => {
    const m = $("#confirm-modal"), ok = $("#confirm-ok"), cancel = $("#confirm-cancel");
    $("#confirm-msg").textContent = msg;
    ok.textContent = okText || "确定";
    ok.className = danger ? "danger" : "";
    m.classList.remove("hidden");
    const done = (v) => { m.classList.add("hidden"); ok.onclick = cancel.onclick = m.onclick = null; resolve(v); };
    ok.onclick = () => done(true);
    cancel.onclick = () => done(false);
    m.onclick = (e) => { if (e.target.id === "confirm-modal") done(false); };
  });
}

// infoModal shows a read-only explanation popup. paras is an array of
// {h?, text} — h renders an emphasized lead line, text the body paragraph.
function infoModal(title, paras) {
  $("#info-title").textContent = title;
  const body = $("#info-body"); body.innerHTML = "";
  paras.forEach((p) => {
    const el = ce("p");
    if (p.h) el.append(ce("b", { textContent: p.h + " " }));
    el.append(document.createTextNode(p.text || ""));
    body.append(el);
  });
  $("#info-modal").classList.remove("hidden");
}

const state = {
  status: null,
  targets: [],
  npxAvailable: false,
  credHosts: {}, // host → username for hosts with a stored HTTPS credential
  credPending: null, // {url, branch} when filling creds before adding a repo
  skillsByRepo: {}, // catalog for the "+ 添加" drawer
  inventory: [], // current tab's directory inventory (phase 3 U6)
  pluginSkills: [], // plugin-provided skills (harness-tagged), injected into inventory
  pluginInstalled: null, // null=未加载；{available, list:[{name,id,scope}]} 已装插件（用于委托更新）
  pluginInstalledLoading: false,
  invScope: "",
  invLoading: false,
  invError: "",
  collapsedGroups: { plugin: true }, // 目录现状各来源分组的「已收起」集合（key→true）。默认除「插件」外全部展开（插件量大且全局只读，默认收起）；各组独立切换。
  dirSources: [], // 用户登记的本地目录源（status.localSources）：[{id,label,path,count}]
  asideTab: "sources", // 左栏分段：sources（来源）| presets（分组）
  collapsed: new Set(), // 来源分区折叠状态（key：git/npx/local），持久化到 localStorage 去拥挤
  presets: [], // 分组（GET /api/presets）：[{name, skills:[{skill,mode}]}]
  activeTarget: undefined, // active 同步目录标签页 (one tab per dir)
  search: "",
  searchFold: { local: false, online: false }, // 搜索结果两区各自折叠状态（false=展开）
  onlineSrc: "both", // 在线结果来源筛选：both | skillssh | skillsmp（齿轮弹窗，持久化）
  onlineSort: "desc", // 在线结果排序：default | desc | asc（按下载/星，默认热度↓，齿轮弹窗，持久化）
  onlineLimit: 0, // 在线结果数量限制：0=全部 | 10 | 20 | 50（齿轮弹窗，持久化）
  // 在线搜索结果独立 state——只由显式触发更新，不挂在每次按键的渲染上。
  // gen 是世代计数器：触发时 +1，迟到的旧响应若 gen 不符则丢弃（防 stale 覆盖 fresh）。
  skillsShOnline: { term: "", loading: false, available: true, results: [], error: "", errSh: "", errMp: "", errHub: "", gen: 0 },
};

// 安装并发锁：同一时刻只允许一个 npx skills add 在跑，其它安装按钮禁用。
let installingPkg = null;

// error_code → 用户文案（收编不同失败点处置不同）
const ADOPT_ERR = {
  copy_failed: "原 skill 未动，请检查磁盘空间或权限",
  verify_failed: "原 skill 未动，复制不完整",
  link_failed: "已自动回滚，原 skill 完好",
  rollback_partial: "复制已入库但建链失败，请重试收编",
  invalid: "非法 skill 名",
  guarded: "受保护目录，不可收编",
  not_found: "skill 不存在",
  name_taken: "受管存储已有同名 skill（另一 agent 收编过），请先改名",
};

// 贡献到 git 仓 / 快捷上传 的 error_code → 用户文案。push 失败诚实分类（凭据无写
// 权限 / 非快进 / 网络），本地提交一律保留，可后续「快捷上传」重试。
const CONTRIB_ERR = {
  push_auth: "推送被拒，可能凭据无写权限——本地提交已保留，请配置有写权限的凭据后用「快捷上传」重试",
  push_rejected: "推送被拒（远端有新提交）——请先「更新」同步远端后再用「快捷上传」重试，本地提交已保留",
  push_network: "推送失败（网络错误）——本地提交已保留，可稍后用「快捷上传」重试",
  push_failed: "推送失败——本地提交已保留，可稍后用「快捷上传」重试",
  repo_missing: "目标仓尚未克隆，请先「更新」该仓",
  no_git: "git 不可用，无法贡献",
  invalid: "非法的仓名或 skill 名",
  not_found: "该 skill 不在仓内",
  name_taken: "目标仓已存在同名 skill，请改名后再贡献",
  copy_failed: "原 skill 未动，请检查磁盘空间或权限",
  verify_failed: "原 skill 未动，复制不完整",
  secrets_blocked: "含疑似密钥/凭据文件，已拦截。请在仓「同步仓库」弹窗里核对并勾选确认后再推，或先从仓里移除这些文件",
  branch_failed: "无法解析推送分支",
  save_failed: "配置保存失败",
};

const harnessClass = (h) => (h === "codex" ? "st-linked-codex" : h === "cc" ? "st-cc" : h === "pi" ? "st-pi" : "st-unknown");
const LOCAL_NS = "@local";
const AGENTS_NS = "@agents"; // skills.sh shared dir namespace (~/.agents/skills)
const SKILLHUB_NS = "@skillhub"; // SkillHub registry store namespace (managed store)
const dirNS = (id) => "@dir:" + id; // user-registered local directory source namespace

// SRC maps a classified source kind to its badge label + CSS class (phase 3 U8).
const SRC = {
  git: { label: "git", cls: "src-git" },
  local: { label: "本地", cls: "src-local" },
  dir: { label: "本地源", cls: "src-local" },
  "skills.sh": { label: "skills.sh", cls: "src-skillssh" },
  plugin: { label: "插件", cls: "src-plugin" },
  handwritten: { label: "未备份", cls: "src-handwritten" },
  unknown: { label: "未知软链", cls: "src-unknown" },
};

let bannerTimer = null;
function banner(msg, isErr, sticky) {
  const b = $("#banner");
  if (bannerTimer) { clearTimeout(bannerTimer); bannerTimer = null; }
  if (!msg) { b.classList.add("hidden"); delete b.dataset.sticky; return; }
  b.textContent = msg;
  b.title = "点击关闭";
  b.className = "banner" + (isErr ? " err" : "");
  if (sticky) b.dataset.sticky = "1"; else delete b.dataset.sticky;
  b.onclick = () => { b.classList.add("hidden"); delete b.dataset.sticky; };
  // 浮层提示自动消失，避免长期遮挡（错误停留更久）。点击可立即关闭。
  // sticky=true 时不自动消失（需点击关闭）——用于需要看清/截图的报错；load() 也不会清它。
  if (!sticky) bannerTimer = setTimeout(() => b.classList.add("hidden"), isErr ? 8000 : 4000);
}

// toast shows a transient confirmation (build/teardown link feedback, R4.2).
let toastTimer = null;
// toast shows a transient top-center notice. kind ∈ ok|err|info (default ok)
// drives the color (green / red / blue).
function toast(msg, kind) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = "toast toast-" + (kind || "ok"); // rebuilds class → also clears "hidden"
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.add("hidden"), 3200);
}

// --- 意见反馈 ---
const feedbackImages = []; // {file, url}

function renderFeedbackThumbs() {
  const box = $("#feedback-thumbs"); box.innerHTML = "";
  feedbackImages.forEach((im, i) => {
    const wrap = ce("span", { className: "fb-thumb" });
    const img = ce("img"); img.src = im.url; wrap.append(img);
    const x = ce("button", { className: "fb-thumb-x ghost", textContent: "×", title: "移除" });
    x.onclick = () => { URL.revokeObjectURL(im.url); feedbackImages.splice(i, 1); renderFeedbackThumbs(); };
    wrap.append(x);
    box.append(wrap);
  });
}
function addFeedbackImages(files) {
  for (const f of files) {
    if (!f.type.startsWith("image/")) continue;
    if (feedbackImages.length >= 5) { toast("截图最多 5 张", "err"); break; }
    if (f.size > 5 * 1024 * 1024) { toast("单张截图不能超过 5MB", "err"); continue; }
    feedbackImages.push({ file: f, url: URL.createObjectURL(f) });
  }
  renderFeedbackThumbs();
}
function clearFeedbackImages() {
  feedbackImages.splice(0).forEach((im) => URL.revokeObjectURL(im.url));
  renderFeedbackThumbs();
}
function openFeedback() {
  $("#feedback-text").value = "";
  clearFeedbackImages();
  $("#feedback-modal").classList.remove("hidden");
  $("#feedback-text").focus();
}
function closeFeedback() {
  $("#feedback-modal").classList.add("hidden");
  clearFeedbackImages();
}
async function submitFeedback() {
  const text = $("#feedback-text").value.trim();
  if (!text) { toast("请先写点意见", "err"); return; }
  const btn = $("#feedback-submit"); btn.disabled = true;
  try {
    const fd = new FormData();
    fd.append("text", text);
    feedbackImages.forEach((im) => fd.append("images", im.file, im.file.name || "screenshot.png"));
    const r = await fetch("/api/feedback", { method: "POST", headers: { Authorization: "Bearer " + TOKEN }, body: fd });
    if (!r.ok) { const d = await r.json().catch(() => ({})); throw new Error(d.error || ("HTTP " + r.status)); }
    toast("已提交，谢谢你的反馈！");
    closeFeedback();
  } catch (e) {
    toast("提交失败：" + e.message, "err");
  } finally {
    btn.disabled = false;
  }
}
$("#feedback-btn").onclick = openFeedback;
$("#feedback-close").onclick = closeFeedback;
$("#feedback-cancel").onclick = closeFeedback;
$("#feedback-submit").onclick = submitFeedback;
$("#feedback-file").onchange = (e) => { addFeedbackImages(e.target.files); e.target.value = ""; };
$("#feedback-text").addEventListener("paste", (e) => {
  const imgs = [...((e.clipboardData && e.clipboardData.items) || [])]
    .filter((it) => it.type.startsWith("image/"))
    .map((it) => it.getAsFile())
    .filter(Boolean);
  if (imgs.length) { e.preventDefault(); addFeedbackImages(imgs); }
});

function hostOf(u) { try { return new URL(u).hostname; } catch { return u; } }
function targetLabel(dir) {
  const t = state.targets.find((x) => x.dir === dir);
  return (t && t.alias) || dir;
}

// shortPath 头部省略、保留尾部路径段（尾巴最有信息量：项目名/.claude/skills）。
// 别名（如 cc）本就短，原样返回；只在超长时截。完整值由调用方放进 title 供 hover。
function shortPath(p, max = 34) {
  if (!p || p.length <= max) return p;
  const segs = p.split("/").filter(Boolean);
  let out = segs.pop() || "";
  while (segs.length) {
    const next = segs[segs.length - 1] + "/" + out;
    if (next.length + 2 > max) break; // +2 预留前缀「…/」
    out = next; segs.pop();
  }
  return "…/" + out;
}

const targetDirs = () => state.targets.map((t) => t.dir);
// currentTarget = the active tab's directory (each tab is one sync dir and keeps
// its own skill→dir mapping). Falls back to the first directory.
const currentTarget = () => {
  const dirs = targetDirs();
  if (state.activeTarget && dirs.includes(state.activeTarget)) return state.activeTarget;
  return dirs[0] || "";
};

const enabledFollow = (repo) =>
  (state.status.enabled || []).some((e) => e.skill === repo + "/*" && e.target === currentTarget());

// lastSig is the fingerprint of everything the UI currently shows on screen,
// recorded at the end of every load(). The background auto-sync (autoSyncTick)
// compares a fresh probe against it to decide whether the disk changed.
let lastSig = "";
let autoSyncing = false; // reentrancy guard for the background probe

// load refreshes all UI state from the daemon. Pass sync=true to first run a live
// reconcile so the footer reflects current reality (resolved skips/conflicts clear,
// newly-unblocked links get placed) instead of a stale last-sync snapshot.
async function load(sync) {
  if (sync) { try { await api("POST", "/api/apply"); } catch { /* status still loads */ } }
  try { state.status = await api("GET", "/api/status"); }
  catch (e) { banner("加载失败：" + e.message, true); return; }
  nodeDependencyUI?.setStatus(state.status);
  window.SkillManagerContent?.onMainStatus(state.status);
  state.npxAvailable = !!state.status.npxAvailable;
  $("#feedback-btn").classList.toggle("hidden", !state.status.feedback);
  if (state.status.version) { // set only the tab/window title; header no longer shows a version
    document.title = "SkillManager v" + state.status.version;
  }
  try { state.targets = (await api("GET", "/api/targets")) || []; }
  catch { state.targets = []; }
  try {
    const list = (await api("GET", "/api/credentials")) || [];
    state.credHosts = {};
    list.forEach((c) => { state.credHosts[c.host] = c.username || ""; });
  } catch { state.credHosts = {}; }
  const repos = state.status.repos || [];
  state.dirSources = state.status.localSources || []; // user-registered local folder sources
  // Catalog for the "管理" drawer: tracked-repo skills, the @local store, and each
  // registered local directory source ("@dir:<id>").
  const names = repos.map((r) => r.name)
    .concat(LOCAL_NS)
    .concat(state.dirSources.map((d) => "@dir:" + d.id));
  const entries = await Promise.all(names.map(async (name) => {
    try { return [name, (await api("GET", "/api/skills?repo=" + encodeURIComponent(name))) || []]; }
    catch { return [name, []]; }
  }));
  state.skillsByRepo = Object.fromEntries(entries);
  // 插件 skill（按 harness 分类，注入到「目录现状」底部分组）。
  try { state.pluginSkills = (await api("GET", "/api/plugins")) || []; }
  catch { state.pluginSkills = []; }
  // skills.sh skill（第三方，可在「+ 添加」里链接进目标，selector 用 @agents/<name>）。
  try { state.skillsShSkills = (await api("GET", "/api/skillssh")) || []; }
  catch { state.skillsShSkills = []; }
  // SkillHub registry skill（装进受管 store，可在 @skillhub 命名空间下链接进目标）。
  try { state.skillHubSkills = (await api("GET", "/api/skillhub")) || []; }
  catch { state.skillHubSkills = []; }
  await refreshPresets();
  renderStats(); renderRepos(); renderTabs(); renderSummary();
  if (state.asideTab === "presets") renderPresets();
  await fetchInventory();
  // Record the just-rendered fingerprint so the background auto-sync can tell a
  // real out-of-band disk change from "nothing moved" and skip repainting otherwise.
  lastSig = computeSig({
    repos: state.status.repos, localSources: state.dirSources,
    skillsByRepo: state.skillsByRepo, pluginSkills: state.pluginSkills,
    skillsShSkills: state.skillsShSkills, skillHubSkills: state.skillHubSkills, inventory: state.inventory,
  });
  if (state.status.gitError) {
    banner("未检测到 git：" + state.status.gitError + "。请安装 Git 并确保在 PATH 中，然后重启本工具——否则无法拉取/更新仓库。", true);
  } else if (repos.length === 0 && state.targets.length === 0) {
    banner("还没有仓库。在左侧添加一个 git skill 仓开始。");
  } else if ($("#banner").dataset.sticky !== "1") {
    // 清掉 load 自己的引导提示，但绝不清掉刚弹出的常驻错误（同步/上传失败等）——
    // 之前这里无条件 banner("") 把紧接其后的错误条一闪即灭，根本看不清。
    banner("");
  }
}

// computeSig builds a stable, order-independent fingerprint of everything the UI
// surfaces: tracked repos (name+state+dirty), registered local sources, every
// source's skills (name+version+description — so an in-place SKILL.md meta edit
// is caught), plugin skills, npx skills, and the current tab's inventory. It
// changes iff something the UI actually displays changed; intra-list reordering
// does not trip it (tokens are sorted). Used purely to gate repaints.
function computeSig(p) {
  const skillTok = (arr) => (arr || [])
    .map((s) => (s.name || s.skill || "") + "|" + (s.version || "") + "|" + (s.description || ""))
    .sort().join(",");
  const repoTok = (p.repos || [])
    .map((r) => r.name + "|" + r.state + "|" + (r.drift && r.drift.dirty ? 1 : 0)).sort().join(",");
  const srcTok = (p.localSources || [])
    .map((d) => d.id + "|" + (d.path || d.dir || "")).sort().join(",");
  const byRepo = Object.keys(p.skillsByRepo || {}).sort()
    .map((k) => k + ":" + skillTok(p.skillsByRepo[k])).join(";");
  const invTok = (p.inventory || [])
    .map((i) => (i.name || "") + "|" + (i.kind || "") + "|" + (i.source || i.ns || "") + "|" + (i.version || "") + "|" + (i.enabled ? 1 : 0))
    .sort().join(",");
  return [repoTok, srcTok, byRepo, skillTok(p.pluginSkills), skillTok(p.skillsShSkills), skillTok(p.skillHubSkills), invTok].join("§");
}

// fetchSnapshot pulls the same change-sensitive data load() reads, WITHOUT
// committing it to state or rendering — the background probe uses it to compute
// a fingerprint quietly. Mirrors load()'s source-name construction exactly so
// its fingerprint lines up with the one load() records in lastSig.
async function fetchSnapshot() {
  const status = await api("GET", "/api/status");
  const repos = status.repos || [];
  const localSources = status.localSources || [];
  const names = repos.map((r) => r.name).concat(LOCAL_NS).concat(localSources.map((d) => "@dir:" + d.id));
  const entries = await Promise.all(names.map(async (name) => {
    try { return [name, (await api("GET", "/api/skills?repo=" + encodeURIComponent(name))) || []]; }
    catch { return [name, []]; }
  }));
  const skillsByRepo = Object.fromEntries(entries);
  let pluginSkills = [], skillsShSkills = [], skillHubSkills = [], inventory = [];
  try { pluginSkills = (await api("GET", "/api/plugins")) || []; } catch { /* probe stays best-effort */ }
  try { skillsShSkills = (await api("GET", "/api/skillssh")) || []; } catch { /* idem */ }
  try { skillHubSkills = (await api("GET", "/api/skillhub")) || []; } catch { /* idem */ }
  const t = currentTarget();
  if (t) {
    try { inventory = ((await api("GET", "/api/inventory?target=" + encodeURIComponent(t))).items) || []; }
    catch { /* idem */ }
  }
  return { repos, localSources, skillsByRepo, pluginSkills, skillsShSkills, skillHubSkills, inventory };
}

// autoSyncTick is the background auto-sync. Users may add/remove/edit local skill
// files outside the tool; this keeps the view honest without a manual click. It
// probes the disk fingerprint and repaints ONLY on a real change — so an idle
// tick is invisible (no flicker, no scroll jump). It never fires while a dialog
// is open (don't yank the UI mid-action / mid-typing) or while the tab is hidden,
// and swallows every error (passive — just try again next tick).
async function autoSyncTick() {
  if (document.hidden) return;
  if (document.querySelector(".modal:not(.hidden)")) return;
  if (autoSyncing) return;
  autoSyncing = true;
  try {
    const snap = await fetchSnapshot();
    if (computeSig(snap) !== lastSig) await load(); // changed on disk → one clean full refresh
  } catch { /* passive poll — ignore and retry on the next tick */ }
  finally { autoSyncing = false; }
}

function renderStats() {
  const el = $("#stats");
  const repos = (state.status.repos || []).length;
  // 「收录」= SkillManager 一共管控/识别了多少个 skill，跨全部源汇总：git 源 + 本地源
  // （@local 受管存储 + @dir 登记目录，都在 skillsByRepo 里）+ npx skills.sh 源。
  // 比「某个目录里链接了几个」更能体现这个工具的整体盘子。
  let catalog = 0;
  for (const arr of Object.values(state.skillsByRepo || {})) catalog += (arr || []).length;
  const npx = (state.skillsShSkills || []).length;
  const total = catalog + npx;
  // 顶部「冲突」是库内跨源体检：名称重复 + 关键词重叠（与页脚「健康度」是两件事）。
  const cf = computeLibraryConflicts();
  const cfN = cf.dups.length + cf.overlaps.length;
  el.innerHTML = "";
  el.append(ce("span", { innerHTML: `仓库 <b>${repos}</b>`, title: "已登记的 git 仓数量" }));
  el.append(ce("span", { innerHTML: `收录 skills <b>${total}</b>`, title: `SkillManager 一共收录管控的 skill 数：git 源 + 本地源 ${catalog} 个，npx(skills.sh) 源 ${npx} 个。` }));
  const c = ce("span", { innerHTML: `${cfN ? '<span class="conflict-stat-icon" aria-hidden="true"></span>' : ''}冲突 <b>${cfN || 0}</b>`, title: "跨源体检：名称重复 / 关键词重叠——点击查看" });
  c.className = (cfN ? "stat-warn stat-conflict " : "") + "clickable-stat";
  c.setAttribute("role", "button");
  c.tabIndex = 0;
  c.onclick = openConflictModal;
  c.onkeydown = (event) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      openConflictModal();
    }
  };
  el.append(c);
}

// REPO_STATE localizes each repo sync state to a Chinese badge label + class.
// "stale" = cloned earlier but not pulled this session (auto-pull was removed),
// so it shows 未更新 rather than the misleading 未克隆.
const REPO_STATE = {
  "cloned": { label: "已克隆", cls: "ok" },
  "synced": { label: "已同步", cls: "ok" },
  "stale": { label: "未更新", cls: "never-synced" },
  "never-synced": { label: "未克隆", cls: "never-synced" },
  "dirty-skip": { label: "有本地改动", cls: "dirty-skip" },
  "failed": { label: "失败", cls: "failed" },
  "cloning": { label: "克隆中", cls: "cloning" },
  "sync-in-progress": { label: "同步中", cls: "sync-in-progress" },
};
function stateBadge(st) {
  const m = REPO_STATE[st] || { label: st, cls: "" };
  return ce("span", { className: "badge " + m.cls, textContent: m.label });
}

// repoDot classifies a repo into a connectivity/usability dot color: green =
// cloned & usable (its skills can be linked, even if not pulled this session),
// red = auth/connection failure, grey = never cloned yet or in progress. Note
// freshness ("未更新") is shown by the state BADGE, not the dot — a cloned repo
// is green regardless of whether it was pulled this session.
function repoDot(repo) {
  const st = repo.state || "never-synced";
  if (repo.error || st === "failed") return "err";
  if (st === "never-synced" || st === "cloning" || st === "sync-in-progress") return "idle";
  return "ok"; // cloned / synced / stale(已克隆未更新) / dirty-skip → usable → green
}

function httpsHost(u) {
  try { if (!/^https?:\/\//i.test(u)) return ""; return new URL(u).hostname; } catch { return ""; }
}

function openCredModal(host, username, pending) {
  $("#cred-host").textContent = host;
  $("#cred-host").dataset.host = host;
  $("#cred-user").value = username || "";
  $("#cred-token").value = "";
  state.credPending = pending || null;
  $("#cred-skip").classList.toggle("hidden", !pending);
  $("#cred-modal").classList.remove("hidden");
  $("#cred-user").focus();
}
function closeCredModal() {
  state.credPending = null;
  $("#cred-skip").classList.add("hidden");
  $("#cred-modal").classList.add("hidden");
}

// sourceDivider builds a sidebar section header. actions (optional) render on the
// right, after a filler line, so each source type's own controls live in its label.
function sourceDivider(label, actions, afterLabel) {
  const has = (actions && actions.length) || afterLabel;
  const d = ce("li", { className: "repo-divider" + (has ? " with-actions" : "") });
  d.append(ce("span", { textContent: label }));
  if (afterLabel) d.append(afterLabel); // 紧跟标签（如 git 仓的 ? 说明）
  if (has) {
    d.append(ce("div", { className: "divider-line" }));
    if (actions) actions.forEach((b) => d.append(b));
  }
  return d;
}

// 来源分区折叠状态：持久化到 localStorage，默认全部展开、各分区独立切换（去拥挤）。
function loadCollapsed() {
  try { (JSON.parse(localStorage.getItem("sm.collapsedSources") || "[]") || []).forEach((k) => state.collapsed.add(k)); }
  catch { /* localStorage 不可用时忽略 */ }
}
function saveCollapsed() {
  try { localStorage.setItem("sm.collapsedSources", JSON.stringify([...state.collapsed])); } catch { /* 忽略 */ }
}

// sectionHead appends a collapsible source-section header to ul and returns true
// when the section is collapsed (caller then skips its body cards). Header action
// buttons keep working — clicking them does not toggle the section.
function sectionHead(ul, key, label, actions, afterLabel) {
  const collapsed = state.collapsed.has(key);
  const d = sourceDivider(label, actions, afterLabel);
  d.classList.add("section-head");
  d.prepend(ce("span", { className: "sec-caret", textContent: collapsed ? "▸" : "▾" }));
  d.onclick = (e) => {
    if (e.target.closest("button")) return; // 标题行里的动作按钮保留各自行为
    if (state.collapsed.has(key)) state.collapsed.delete(key); else state.collapsed.add(key);
    saveCollapsed();
    renderRepos();
  };
  ul.append(d);
  return collapsed;
}

function renderRepos() {
  const ul = $("#repo-list"); ul.innerHTML = "";
  // 三类源平级展示：git 仓 / npx skills / 本地源（都是「源」）。每类一个可折叠分区，
  // 该来源专属的动作放在标题行右侧；折叠状态持久化以去拥挤。
  // —— git 仓 —— 标题行：全量更新（仅 git）。说明统一在「使用指南」里。
  const gitFullUpdate = ce("button", { className: "ghost small", textContent: "全量更新", title: "更新全部 git 仓：拉取上游并重新同步（不含 npx）；有本地改动会提示是否还原并更新" });
  gitFullUpdate.onclick = (e) => { e.stopPropagation(); updateNow(false); };
  if (!sectionHead(ul, "git", "git 仓", [gitFullUpdate])) {
  // 添加行：导出 / 导入 / 添加（「添加」点击打开弹窗输入 URL + 分支）。
  const addRow = ce("li", { className: "repo-addrow" });
  const gitExport = ce("button", { className: "ghost small", textContent: "导出", title: "导出 git 仓库列表（用于在另一台机器重建来源）" });
  gitExport.onclick = exportRepos;
  const gitImport = ce("button", { className: "ghost small", textContent: "导入", title: "从文件导入 git 仓库列表" });
  gitImport.onclick = importRepos;
  const addBtn = ce("button", { className: "small", textContent: "添加", title: "添加一个 git 仓作为源（弹窗输入地址与分支）" });
  addBtn.onclick = openGitRepoModal;
  // 导入 / 导出 靠左，添加 靠右（中间弹性间隔推开）。
  addRow.append(gitImport, gitExport, ce("span", { className: "group-spacer" }), addBtn);
  ul.append(addRow);
  (state.status.repos || []).forEach((repo) => {
    const host = httpsHost(repo.url);
    // 整张卡片可点击打开「该仓库的 skill」弹窗；卡片内的按钮/圆点自行 stopPropagation。
    const li = ce("li", { className: "repo-card clickable", title: "查看该仓库内的 skill" });
    li.onclick = () => openRepoSkills(repo.name, { git: true });
    const top = ce("div", { className: "repo-top" });
    const dotKind = repoDot(repo);
    const dot = ce("span", { className: "repo-dot " + dotKind });
    if (dotKind === "err" && host) {
      dot.title = "连接/鉴权失败，点击重填凭据";
      dot.classList.add("clickable");
      dot.onclick = (e) => { e.stopPropagation(); openCredModal(host, state.credHosts[host] || ""); };
    } else {
      dot.title = dotKind === "ok" ? "上次同步成功" : dotKind === "err" ? "上次同步失败" : "尚未同步";
    }
    const nameSpan = ce("span", { className: "repo-name", textContent: repo.alias || repo.name, title: repo.alias ? ("仓库名：" + repo.name + "（双击改别名）") : "双击设别名，便于区分多个仓库" });
    nameSpan.ondblclick = (e) => { e.stopPropagation(); startRepoAliasEdit(repo, nameSpan); };
    top.append(dot, nameSpan);
    const rename = ce("button", { className: "ghost small repo-rename", textContent: "✎", title: "设别名（便于区分多个仓库）", style: "padding:0 6px;line-height:1" });
    rename.onclick = (e) => { e.stopPropagation(); startRepoAliasEdit(repo, nameSpan); };
    top.append(rename);
    // 「有更新」（上游领先）与 state 徽章语义重叠：hasUpdate 时只显示可动作的「有更新」，
    // 否则显示 state（已同步 / 未更新 / 未同步），避免「未更新」+「有更新」同时出现看着矛盾。
    if (repo.hasUpdate) {
      top.append(ce("span", { className: "badge has-update", textContent: "有更新", title: "上游有新提交，点「同步仓库」拉取" }));
    } else {
      top.append(stateBadge(repo.state || "never-synced"));
    }
    if (repo.alias) top.append(ce("span", { className: "muted", style: "font-size:11px;opacity:.6", textContent: repo.name }));
    li.append(top);
    li.append(ce("div", { className: "repo-url", textContent: repo.url }));
    const meta = ce("div", { className: "repo-meta" });
    const n = (state.skillsByRepo[repo.name] || []).length;
    meta.append(ce("span", { className: "badge count", textContent: n + " skill" }));
    if (host) {
      const has = Object.prototype.hasOwnProperty.call(state.credHosts, host);
      const cb = ce("button", { className: "ghost small", textContent: has ? "凭据✓" : "填写凭据", title: has ? ("已为 " + host + " 配置凭据，点此重填") : ("为私有仓 " + host + " 填写 HTTPS 令牌") });
      cb.onclick = (e) => { e.stopPropagation(); openCredModal(host, state.credHosts[host] || ""); };
      meta.append(cb);
    }
    meta.append(ce("span", { className: "group-spacer" }));
    const rm = ce("button", { className: "danger small", textContent: "删除" });
    rm.onclick = async (e) => {
      e.stopPropagation();
      if (!(await confirmModal("删除仓库 " + repo.name + "？它建立的软链会立即清理。"))) return;
      await api("DELETE", "/api/repos", { url: repo.url });
      await load();
    };
    meta.append(rm); // 移除在左
    const up = ce("button", { className: "ghost small", style: "white-space:nowrap", textContent: "同步仓库", title: "与上游同步该仓：无本地改动直接拉取更新；有改动（移动/删除/修改/新增）则弹窗选择上传内容（确认=更新并上传 / 仅更新）" });
    up.onclick = (e) => { e.stopPropagation(); repoUpdateFlow(repo, up); };
    meta.append(up); // 同步仓库在右
    li.append(meta);
    if (repo.error) li.append(ce("div", { className: "muted", style: "color:var(--err);font-size:12px;margin-top:6px;white-space:pre-wrap", textContent: repo.error }));
    if (repo.authHint) li.append(ce("div", { className: "repo-authhint", textContent: host ? "鉴权失败，无法自动更新：点上方「填写凭据」填个人令牌(PAT)，或改用 SSH。" : "鉴权失败，无法自动更新：私有仓需配置 SSH key（加入 ssh-agent）。详见标题旁 ? 指南。" }));
    ul.append(li);
  });
  } // /git section
  // 目录源（别家管理）：skills.sh = vercel-labs/skills，canonical 在 ~/.agents/skills，
  // 归 npx skills 管。我们只读识别，更新转交其原生命令，绝不接管（第④不变式）。
  const sh = state.status.skillsSh;
  if (sh && sh.count > 0) {
    // npx 分区标题行：全量更新（代调 npx skills update），与 git 仓的全量更新对齐。
    let npxActions = [];
    const npxUpdate = ce("button", {
      className: "ghost small",
      textContent: state.npxAvailable ? "全量更新" : "需要 Node.js",
      title: state.npxAvailable ? "npx skills update：更新全部 npx skills 的 skill" : "安装或升级 Node.js 后重启 SkillManager，再更新 skills.sh skill",
    });
    npxUpdate.onclick = (e) => { e.stopPropagation(); updateSkillsShAll(npxUpdate); };
    npxActions = [npxUpdate];
    if (!sectionHead(ul, "npx", "npx skills", npxActions)) {
    const shLi = ce("li", { className: "repo-card repo-skillssh clickable", title: "查看 npx skills 管理的 skill" });
    shLi.onclick = () => openSkillsShModal();
    const stop = ce("div", { className: "repo-top" });
    stop.append(ce("span", { className: "repo-dot ok", title: "npx skills 源（~/.agents/skills canonical）" }));
    stop.append(ce("span", { className: "repo-name", textContent: "npx skills" }));
    stop.append(ce("span", { className: "src-badge src-skillssh", textContent: "只读" }));
    shLi.append(stop);
    shLi.append(ce("div", { className: "repo-url", textContent: (sh.root || "~/.agents/skills") + " · vercel-labs/skills（npx skills 管理）" }));
    const smeta = ce("div", { className: "repo-meta" });
    smeta.append(ce("span", { className: "badge count", textContent: sh.count + " skill" }));
    if (!state.npxAvailable) {
      smeta.append(ce("span", { className: "group-spacer" }));
      const dependency = state.status && state.status.nodeDependency;
      const hint = dependency && dependency.state === "too_old"
        ? "Node.js " + (dependency.version || "版本过旧") + "（需要 ≥" + (dependency.requiredVersion || "22.20.0") + "）"
        : "需要 Node.js ≥" + (dependency && dependency.requiredVersion || "22.20.0");
      smeta.append(ce("span", { className: "inv-hint", textContent: hint }));
    }
    shLi.append(smeta);
    ul.append(shLi);
    } // /npx section
  }

  // SkillHub（npx @astron-team/skillhub）不占独立源卡：它是「在线（+ 添加 搜索）」里的
  // 一个源，token 在搜索区的 ⚙ 里配置，安装的 skill 走 @skillhub 命名空间软链进目标、
  // 直接进「目录现状 / 预设」，与 skillsmp 一致（skillsmp 也没有独立卡）。

  // 本地源：同一类能力——都是「本地源」。区别只在来源：
  //   · local —— SkillManager 创建的受管存储（备份/手写归此，可删 skill）
  //   · 其余 —— 用户选择的文件夹（实时识别，不复制，整源可移除）
  // 「添加本地源」放在分区标题行右侧（选一个文件夹作为来源）。
  const addLocal = ce("button", { className: "ghost small", textContent: "添加本地源", title: "选择一个文件夹作为本地源（实时识别其中的 skill，不复制、不改动原文件）" });
  addLocal.onclick = openLocalSrcModal;
  if (!sectionHead(ul, "local", "本地源", [addLocal])) {
  const localLi = ce("li", { className: "repo-card repo-local clickable", title: "查看本地（已备份）skill" });
  localLi.onclick = () => openRepoSkills(LOCAL_NS, { local: true });
  const ltop = ce("div", { className: "repo-top" });
  ltop.append(ce("span", { className: "repo-dot ok", title: "本地受管存储（SkillManager 创建）" }));
  ltop.append(ce("span", { className: "repo-name", textContent: "local" }));
  ltop.append(ce("span", { className: "src-badge src-local", textContent: "本地源" }));
  localLi.append(ltop);
  localLi.append(ce("div", { className: "repo-url", textContent: "~/.skillmanage/local · SkillManager 创建（备份/手写归此）" }));
  const lmeta = ce("div", { className: "repo-meta" });
  lmeta.append(ce("span", { className: "badge count", textContent: (state.skillsByRepo[LOCAL_NS] || []).length + " skill" }));
  localLi.append(lmeta);
  ul.append(localLi);

  // 用户登记的本地目录源：每个文件夹一张卡片（实时识别，不复制）。
  (state.dirSources || []).forEach((d) => {
    const ns = dirNS(d.id);
    const li = ce("li", { className: "repo-card repo-local clickable", title: "查看该本地源里的 skill" });
    li.onclick = () => openRepoSkills(ns, { dir: true, dirId: d.id, title: d.label + " · 本地源", hint: "本地源：可移动其中的 skill（移除整个源请用侧栏「移除」）" });
    const top = ce("div", { className: "repo-top" });
    top.append(ce("span", { className: "repo-dot ok", title: "本地源（你选择的文件夹，实时识别，不复制）" }));
    top.append(ce("span", { className: "repo-name", textContent: d.label }));
    top.append(ce("span", { className: "src-badge src-local", textContent: "本地源" }));
    li.append(top);
    li.append(ce("div", { className: "repo-url", textContent: d.path + " · 你选择的文件夹（实时识别，不复制）" }));
    const m = ce("div", { className: "repo-meta" });
    m.append(ce("span", { className: "badge count", textContent: (d.count || 0) + " skill" }));
    m.append(ce("span", { className: "group-spacer" }));
    const rm = ce("button", { className: "danger small", textContent: "移除" });
    rm.onclick = async (e) => {
      e.stopPropagation();
      if (!(await confirmModal("移除本地源「" + d.label + "」？它建立的软链会清理，原文件夹不动。"))) return;
      try { await api("DELETE", "/api/local-source", { id: d.id }); }
      catch (err) { banner("移除失败：" + err.message, true); return; }
      toast("已移除本地源 " + d.label);
      await load();
    };
    m.append(rm);
    li.append(m);
    ul.append(li);
  });
  } // /local section
}

// ───────────────────────── 分组（Presets）─────────────────────────
// 分组 = 一组 skill 选择器的命名集合（只引用库里的 skill，不复制）。与目标无关，
// 可叠加启用 / 批量移除到「当前目录」。左栏分段在「来源 / 分组」间切换。

function setAsideTab(tab) {
  state.asideTab = tab;
  $("#seg-sources").classList.toggle("active", tab === "sources");
  $("#seg-presets").classList.toggle("active", tab === "presets");
  $("#repo-list").classList.toggle("hidden", tab !== "sources");
  $("#presets-view").classList.toggle("hidden", tab !== "presets");
  // Re-fetch on switch (not just re-render the cache): 分组 can now be created
  // out-of-band via the CLI / loopback API, so the cached state.presets may be
  // stale. Pull fresh, then render.
  if (tab === "presets") refreshPresets().then(renderPresets);
}

async function refreshPresets() {
  try { const d = await api("GET", "/api/presets"); state.presets = (d && d.presets) || []; }
  catch { state.presets = []; }
}

function renderPresets() {
  const box = $("#presets-view"); box.innerHTML = "";
  const target = currentTarget();
  // 两条创建途径：① 从当前目录快照；② 新建空分组再从库里挑 skill。
  // 不放说明文字——按钮文案与 hover 已讲清。
  const head = ce("div", { className: "preset-head" });
  const createBtn = ce("button", { className: "small", textContent: "镜像当前项目", title: target ? ("镜像「" + targetLabel(target) + "」当前已启用的 skill 为一个分组") : "先在右上选一个目录标签页" });
  createBtn.disabled = !target;
  createBtn.onclick = () => createPresetFromCurrent(target);
  const emptyBtn = ce("button", { className: "ghost small", textContent: "创建空组", title: "创建一个空分组，再点卡片从库（git 仓 / 本地源 / npx）里挑 skill" });
  emptyBtn.onclick = () => newEmptyPreset();
  head.append(createBtn, emptyBtn);
  box.append(head);
  if (!target) box.append(ce("div", { className: "muted", style: "font-size:12px;margin:2px 0 8px", textContent: "未选择目录：先在右上选一个同步目录标签页，才能从当前目录新建 / 启用分组（新建空分组不需要目录）。" }));

  if (!state.presets.length) {
    box.append(ce("div", { className: "empty", textContent: "还没有分组。两种建法：在某个目录启用好一组 skill 点「镜像当前项目」；或「创建空组」后点卡片从库里挑 skill。" }));
    return;
  }
  const list = ce("div", { className: "preset-list" });
  state.presets.forEach((p) => list.append(presetCard(p, target)));
  box.append(list);
}

// syncChipsThumb positions the self-drawn scroll indicator for a preset's chip
// area: hidden when not overflowing, else a thumb whose height/offset track the
// scroll position. (WKWebView won't persistently render a CSS scrollbar.)
function syncChipsThumb(chips, thumb) {
  const sh = chips.scrollHeight, ch = chips.clientHeight;
  if (sh <= ch + 1) { thumb.style.display = "none"; return; }
  thumb.style.display = "block";
  const th = Math.max(24, (ch * ch) / sh);
  const denom = sh - ch;
  const top = denom > 0 ? (chips.scrollTop / denom) * (ch - th) : 0;
  thumb.style.height = th + "px";
  thumb.style.top = top + "px";
}

// presetSkillLabel renders a preset member: a normal skill shows its clean name;
// a whole-repo follow (<ns>/* — only legacy presets, new snapshots expand it)
// shows "<source> · 整仓" instead of a bare "*".
function presetSkillLabel(sel) {
  if (sel === "*" || sel.endsWith("/*")) {
    const ns = sel === "*" ? "" : sel.slice(0, -2);
    return (ns ? sourceMeta(ns).label : "全部") + " · 整仓";
  }
  return cleanSkillName(sel);
}

function presetCard(p, target) {
  const enabled = new Set((state.status.enabled || []).filter((e) => e.target === target).map((e) => e.skill));
  const skills = p.skills || [];
  const total = skills.length;
  const have = skills.filter((s) => enabled.has(s.skill)).length;
  // 整张卡片可点击打开跨源选择器（编辑这个分组）；chip 的 × 与底部动作按钮自行排除。
  const card = ce("li", { className: "repo-card preset-card clickable", title: "点击编辑：从库里添加 / 移除 skill" });
  card.onclick = (e) => { if (e.target.closest("button")) return; openPresetPicker(p); };
  const top = ce("div", { className: "repo-top" });
  top.append(ce("span", { className: "repo-name", textContent: p.name }));
  top.append(ce("span", { className: "badge count", textContent: total + " 项" }));
  card.append(top);
  // skill 列表：每个 chip 带 × 可直接从分组移除。超过约四行高度时卡内滚动；
  // WKWebView 不渲染 CSS 自定义滚动条（原生 overlay 会自动隐藏），故在右侧
  // 自绘一个常驻滚动指示条，超长时固定可见。
  const wrap = ce("div", { className: "preset-chips-wrap" });
  const chips = ce("div", { className: "preset-chips" });
  if (!total) chips.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "（空）点卡片从库里挑 skill" }));
  skills.forEach((s) => {
    const chip = ce("span", { className: "preset-chip", title: s.skill });
    chip.append(ce("span", { className: "preset-chip-name", textContent: presetSkillLabel(s.skill) }));
    const x = ce("button", { className: "preset-chip-x", textContent: "×", title: "从分组移除该 skill（不影响已启用的软链）" });
    x.onclick = () => removeSkillFromPreset(p, s.skill);
    chip.append(x);
    chips.append(chip);
  });
  wrap.append(chips);
  const thumb = ce("div", { className: "chips-thumb" });
  wrap.append(thumb);
  card.append(wrap);
  chips.onscroll = () => syncChipsThumb(chips, thumb);
  requestAnimationFrame(() => syncChipsThumb(chips, thumb)); // 入 DOM 后按真实尺寸定位
  // 应用行。
  const bar = ce("div", { className: "repo-meta" });
  const apply = ce("button", { className: "small", textContent: "启用到当前目录", title: target ? ("把这组 skill 批量启用到「" + targetLabel(target) + "」（叠加，不影响其它已启用项）") : "先选一个目录" });
  apply.disabled = !target || !total;
  apply.onclick = () => applyPreset(p.name, target);
  bar.append(apply);
  const remove = ce("button", { className: "ghost small", textContent: "从当前移除", title: "把这组 skill 从当前目录批量停用（其它不动）" });
  remove.disabled = !target || have === 0;
  remove.onclick = () => removePreset(p.name, target);
  bar.append(remove);
  bar.append(ce("span", { className: "group-spacer" }));
  const del = ce("button", { className: "danger small", textContent: "删除", title: "删除该分组（不影响已经启用的 skill）" });
  del.onclick = () => deletePreset(p.name);
  bar.append(del);
  card.append(bar);
  return card;
}

// presetCatalog enumerates every selectable skill across all sources, grouped by
// source, with the EXACT selector each would enable as (ns + "/" + name) so the
// picker's choices line up with apply/remove/dedup elsewhere.
// appliedPresets returns the groups currently fully applied to a target — every
// one of a group's selectors is enabled there (matches the edit-propagation /
// delete "fully applied" rule). Used for the「已应用分组」tags atop the inventory.
function appliedPresets(target) {
  if (!target) return [];
  const enabled = new Set((state.status.enabled || []).filter((e) => e.target === target).map((e) => e.skill));
  return (state.presets || []).filter((p) => (p.skills || []).length > 0 && p.skills.every((s) => enabled.has(s.skill)));
}

function presetCatalog() {
  const groups = [];
  const mapItems = (ns, arr) => (arr || []).map((sk) => {
    const nm = sk.linkName || sk.logicalName || sk.name;
    return { sel: ns + "/" + nm, name: cleanSkillName(nm), desc: sk.description || "" };
  });
  (state.status.repos || []).forEach((repo) => {
    const items = mapItems(repo.name, state.skillsByRepo[repo.name]);
    if (items.length) groups.push({ ns: repo.name, label: (repo.alias || repo.name) + "（git 仓）", items });
  });
  const loc = mapItems(LOCAL_NS, state.skillsByRepo[LOCAL_NS]);
  if (loc.length) groups.push({ ns: LOCAL_NS, label: "local（本地源）", items: loc });
  (state.dirSources || []).forEach((d) => {
    const items = mapItems(dirNS(d.id), state.skillsByRepo[dirNS(d.id)]);
    if (items.length) groups.push({ ns: dirNS(d.id), label: d.label + "（本地源）", items });
  });
  const npx = mapItems(AGENTS_NS, state.skillsShSkills);
  if (npx.length) groups.push({ ns: AGENTS_NS, label: "npx skills", items: npx });
  const hub = mapItems(SKILLHUB_NS, state.skillHubSkills);
  if (hub.length) groups.push({ ns: SKILLHUB_NS, label: "SkillHub", items: hub });
  return groups;
}

// openPresetPicker opens the cross-source skill picker for a preset. Each source
// group has a「全部启用（整仓）」toggle = whole-repo follow (<ns>/*): when on, ALL of
// that source's individual skills are checked AND locked (can't single-uncheck);
// when off, you pick individual skills (snapshot). On save: 整仓 group → <ns>/*
// (follow); else each checked skill → <ns>/<name> (snapshot). Members whose source
// is no longer in the library (stale refs / orphan wildcards) are preserved as-is.
function openPresetPicker(preset) {
  const groups = presetCatalog();
  const known = new Set();          // every catalog selector
  const catalogNs = new Set();      // every source ns present in the catalog
  groups.forEach((g) => { catalogNs.add(g.ns); g.items.forEach((it) => known.add(it.sel)); });
  // Initial membership from the preset.
  const memberSel = new Set();      // individual members
  const wholeNs = new Set();        // ns that are whole-repo follow (<ns>/*)
  (preset.skills || []).forEach((s) => {
    if (s.skill === "*") return;    // bare "*" is legacy noise — ignore
    if (s.skill.endsWith("/*")) wholeNs.add(s.skill.slice(0, -2));
    else memberSel.add(s.skill);
  });

  const overlay = ce("div", { className: "modal" });
  const card = ce("div", { className: "modal-card" });
  card.style.width = "min(560px,100%)";
  const head = ce("div", { className: "modal-head" });
  head.append(ce("h3", { textContent: "添加 skill 到「" + preset.name + "」" }));
  const x = ce("button", { className: "modal-x", textContent: "×", title: "关闭" });
  head.append(x);
  card.append(head);
  const body = ce("div", { className: "modal-body picker-body", style: "padding:12px 18px;max-height:62vh;overflow:auto" });
  const filter = ce("input", { type: "search", placeholder: "过滤 skill…" });
  filter.style.width = "100%";
  // 过滤框放进 head 与滚动 body 之间的独立固定条，随列表滚动时钉在顶部。
  const filterBar = ce("div", { className: "picker-filter", style: "padding:12px 18px;border-bottom:1px solid var(--line)" });
  filterBar.append(filter);
  card.append(filterBar);
  const checks = [];     // {input, row, group, hay} — for filtering
  const groupCtl = [];   // {ns, allCb, itemCbs:[{sel,input}]} — for save
  if (!groups.length) {
    body.append(ce("div", { className: "empty", textContent: "库里还没有可选的 skill。先在「来源」里添加 git 仓 / 本地源，或装 npx skills。" }));
  }
  groups.forEach((g) => {
    const isWhole = wholeNs.has(g.ns);
    // 分区标题行：左标签 + 右「全部启用（整仓同步）」开关。
    const gh = ce("div", { className: "picker-group" });
    gh.append(ce("span", { className: "picker-group-label", textContent: g.label }));
    gh.append(ce("span", { className: "group-spacer" }));
    const allLabel = ce("label", { className: "picker-all", title: "整仓同步：启用该源现有及将来新增的全部 skill；开启后不能单独取消某个 skill" });
    const allCb = ce("input", { type: "checkbox" });
    allCb.checked = isWhole;
    allLabel.append(allCb, ce("span", { textContent: "全部启用（整仓）" }));
    gh.append(allLabel);
    body.append(gh);
    const itemCbs = [];
    g.items.forEach((it) => {
      const row = ce("label", { className: "picker-row", title: it.sel });
      const cb = ce("input", { type: "checkbox" });
      cb.checked = isWhole || memberSel.has(it.sel);
      cb.disabled = isWhole; // 整仓时锁定，不允许单个取消
      row.append(cb, ce("span", { className: "picker-name", textContent: it.name }));
      if (it.desc) row.append(ce("span", { className: "picker-desc muted", textContent: it.desc }));
      body.append(row);
      itemCbs.push({ sel: it.sel, input: cb });
      checks.push({ input: cb, row, group: gh, hay: (it.name + " " + it.sel + " " + it.desc).toLowerCase() });
    });
    allCb.onchange = () => {
      const on = allCb.checked;
      itemCbs.forEach((c) => { if (on) c.input.checked = true; c.input.disabled = on; });
    };
    groupCtl.push({ ns: g.ns, allCb, itemCbs });
  });
  filter.oninput = () => {
    const q = filter.value.trim().toLowerCase();
    const shownByGroup = new Map();
    checks.forEach((c) => {
      const show = !q || c.hay.includes(q);
      c.row.classList.toggle("hidden", !show);
      if (show) shownByGroup.set(c.group, true);
    });
    const seen = new Set(); // 隐藏没有任何可见项的分组标题
    checks.forEach((c) => { if (!seen.has(c.group)) { seen.add(c.group); c.group.classList.toggle("hidden", !shownByGroup.get(c.group)); } });
  };
  card.append(body);
  const actions = ce("div", { style: "display:flex;gap:8px;justify-content:flex-end;padding:12px 18px;border-top:1px solid var(--line)" });
  const cancel = ce("button", { className: "ghost small", textContent: "取消" });
  const save = ce("button", { className: "small", textContent: "保存" });
  actions.append(cancel, save);
  card.append(actions);
  overlay.append(card);
  document.body.append(overlay);
  const close = () => overlay.remove();
  x.onclick = cancel.onclick = close;
  overlay.onclick = (e) => { if (e.target === overlay) close(); };
  save.onclick = async () => {
    // 保留 source 已不在库里的旧引用：独立 selector 不在 known 的、以及孤儿整仓通配。
    const kept = (preset.skills || []).filter((s) => {
      if (s.skill === "*") return false;
      if (s.skill.endsWith("/*")) return !catalogNs.has(s.skill.slice(0, -2));
      return !known.has(s.skill);
    });
    const built = [];
    groupCtl.forEach((gc) => {
      if (gc.allCb.checked) built.push({ skill: gc.ns + "/*", mode: "follow" }); // 整仓
      else gc.itemCbs.forEach((c) => { if (c.input.checked) built.push({ skill: c.sel, mode: "snapshot" }); });
    });
    const skills = kept.concat(built);
    save.disabled = true;
    try { await api("PUT", "/api/presets", { name: preset.name, skills }); }
    catch (e) { banner("保存失败：" + e.message, true); save.disabled = false; return; }
    try { await api("POST", "/api/apply"); } catch { /* 同步到已应用目录后对账，尽力而为 */ }
    close();
    toast("已更新分组「" + preset.name + "」（" + skills.length + " 项）");
    await load(); renderPresets();
  };
  setTimeout(() => filter.focus(), 0);
}

async function newEmptyPreset() {
  const name = await promptModal("创建空组", "起个名字，建好后点卡片从库里挑 skill（同名会覆盖）", "");
  if (!name) return;
  try { await api("PUT", "/api/presets", { name, skills: [] }); }
  catch (e) { banner("新建失败：" + e.message, true); return; }
  await refreshPresets(); renderPresets();
  const p = state.presets.find((x) => x.name === name); // 建好直接开选择器，省一步
  if (p) openPresetPicker(p);
}

async function removeSkillFromPreset(preset, sel) {
  const skills = (preset.skills || []).filter((s) => s.skill !== sel);
  try { await api("PUT", "/api/presets", { name: preset.name, skills }); }
  catch (e) { banner("移除失败：" + e.message, true); return; }
  try { await api("POST", "/api/apply"); } catch { /* 同步到已应用目录后对账，尽力而为 */ }
  await load(); renderPresets();
}

async function createPresetFromCurrent(target) {
  if (!target) return;
  const name = await promptModal("从当前目录新建分组", "给这组 skill 起个名字（同名会覆盖）", "");
  if (!name) return;
  try {
    const r = await api("POST", "/api/presets", { name, target });
    toast("已存为分组「" + name + "」（" + r.count + " skill）");
  } catch (e) { banner("新建分组失败：" + e.message, true); return; }
  await refreshPresets(); renderPresets();
}

async function applyPreset(name, target) {
  if (!target) return;
  try {
    const r = await api("POST", "/api/presets/apply", { name, target });
    await api("POST", "/api/apply"); // 落盘后整理软链（与单个启用一致）
    toast("已启用分组「" + name + "」（新增 " + r.added + "/" + r.total + "）");
  } catch (e) { banner("启用分组失败：" + e.message, true); return; }
  await load(); renderPresets();
}

async function removePreset(name, target) {
  if (!target) return;
  if (!(await confirmModal("把分组「" + name + "」的 skill 从当前目录移除？只停用这组，其它不动。"))) return;
  try {
    const r = await api("POST", "/api/presets/remove", { name, target });
    await api("POST", "/api/apply");
    toast("已移除分组「" + name + "」（停用 " + r.removed + "）");
  } catch (e) { banner("移除失败：" + e.message, true); return; }
  await load(); renderPresets();
}

async function deletePreset(name) {
  if (!(await confirmModal("删除分组「" + name + "」？会同时拆除这组 skill 在所有目录建立的软链；与其它分组共用的 skill 会保留。", "删除", true))) return;
  let r;
  try { r = await api("DELETE", "/api/presets?name=" + encodeURIComponent(name)); }
  catch (e) { banner("删除失败：" + e.message, true); return; }
  try { await api("POST", "/api/apply"); } catch { /* 拆链对账尽力而为 */ }
  toast("已删除分组「" + name + "」" + (r && r.removed ? "（拆除 " + r.removed + " 处软链）" : ""));
  await load(); renderPresets();
}

// promptModal: 轻量文本输入弹窗，返回 Promise<string|null>（取消 / 空 → null）。
// 自建 DOM 并复用现有 .modal/.modal-card 样式，无需在 index.html 预置标记。
function promptModal(title, label, def) {
  return new Promise((resolve) => {
    const overlay = ce("div", { className: "modal" });
    const card = ce("div", { className: "modal-card" });
    card.style.width = "min(420px,100%)";
    const head = ce("div", { className: "modal-head" });
    head.append(ce("h3", { textContent: title }));
    const x = ce("button", { className: "modal-x", textContent: "×", title: "关闭" });
    head.append(x);
    card.append(head);
    const body = ce("div", { className: "modal-body", style: "padding:16px 18px" });
    if (label) body.append(ce("label", { className: "tm-label", textContent: label }));
    const input = ce("input", { value: def || "" });
    input.style.width = "100%";
    body.append(input);
    const actions = ce("div", { style: "display:flex;gap:8px;justify-content:flex-end;margin-top:14px" });
    const cancel = ce("button", { className: "ghost small", textContent: "取消" });
    const ok = ce("button", { className: "small", textContent: "确定" });
    actions.append(cancel, ok);
    body.append(actions);
    card.append(body);
    overlay.append(card);
    document.body.append(overlay);
    const done = (v) => { overlay.remove(); resolve(v); };
    x.onclick = cancel.onclick = () => done(null);
    overlay.onclick = (e) => { if (e.target === overlay) done(null); };
    ok.onclick = () => done(input.value.trim() || null);
    input.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); ok.onclick(); } else if (e.key === "Escape") done(null); };
    setTimeout(() => input.focus(), 0);
  });
}

// cleanSkillName returns a friendly display label for a skill whose identifier
// carries layout noise — e.g. AAIF's ".well-known/skills/<name>/SKILL.md" nesting
// that some skills.sh repos use. It strips a trailing SKILL.md and any directory
// path (both / and \), leaving just the skill's own folder name. The original
// identifier is unchanged — only the label shown to the user is cleaned.
function cleanSkillName(name) {
  if (!name) return name;
  const parts = String(name).replace(/[\\/]+$/, "").split(/[\\/]/);
  let last = parts[parts.length - 1] || String(name);
  if (/^SKILL\.md$/i.test(last) && parts.length > 1) last = parts[parts.length - 2];
  return last || String(name);
}

// openSkillsShModal lists skills.sh-managed skills. They are read-only at the
// source (updates go through the sidebar card's「更新」/ npx), but you CAN enable
// them into the current target / 自动同步 here (selector namespace "@agents"),
// same as any other source.
async function openSkillsShModal() {
  $("#repo-skills-title").textContent = "npx skills";
  const body = $("#repo-skills-body");
  const render = (skills) => {
    // 重渲染前记住列表滚动位置，渲染完还原——避免启用/停用/更新后列表跳回顶部。
    const prevList = body.querySelector(".rs-list");
    const prevScroll = prevList ? prevList.scrollTop : 0;
    body.innerHTML = "";
    const target = currentTarget();
    const follow = enabledFollow(AGENTS_NS);
    const bar = ce("div", { className: "rs-toolbar" });
    if (target) {
      bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "启用到当前目录：" + targetLabel(target) + "（只读源，更新走 npx）" }));
      bar.append(ce("span", { className: "group-spacer" }));
      const fb = ce("button", {
        className: (follow ? "" : "ghost") + " small",
        style: "white-space:nowrap",
        textContent: follow ? "取消启用" : "全部启用",
        title: follow ? "关闭自动同步：不再自动纳入 skills.sh 的 skill（已建立的软链保留）" : "全部启用：skills.sh 现有及将来的全部 skill 自动启用进当前目录",
      });
      fb.onclick = async () => {
        fb.disabled = true;
        if (follow) await api("DELETE", "/api/enabled", { skill: AGENTS_NS + "/*", target });
        else await api("POST", "/api/enabled", { skill: AGENTS_NS + "/*", target, mode: "follow" });
        await api("POST", "/api/apply");
        toast(follow ? "已关闭自动同步" : "已开启自动同步");
        await load(); render(skills);
      };
      bar.append(fb);
    } else {
      bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "由 npx skills 管理。先选一个目录标签页 才能启用；更新请用侧栏卡片上的「更新」。" }));
    }
    body.append(bar);
    const list = ce("div", { className: "rs-list" });
    body.append(list);
    if (!skills) { list.append(ce("div", { className: "empty", textContent: "加载中…" })); return; }
    if (!skills.length) { list.append(ce("div", { className: "empty", textContent: "暂无已装的 npx skills skill。" })); return; }
    const present = new Set(state.inventory.filter((i) => i.managed).map((i) => i.name));
    skills.forEach((sk) => {
      const name = sk.linkName || sk.logicalName;
      const card = ce("div", { className: "skill rs-card clickable", title: "查看详情" });
      // Card opens detail; skip clicks on buttons, links, and the selectable
      // update-command code so they keep their own behavior.
      card.onclick = (e) => { if (e.target.closest("button, a, code")) return; openDetail(AGENTS_NS, name); };
      const main = ce("div", { className: "skill-main" });
      const r1 = ce("div", { className: "skill-row1" });
      r1.append(ce("span", { className: "skill-name", textContent: cleanSkillName(name), title: name }));
      { const vt = verTag(sk.version); if (vt) r1.append(vt); }
      // 来源徽章：lockfile 里的 owner/repo（hover 看完整 URL）。
      const srcText = sk.source || repoFromUrl(sk.sourceUrl) || hostOf(sk.sourceUrl || "");
      if (srcText) {
        const sb = ce("span", { className: "src-badge src-skillssh", textContent: srcText });
        if (sk.sourceUrl) sb.title = sk.sourceUrl;
        r1.append(sb);
      }
      r1.append(ce("span", { className: "group-spacer" }));
      // 单个更新：npx skills update <name>，更新后重拉列表刷新版本号。
      const up = ce("button", { className: "small", textContent: "更新", title: "更新此 skill（npx skills update " + name + "）" });
      up.onclick = () => updateSkillsShOne(name, up, async () => render((await api("GET", "/api/skillssh")) || []));
      r1.append(up);
      // 卸载：删真身 + 所有软链；卸载后重拉列表（该 skill 应消失）。
      const rm = ce("button", { className: "danger small", textContent: "卸载", title: "卸载：删除 ~/.agents/skills 下真身 + 所有软链" });
      rm.onclick = () => uninstallSkillsSh(name, rm, async () => render((await api("GET", "/api/skillssh")) || []));
      r1.append(rm);
      // 启用状态（停用/启用/整仓 follow 时置灰的锁定停用）放最右——与 git 仓弹窗顺序一致（动作在左、状态在最右）。
      r1.append(enableControl(AGENTS_NS, name, follow, present.has(name), target, () => render(skills)));
      main.append(r1);
      if (sk.description) main.append(ce("div", { className: "skill-desc", textContent: sk.description }));
      // 来源 URL（可见、可选中）——更新就从这里拉取，但命令按 skill 名走，
      // URL 由 skills.sh 自己的台账（~/.agents/.skill-lock.json）记录，不用手填。
      if (sk.sourceUrl) main.append(ce("div", { className: "rs-srcurl", textContent: "来源 " + sk.sourceUrl }));
      const cmd = ce("div", { className: "rs-cmdline" });
      cmd.append(ce("code", { className: "rs-cmd", textContent: "npx skills update " + name }));
      cmd.append(ce("span", { className: "rs-cmd-note", textContent: "按名更新，URL 由 npx skills 台账记录" }));
      main.append(cmd);
      card.append(main); list.append(card);
    });
    list.scrollTop = prevScroll; // 还原到重渲染前的滚动位置
  };
  render(null);
  $("#repo-skills-modal").classList.remove("hidden");
  try {
    render((await api("GET", "/api/skillssh")) || []);
  } catch (e) {
    body.innerHTML = "";
    body.append(ce("div", { className: "empty", style: "color:var(--err)", textContent: "加载失败：" + e.message }));
  }
}

// SkillHub (npx @astron-team/skillhub): search + install live in the unified online
// search (a 3rd source alongside skills.sh / skillsmp). Download-ONLY from the CLI's
// built-in public registry — no registry/token/auth config. No source card:
// installed @skillhub skills link into targets and appear in 目录现状 / 预设 like
// any source; skillsmp has no card either. installedSkillHubNames dedups online hits.
function installedSkillHubNames() {
  return new Set((state.skillHubSkills || []).map((s) => (s.linkName || s.logicalName || "").toLowerCase()));
}

// updateSkillsShOne updates a single skills.sh skill via `npx skills update
// <name>` (delegated — we never take ownership). On success re-renders the list
// so the new version shows.
async function updateSkillsShOne(name, btn, after) {
  if (nodeDependencyUI && !nodeDependencyUI.requireNode(btn)) return;
  const old = btn.textContent; btn.disabled = true; btn.textContent = "更新中…";
  try {
    const d = await api("POST", "/api/skillssh/update", { name });
    if (d && d.ok) { toast("已更新 " + cleanSkillName(name), "ok"); if (after) await after(); return; }
    banner("更新 " + cleanSkillName(name) + " 失败：" + ((d && (d.stderr || d.error)) || "未知错误"), true);
  } catch (e) {
    banner("更新 " + cleanSkillName(name) + " 失败：" + e.message, true);
  }
  btn.disabled = false; btn.textContent = old;
}

async function updateSkillsShAll(btn) {
  if (nodeDependencyUI && !nodeDependencyUI.requireNode(btn)) return;
  const old = btn.textContent; btn.disabled = true; btn.textContent = "更新中…";
  try {
    const d = await api("POST", "/api/skillssh/update-all", {});
    if (d && d.ok) toast("skills.sh 已全部更新");
    else banner("skills.sh 更新失败：" + ((d && (d.stderr || d.error)) || "未知错误"), true);
  } catch (e) {
    banner("skills.sh 更新失败：" + e.message, true);
  }
  btn.disabled = false; btn.textContent = old;
}

// openRepoSkills shows a modal listing every skill in a source, and IS the place
// to enable skills / 自动同步 into the current target (replacing the old top「管理」
// drawer). opts.ns overrides the selector namespace (defaults to repoName);
// opts.local → @local store skills are also deletable; opts.title overrides the
// heading. Enable/follow act on the current target tab.
function openRepoSkills(repoName, opts) {
  opts = opts || {};
  const ns = opts.ns || repoName; // selector namespace for enable / 自动同步
  $("#repo-skills-title").textContent = opts.title || (opts.local ? "local · 本地源" : (opts.git ? repoDisplayName(repoName) : repoName));
  const body = $("#repo-skills-body");
  let creators = {}; // git repos only: skill dir name → {creator, createdAt}, for the card badge
  let filterText = ""; // 弹窗内 skill 过滤词，跨 render() 保留（启用/移动/删除后重建列表不丢）
  const render = () => {
    // 保留滚动位置：删除/移动后 render() 会重建整个列表，否则滚动条弹回顶部（回到首个），
    // 体验差。先记下旧 .rs-list 的 scrollTop，重建后还原。
    const prevScroll = (body.querySelector(".rs-list") || {}).scrollTop || 0;
    body.innerHTML = "";
    const target = currentTarget();
    const follow = enabledFollow(ns);
    // Toolbar: which target we enable into + the 自动同步 toggle for this source.
    const bar = ce("div", { className: "rs-toolbar" });
    if (target) {
      const full = targetLabel(target);
      bar.append(ce("span", { className: "muted rs-target", style: "font-size:12px", textContent: "启用到当前目录：" + shortPath(full), title: full }));
    } else {
      bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "未选择同步目录——先在上方选一个目录标签页，再启用。" }));
    }
    // 过滤输入框：占据「启用到当前目录」与右侧按钮之间的中段，实时按名称/描述筛选下面的卡片。
    const filter = ce("input", { className: "rs-filter", type: "search", placeholder: "过滤 skill（名称 / 描述）…", value: filterText });
    filter.oninput = () => { filterText = filter.value; applyFilter(); };
    bar.append(filter);
    // 顺序：同步仓库（左）→ 取消同步 / 自动同步 skill（右）。
    // git 仓专属：「同步仓库」（更新+上传合并）。无本地改动直接更新；有改动则弹窗
    // 勾选上传内容（确认=更新并上传 / 仅更新）。软链 skill 的仓级操作只在这里做。
    if (opts.git) {
      const ub = ce("button", { className: "small", style: "white-space:nowrap", textContent: "同步仓库", title: "与上游同步该仓：无本地改动直接拉取更新；有改动（移动/删除/修改/新增）则弹窗让你选择要上传的内容（确认=更新并上传 / 仅更新）" });
      ub.onclick = () => repoUpdateFlow(state.status.repos.find((rp) => rp.name === repoName) || { name: repoName }, ub);
      bar.append(ub);
    }
    if (target) {
      const fb = ce("button", {
        className: (follow ? "" : "ghost") + " small",
        style: "white-space:nowrap",
        textContent: follow ? "取消启用" : "全部启用",
        title: follow ? "关闭自动同步：不再自动纳入该源的 skill（已建立的软链保留）" : "全部启用：该源现有及将来新增的全部 skill 自动启用进当前目录（与「同步仓库」无关——那是拉取上游）",
      });
      fb.onclick = async () => {
        fb.disabled = true;
        if (follow) await api("DELETE", "/api/enabled", { skill: ns + "/*", target });
        else await api("POST", "/api/enabled", { skill: ns + "/*", target, mode: "follow" });
        await api("POST", "/api/apply");
        toast(follow ? "已关闭自动同步" : "已开启自动同步");
        await load(); render();
      };
      bar.append(fb);
    }
    body.append(bar);

    // The cards live in their own scrolling list; the toolbar above stays fixed.
    const list = ce("div", { className: "rs-list" });
    body.append(list);
    // applyFilter 只做显示/隐藏，不重建列表——所以输入时不会丢焦点；filterText 跨 render 保留。
    const applyFilter = () => {
      const q = filterText.trim().toLowerCase();
      list.querySelectorAll(".rs-card").forEach((c) => {
        c.style.display = !q || (c.dataset.search || "").includes(q) ? "" : "none";
      });
    };
    const skills = state.skillsByRepo[repoName] || [];
    if (skills.length === 0) { list.append(ce("div", { className: "empty", textContent: "该源暂无 skill。" })); return; }
    const present = new Set(state.inventory.filter((i) => i.managed).map((i) => i.name));
    skills.forEach((sk) => {
      const nm = sk.linkName || sk.logicalName;
      const card = ce("div", { className: "skill rs-card clickable", title: "查看详情" });
      // Clicking the card opens the skill's detail (SKILL.md); clicks on the
      // action buttons are excluded so they don't double-fire.
      card.onclick = (e) => { if (e.target.closest("button, a")) return; openDetail(repoName, nm); };
      const main = ce("div", { className: "skill-main" });
      const r1 = ce("div", { className: "skill-row1" });
      r1.append(ce("span", { className: "skill-name", textContent: cleanSkillName(nm), title: nm }));
      { const vt = verTag(sk.version); if (vt) r1.append(vt); }
      { // git skill: 创建人 标签（取自一次 git-log；未提交的新 skill 暂无）
        const cr = creators[sk.logicalName] || creators[nm];
        if (cr && cr.creator) r1.append(ce("span", { className: "badge author", textContent: cr.creator, title: "创建人：" + cr.creator + (cr.createdAt ? "（" + cr.createdAt + "）" : "") }));
      }
      r1.append(ce("span", { className: "group-spacer" }));
      if (opts.local) {
        const del = ce("button", { className: "danger small", textContent: "删除", title: "永久删除该本地 skill 的受管副本，并拆除它建立的所有软链（不可恢复）" });
        del.onclick = () => deleteLocalSkill(nm, del, render);
        r1.append(del);
        const mv = ce("button", { className: "ghost small", textContent: "移动", title: "移动到一个 git 源仓：转移进仓、软链改用 git 来源、同步清单位置（只暂存，待该仓「同步仓库」推送）" });
        mv.onclick = () => openMoveModal({ name: nm, desc: sk.description, sourceKind: "local", onDone: render });
        r1.append(mv);
      } else if (opts.git) {
        const del = ce("button", { className: "danger small", textContent: "删除", title: "从该 git 仓移除此 skill（工作区删除 + 拆软链）；这是本地删除，需在上方「同步仓库」推送后远端才生效" });
        del.onclick = () => deleteRepoSkill(repoName, nm, del, render);
        r1.append(del);
        const mv = ce("button", { className: "ghost small", textContent: "移动", title: "移动到另一个 git 源仓或移回本地（同步清单位置；只暂存，待「同步仓库」推送）" });
        mv.onclick = () => openMoveModal({ name: nm, desc: sk.description, sourceKind: "git", fromRepo: repoName, onDone: render });
        r1.append(mv);
      } else if (opts.dir) {
        const mv = ce("button", { className: "ghost small", textContent: "移动", title: "把该 skill 从本地源移动到受管库 / 另一个本地源 / git 源仓（移出会从该源文件夹删除此子目录）" });
        mv.onclick = () => openMoveModal({ name: nm, desc: sk.description, sourceKind: "dir", fromDir: opts.dirId, onDone: render });
        r1.append(mv);
      }
      r1.append(enableControl(ns, nm, follow, present.has(nm), target, render));
      main.append(r1);
      if (sk.description) main.append(ce("div", { className: "skill-desc", textContent: sk.description }));
      card.append(main);
      card.dataset.search = (cleanSkillName(nm) + " " + nm + " " + (sk.description || "")).toLowerCase();
      list.append(card);
    });
    applyFilter(); // 应用（重建后仍保留的）过滤词
    list.scrollTop = prevScroll; // 还原滚动位置（内容变矮时浏览器自动夹到底部）
  };
  render();
  $("#repo-skills-modal").classList.remove("hidden");
  // git repos: fetch each skill's creator in one git-log pass, then re-render to
  // show the 创建人 badge (best-effort — failure just leaves no badge).
  if (opts.git) {
    api("GET", "/api/repo-creators?repo=" + encodeURIComponent(repoName))
      .then((d) => { creators = (d && d.creators) || {}; render(); })
      .catch(() => {});
  }
}

// followLockedBtn renders the disabled 「停用」shown for a skill that is covered by
// a whole-source 整仓自动同步 (follow): it is enabled as part of the set and cannot
// be toggled individually. ONE authoritative renderer, used by every surface
// (source modals + global search via enableControl, and the 目录现状 inventory via
// inventoryCard) so the follow-locked affordance never diverges between views
// again. Earlier the modal/search path showed an ambiguous "自动同步中" text label
// (reads like a sync is in progress) while the inventory showed this greyed 停用 —
// two looks for one state; this collapses them to the 停用 form.
function followLockedBtn(srcLabel) {
  const msg = "「" + srcLabel + "」整仓自动同步中，不能单独停用该 skill。如需停用，请在该源弹窗点「取消启用」（会移除该源在本目录的全部 skill）。";
  const off = ce("button", { className: "danger small", textContent: "停用", style: "opacity:.4;cursor:not-allowed", title: msg });
  off.onclick = () => toast(msg, "info");
  return off;
}

// enableControl returns the per-skill action used inside the source modals: a
// SINGLE button that reflects the current state and flips it — 「停用」when the
// skill is enabled in the target (click → tear down the link), 「启用」when it is
// not (click → build the link, with same-name shadow confirmation). The two are
// mutually exclusive: only one shows at a time. Under 整仓自动同步 a greyed, locked
// 停用 shows instead (followLockedBtn); with no target, a hint. render re-renders
// the modal.
function enableControl(ns, name, follow, enabled, target, render) {
  if (!target) return ce("span", { className: "inv-hint", textContent: "未选目录" });
  if (follow) return followLockedBtn(sourceMeta(ns).label);
  const sel = ns + "/" + name;
  if (enabled) {
    const off = ce("button", { className: "danger small", textContent: "停用", title: "拆除当前目录下的软链（不影响真身与其它目录）" });
    off.onclick = async () => {
      off.disabled = true;
      await api("DELETE", "/api/enabled", { skill: sel, target });
      const sum = await api("POST", "/api/apply");
      summaryToast(sum, name, false);
      await load(); render();
    };
    return off;
  }
  const on = ce("button", { className: "small", textContent: "启用", title: "在当前目录建立软链" });
  on.onclick = async () => {
    if (!(await confirmShadowEnable(name, target))) return;
    on.disabled = true;
    await api("POST", "/api/enabled", { skill: sel, target, mode: "snapshot" });
    const sum = await api("POST", "/api/apply");
    summaryToast(sum, name, true);
    await load(); render();
  };
  return on;
}

// driftLabel maps a drift kind to its Chinese label.
function driftLabel(kind) {
  return kind === "added" ? "新增未推送" : kind === "modified" ? "修改未推送" : kind === "deleted" ? "删除未推送" : kind === "committed" ? "已提交未推送" : "本地改动";
}

// repoUpdateFlow is the merged 更新/上传 entry (repo card + repo popup toolbar). It
// checks the repo's live local changes: clean → just update (pull + reconcile);
// dirty → open a dialog to pick what to upload, with 确认 (upload then update) or
// 仅更新 (update only, PRESERVING local changes via KeepLocal stash+merge+pop —
// never discards; conflict fails for manual git). 移动/删除/修改都是本地改动，
// 推送统一在这里发生。
async function repoUpdateFlow(repo, btn) {
  if (!repo || !repo.url) { banner("无法确定仓库", true); return; }
  const old = btn ? btn.textContent : "";
  if (btn) { btn.disabled = true; btn.textContent = "检查中…"; }
  let entries = [], secrets = [];
  try {
    const d = await api("GET", "/api/repo-drift?repo=" + encodeURIComponent(repo.name));
    entries = (d && d.entries) || [];
    secrets = (d && d.secrets) || [];
  } catch { entries = []; } // never-synced / uncloned → no drift, fall through to plain update
  if (btn) { btn.disabled = false; btn.textContent = old; }
  if (!entries.length) { await doRepoUpdate(repo, false, btn); return; } // clean → just update
  openRepoUpdateModal(repo, entries, secrets);
}

// doRepoUpdate pulls a single repo's upstream and re-syncs. force=true means "don't
// skip on dirty" — the per-repo path uses KeepLocal, so 仅更新 PRESERVES local changes
// (stash+merge+pop), it does NOT reset --hard/discard. Used by the dialog's 仅更新 and
// after an upload. A non-force update that unexpectedly reports dirty re-opens the dialog.
async function doRepoUpdate(repo, force, btn) {
  const old = btn ? btn.textContent : "";
  if (btn) { btn.disabled = true; btn.textContent = "更新中…"; }
  try {
    const resp = await api("POST", "/api/repos/update", { url: repo.url, force: !!force });
    if (resp && resp.dirty && !force) {
      openRepoUpdateModal(repo, (resp.drift && resp.drift.entries) || [], (resp.drift && resp.drift.secrets) || []);
      return;
    }
    // 仅更新遇到冲突：本地改动已保留（未丢弃、未上传），交给用户用 git 解决。
    if (resp && resp.state === "conflict") {
      banner("更新 " + repo.name + " 失败：本地改动与上游有冲突，已保留你的改动（未丢弃、未上传）。请到镜像仓用 git 手动解决冲突后，再点「同步仓库」。" + (resp.error ? "（" + resp.error + "）" : ""), true, true);
      await load();
      return;
    }
    // A failed update returns HTTP 200 with state:"failed" + a top-level error
    // (and summary.errors may be null), so check BOTH — otherwise a failure shows
    // a misleading green「已同步」. Errors are sticky so they can be read.
    const sum = (resp && resp.summary) || {};
    const summaryErrs = (sum.errors && sum.errors.length) ? sum.errors.join("；") : "";
    const failed = resp && (resp.state === "failed" || (resp.error && !resp.dirty)) ? (resp.error || "未知错误") : "";
    const errMsg = [failed, summaryErrs].filter(Boolean).join("；");
    if (errMsg) banner("同步 " + repo.name + " 失败：" + errMsg, true, true);
    else {
      // 展示本次从上游拉取的 skill 变更（与「全量更新」同款面板）；无变更则轻提示。
      const ups = (resp && resp.updates) || [];
      if (ups.length) await showUpdatePanel(ups);
      else toast("已同步 " + repo.name + "：无更新内容");
    }
    await load();
  } catch (e) {
    banner("同步 " + repo.name + " 失败：" + e.message, true, true);
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = old; }
  }
}

// quickUpload commits (if dirty) and pushes a single git-source skill's local
// changes — including re-pushing a push-failed contribution (committed-unpushed).
async function quickUpload(i, btn) {
  const repo = i.repo || (i.selector ? i.selector.split("/")[0] : "");
  if (!repo) { banner("无法确定该 skill 所属的 git 仓", true); return; }
  const old = btn.textContent; btn.disabled = true; btn.textContent = "上传中…";
  try {
    const r = await fetch("/api/quickupload", {
      method: "POST",
      headers: { Authorization: "Bearer " + TOKEN, "Content-Type": "application/json" },
      body: JSON.stringify({ repo, skill: i.name }),
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) {
      btn.disabled = false; btn.textContent = old;
      banner((d.committed ? "快捷上传：" : "快捷上传失败：") + (CONTRIB_ERR[d.error_code] || d.error || r.statusText), true);
      return;
    }
    toast(d.nothing ? "没有需要上传的改动" : "已上传 " + cleanSkillName(i.name), d.nothing ? "info" : "ok");
    await fetchInventory();
  } catch (e) {
    btn.disabled = false; btn.textContent = old;
    banner("快捷上传 " + i.name + " 失败：" + e.message, true);
  }
}

// openRepoUpdateModal shows the merged update+upload dialog: a read-only list of
// ALL the repo's changed skills + an editable commit message. 确认 = upload them
// ALL (commit+push) then update; 仅更新 = update only, PRESERVING local changes
// (KeepLocal stash+merge+pop; never discards). Selective upload was dropped on
// purpose: a mirror can't push half its working tree and keep the rest diverged
// from upstream, so 确认 is all-or-nothing.
function openRepoUpdateModal(repo, entries, secrets) {
  const desc = {};
  (state.skillsByRepo[repo.name] || []).forEach((sk) => { desc[sk.linkName || sk.logicalName] = sk.description || ""; });
  entries = entries || [];
  state.uploadCtx = { repo: repo.name, repoObj: repo, desc, entries };
  $("#upload-title").textContent = "同步仓库：" + repo.name;
  const list = $("#upload-list");
  list.innerHTML = "";
  entries.forEach((e) => {
    const row = ce("div", { className: "upload-row" });
    row.append(ce("span", { className: "upload-name", textContent: cleanSkillName(e.skill), title: e.path || e.skill }));
    row.append(ce("span", { className: "badge drift", textContent: driftLabel(e.kind) }));
    list.append(row);
  });
  renderUploadSecrets(secrets || []); // 疑似密钥/凭据：标红 + 必须勾选确认才放开「确认」
  const ta = $("#upload-msg"); ta.dataset.touched = "0";
  refreshUploadMsg();
  $("#upload-modal").classList.remove("hidden");
}

// renderUploadSecrets warns about changed files that look like secrets/credentials
// and gates the 确认 (push) button behind an explicit acknowledgement — pushing a
// secret to a shared repo writes it into permanent git history. 仅更新 stays
// enabled (it never uploads — only pulls upstream and keeps local changes local —
// so it can never leak the secret to the remote).
function renderUploadSecrets(secrets) {
  const box = $("#upload-secrets");
  const ok = $("#upload-ok");
  box.innerHTML = "";
  if (!secrets.length) { box.classList.add("hidden"); ok.disabled = false; return; }
  box.classList.remove("hidden");
  box.append(ce("div", { className: "us-head", textContent: "⚠ 检测到疑似密钥 / 凭据文件" }));
  box.append(ce("div", { className: "us-note", textContent: "推送到共享仓后会永久留在 git 历史里、删不掉。请先确认这些文件确实可以公开（或在仓里移除它们后再同步）：" }));
  const ul = ce("ul", { className: "us-list" });
  secrets.forEach((p) => ul.append(ce("li", { textContent: p })));
  box.append(ul);
  const lbl = ce("label", { className: "us-ack" });
  const cb = ce("input", { type: "checkbox", id: "upload-secrets-ack" });
  cb.onchange = () => { ok.disabled = !cb.checked; };
  lbl.append(cb);
  lbl.append(ce("span", { textContent: "我已确认，仍要推送以上文件" }));
  box.append(lbl);
  ok.disabled = true; // 闸住，直到用户显式勾选
}

// refreshUploadMsg rebuilds the default commit message from ALL changed skills,
// but leaves a hand-edited message alone (dataset.touched).
function refreshUploadMsg() {
  const ta = $("#upload-msg");
  if (ta.dataset.touched === "1") return;
  const entries = (state.uploadCtx && state.uploadCtx.entries) || [];
  const desc = (state.uploadCtx && state.uploadCtx.desc) || {};
  if (!entries.length) { ta.value = ""; return; }
  const head = "MIC-0 更新 " + entries.length + " 个 skill";
  const body = entries.map((e) => "- " + cleanSkillName(e.skill) + (desc[e.skill] ? "：" + desc[e.skill] : "")).join("\n");
  ta.value = head + "\n\n" + body;
}

// doUpdateAndUpload (确认): commit + push ALL changed skills, then update. Since
// everything is uploaded, the subsequent align has nothing to discard.
async function doUpdateAndUpload(btn) {
  const ctx = state.uploadCtx;
  if (!ctx) return;
  const paths = (ctx.entries || []).map((e) => e.path || e.skill);
  if (!paths.length) { $("#upload-modal").classList.add("hidden"); await doRepoUpdate(ctx.repoObj, false, null); return; }
  const message = $("#upload-msg").value;
  const ackEl = $("#upload-secrets-ack");
  const confirmSecrets = !!(ackEl && ackEl.checked); // 显式确认才放行密钥文件
  const old = btn.textContent; btn.disabled = true; btn.textContent = "上传中…";
  try {
    const r = await fetch("/api/upload", {
      method: "POST",
      headers: { Authorization: "Bearer " + TOKEN, "Content-Type": "application/json" },
      body: JSON.stringify({ repo: ctx.repo, skills: paths, message, confirmSecrets }),
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) {
      // 密钥拦截：把后端返回的可疑文件列表渲染进弹窗（含勾选框），保持弹窗打开让用户确认后重试。
      if (d.error_code === "secrets_blocked") {
        renderUploadSecrets(d.secrets || []);
        banner("检测到疑似密钥/凭据文件，已拦截。请核对后勾选确认再推送。", true, true);
        return;
      }
      banner((d.committed ? "上传：" : "上传失败：") + (CONTRIB_ERR[d.error_code] || d.error || r.statusText), true, true);
      return;
    }
    $("#upload-modal").classList.add("hidden");
    toast(d.nothing ? "没有需要上传的改动，正在更新…" : "已上传 " + ((d.uploaded && d.uploaded.length) || paths.length) + " 个改动，正在更新…", "ok");
    await doRepoUpdate(ctx.repoObj, true, null); // align to the just-pushed upstream (clean → no-op + pull)
  } catch (e) {
    banner("上传失败：" + e.message, true, true);
  } finally {
    // ALWAYS restore the button — the modal DOM is reused, so leaving it disabled +
    // "上传中…" on the success path stuck the 确认 button permanently (the bug).
    btn.disabled = false; btn.textContent = old;
  }
}

// doUpdateOnly (仅更新): pull upstream while PRESERVING local changes (server uses
// KeepLocal → stash + merge + stash pop). Non-conflicting changes are kept and can
// still be uploaded later; a conflict fails the update for manual git resolution —
// local work is never discarded. (force=true here只表示「不因 dirty 跳过」，不等于丢弃。)
async function doUpdateOnly() {
  const ctx = state.uploadCtx;
  if (!ctx) return;
  $("#upload-modal").classList.add("hidden");
  await doRepoUpdate(ctx.repoObj, true, null);
}

// deleteRepoSkill removes a skill from a git repo's working tree (a PENDING
// deletion) and tears down its links. It does NOT push — the user pushes the
// removal via the repo「上传」dialog (deletion is staged like any other change),
// so the remote only changes on an explicit upload.
async function deleteRepoSkill(repo, name, btn, rerender) {
  if (!(await confirmModal(
    "从 git 仓「" + repo + "」移除 skill「" + cleanSkillName(name) + "」？\n\n会删除工作区里的该 skill 并拆除它建立的软链。这是一次本地删除——需在仓弹窗上方「同步仓库」推送后，远端仓库才真正移除。",
    "删除", true))) return;
  const old = btn.textContent; btn.disabled = true; btn.textContent = "删除中…";
  try {
    await api("DELETE", "/api/repo-skill", { repo, skill: name });
    toast("已从 " + repo + " 移除 " + cleanSkillName(name) + "（记得用「同步仓库」推送删除）", "ok");
    await load();
    if (rerender) rerender();
  } catch (e) {
    btn.disabled = false; btn.textContent = old;
    banner("删除失败：" + e.message, true);
  }
}

async function deleteLocalSkill(name, btn, rerender) {
  if (!(await confirmModal(
    "永久删除本地 skill「" + name + "」？\n\n会删除 ~/.skillmanage/local 里的受管副本，并拆除它在各目录建立的软链。此操作不可恢复。",
    "永久删除", true))) return;
  btn.disabled = true; btn.textContent = "删除中…";
  try {
    await api("DELETE", "/api/local-skill", { name });
    toast("已删除本地 skill " + name);
    await load();        // refresh repo catalog + counts + inventory
    if (rerender) rerender();
  } catch (e) {
    btn.disabled = false; btn.textContent = "删除";
    banner("删除 " + name + " 失败：" + e.message, true);
  }
}

// renderTabs draws one tab per sync directory. Switching a tab re-scans that
// directory's inventory.
// defaultTabLabel derives a meaningful default name from a target path: the
// dir is always ".../<harness>/skills", so the leaf "skills" is useless — drop
// it and the harness folder (.claude/.codex/.agents) and use the parent (the
// project / home dir name) instead.
function defaultTabLabel(dir) {
  const parts = dir.replace(/\/+$/, "").split("/").filter(Boolean);
  if (parts.length && parts[parts.length - 1].toLowerCase() === "skills") parts.pop();
  // pi 全局在 .pi/agent/skills —— 去掉 "skills" 后还剩 "agent" 这一层，多剥一层
  if (parts.length >= 2 && parts[parts.length - 1] === "agent" && parts[parts.length - 2] === ".pi") parts.pop();
  if (parts.length && /^\.(claude|codex|agents|pi)$/.test(parts[parts.length - 1])) parts.pop();
  return parts[parts.length - 1] || dir;
}

// startAliasEdit turns a tab's label into an inline text input (no native prompt,
// which WKWebView blocks). Enter / blur saves via /api/targets/alias; Esc cancels.
// An empty value clears the alias (tab reverts to showing the path).
function startAliasEdit(t, tab) {
  const span = tab.querySelector(".tab-dir");
  if (!span || tab.querySelector(".tab-alias-input")) return;
  const input = ce("input", { className: "tab-alias-input", value: t.alias || "" });
  input.placeholder = defaultTabLabel(t.dir);
  span.replaceWith(input);
  input.focus(); input.select();
  let done = false;
  const commit = async (save) => {
    if (done) return; done = true;
    if (save) {
      try { await api("POST", "/api/targets/alias", { dir: t.dir, alias: input.value.trim() }); await load(); return; }
      catch (err) { banner("保存别名失败：" + err.message, true); }
    }
    renderTabs();
  };
  input.onkeydown = (e) => {
    if (e.key === "Enter") { e.preventDefault(); commit(true); }
    else if (e.key === "Escape") { e.preventDefault(); commit(false); }
  };
  input.onblur = () => commit(true);
  // Keep clicks inside the input from bubbling to the tab (which would switch
  // tabs and re-render, destroying the input mid-edit).
  input.onclick = (e) => e.stopPropagation();
  input.onmousedown = (e) => e.stopPropagation();
  input.ondblclick = (e) => e.stopPropagation();
}

// startRepoAliasEdit turns a git repo card's name into an inline text input
// (mirrors startAliasEdit; WKWebView blocks native prompt). Enter/blur saves via
// /api/repos/alias, Esc cancels; empty clears the alias (card reverts to name).
function startRepoAliasEdit(repo, span) {
  const parent = span.parentNode;
  if (!parent || parent.querySelector(".tab-alias-input")) return;
  const input = ce("input", { className: "tab-alias-input", value: repo.alias || "" });
  input.placeholder = repo.name;
  span.replaceWith(input);
  input.focus(); input.select();
  let done = false;
  const commit = async (save) => {
    if (done) return; done = true;
    if (save) {
      try { await api("POST", "/api/repos/alias", { name: repo.name, alias: input.value.trim() }); await load(); return; }
      catch (err) { banner("保存别名失败：" + err.message, true); }
    }
    renderRepos();
  };
  input.onkeydown = (e) => {
    if (e.key === "Enter") { e.preventDefault(); commit(true); }
    else if (e.key === "Escape") { e.preventDefault(); commit(false); }
  };
  input.onblur = () => commit(true);
  input.onclick = (e) => e.stopPropagation();
  input.onmousedown = (e) => e.stopPropagation();
  input.ondblclick = (e) => e.stopPropagation();
}

function renderTabs() {
  const bar = $("#target-tabs"); bar.innerHTML = "";
  const active = currentTarget();
  if (state.targets.length === 0) {
    bar.append(ce("span", { className: "muted", style: "align-self:center", textContent: "还没有同步目录 →" }));
  }
  let activeEl = null;
  state.targets.forEach((t) => {
    const tab = ce("div", { className: "tab" + (t.dir === active ? " active" : ""), title: t.dir });
    if (t.dir === active) activeEl = tab;
    tab.append(ce("span", { className: "badge " + harnessClass(t.harness), textContent: t.harness }));
    tab.append(ce("span", { className: "tab-dir", textContent: t.alias || t.dir }));
    const rm = ce("button", { className: "tab-x", textContent: "×", title: "移除此同步目录" });
    rm.onclick = async (e) => {
      e.stopPropagation();
      if (!(await confirmModal("移除同步目录 " + (t.alias || t.dir) + "？\n该目录下由本工具建立的链接会立即清理；目录里你自己的真身 skill 不受影响。"))) return;
      if (state.activeTarget === t.dir) state.activeTarget = undefined;
      await api("DELETE", "/api/targets", { dir: t.dir });
      await load();
    };
    tab.append(rm);
    // Switch active in place (don't rebuild the bar): a full renderTabs() here
    // would destroy this element before a double-click could land on it, so
    // dblclick-to-rename never fired. Swap the active class instead.
    tab.onclick = () => {
      if (state.activeTarget === t.dir) return;
      state.activeTarget = t.dir;
      $("#target-tabs").querySelectorAll(".tab.active").forEach((el) => el.classList.remove("active"));
      tab.classList.add("active");
      fetchInventory();
    };
    // Double-click the tab to rename its alias inline (path still shows on hover).
    tab.ondblclick = (e) => { e.stopPropagation(); startAliasEdit(t, tab); };
    // Drag to reorder tabs (persisted via /api/targets/reorder).
    tab.draggable = true;
    tab.ondragstart = (e) => { state.dragDir = t.dir; tab.classList.add("dragging"); e.dataTransfer.effectAllowed = "move"; };
    tab.ondragend = () => { tab.classList.remove("dragging"); document.querySelectorAll(".tab.drop-to").forEach((x) => x.classList.remove("drop-to")); };
    tab.ondragover = (e) => { if (state.dragDir && state.dragDir !== t.dir) { e.preventDefault(); tab.classList.add("drop-to"); } };
    tab.ondragleave = () => tab.classList.remove("drop-to");
    tab.ondrop = (e) => { e.preventDefault(); tab.classList.remove("drop-to"); reorderTabs(state.dragDir, t.dir); };
    bar.append(tab);
  });
  if (activeEl) {
    const cRect = bar.getBoundingClientRect();
    const tRect = activeEl.getBoundingClientRect();
    const delta = (tRect.left - cRect.left) - (bar.clientWidth - activeEl.offsetWidth) / 2;
    bar.scrollTo({ left: bar.scrollLeft + delta, behavior: "smooth" });
  }
}

// reorderTabs moves the dragged tab to where it was dropped and persists order.
async function reorderTabs(fromDir, toDir) {
  state.dragDir = null;
  if (!fromDir || fromDir === toDir) return;
  const arr = state.targets.slice();
  const fi = arr.findIndex((t) => t.dir === fromDir);
  const ti = arr.findIndex((t) => t.dir === toDir);
  if (fi < 0 || ti < 0) return;
  const [moved] = arr.splice(fi, 1);
  arr.splice(ti, 0, moved);
  state.targets = arr;
  renderTabs();
  try { await api("POST", "/api/targets/reorder", { dirs: arr.map((t) => t.dir) }); }
  catch (e) { banner("保存标签顺序失败：" + e.message, true); }
}

function openTargetModal() {
  $("#target-path").value = "";
  $("#target-alias").value = "";
  $("#target-modal").classList.remove("hidden");
  $("#target-path").focus();
  browseTo("");
}
function closeTargetModal() { $("#target-modal").classList.add("hidden"); }

// browseTo lists a directory's subfolders into a picker. boxSel/pathSel let it
// drive either the「添加同步目录」or the「添加本地源」modal (same widget, two homes).
async function browseTo(path, boxSel = "#target-browser", pathSel = "#target-path") {
  const box = $(boxSel);
  box.innerHTML = "";
  let resp;
  try { resp = await api("GET", "/api/browse?path=" + encodeURIComponent(path)); }
  catch (err) { box.append(ce("div", { className: "dir-empty err", textContent: err.message })); return; }
  $(pathSel).value = resp.path;
  if (resp.parent) {
    const up = ce("div", { className: "dir-row up" });
    up.append(ce("span", { className: "ic", textContent: "⬆" }), ce("span", { textContent: "上级目录" }));
    up.onclick = () => browseTo(resp.parent, boxSel, pathSel);
    box.append(up);
  }
  if (resp.dirs.length === 0) { box.append(ce("div", { className: "dir-empty", textContent: "（无子目录）" })); return; }
  resp.dirs.forEach((d) => {
    const row = ce("div", { className: "dir-row" });
    row.append(ce("span", { className: "ic", textContent: "📁" }), ce("span", { textContent: d.name }));
    row.onclick = () => browseTo(d.path, boxSel, pathSel);
    box.append(row);
  });
}

// --- 添加本地源: pick a folder; import its skill(s) into the @local store ---
function openLocalSrcModal() {
  $("#localsrc-path").value = "";
  $("#localsrc-alias").value = "";
  $("#localsrc-modal").classList.remove("hidden");
  $("#localsrc-path").focus();
  browseTo("", "#localsrc-browser", "#localsrc-path");
}
function closeLocalSrcModal() { $("#localsrc-modal").classList.add("hidden"); }

async function addLocalSource(dir, label) {
  let res;
  try { res = await api("POST", "/api/local-source", { dir, label }); }
  catch (err) { banner("添加本地源失败：" + err.message, true); return; }
  toast("已添加本地源「" + (res.label || dir) + "」（识别到 " + (res.count || 0) + " 个 skill）");
  await load();
}

// --- inventory (目录现状视图, phase 3 U8) ---

async function fetchInventory() {
  const t = currentTarget();
  if (!t) { state.inventory = []; state.inherited = []; state.invLoading = false; state.invError = ""; renderInventory(); return; }
  state.invLoading = true; state.invError = ""; renderInventory();
  try {
    const r = await api("GET", "/api/inventory?target=" + encodeURIComponent(t));
    state.inventory = r.items || [];
    state.invScope = r.scope || "";
    // 项目级目标：额外拉「上级继承」的 skill（运行时上级目录也会读到），只读展示以防重复启用。
    state.inherited = [];
    if (state.invScope === "project") {
      try { state.inherited = ((await api("GET", "/api/inherited?target=" + encodeURIComponent(t))).inherited) || []; }
      catch { /* 继承信息拿不到不影响主视图 */ }
    }
  } catch (e) { state.inventory = []; state.inherited = []; state.invError = e.message; }
  state.invLoading = false;
  renderInventory();
}

// renderInheritedSection: 项目级目标顶部的只读区块，列出运行时上级目录（父级 /
// 用户级）已提供的 skill，提醒用户不必在本级重复启用。禁止任何操作（纯展示）。
function renderInheritedSection(root) {
  const inh = state.inherited || [];
  if (!inh.length) return;
  const open = state.collapsed.has("inherited-open"); // 默认收起：present=已展开
  const box = ce("div", { style: "border:1px solid var(--line);border-radius:8px;padding:8px 12px;margin-bottom:12px" });
  const head = ce("div", { className: "row", style: "gap:10px;align-items:baseline;cursor:pointer" });
  head.append(ce("span", { className: "sec-caret", textContent: open ? "▾" : "▸" }));
  head.append(ce("span", { style: "font-weight:600;color:var(--text)", textContent: "继承自上级（" + inh.length + "）" }));
  head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "上级目录运行时已提供，本级无需重复启用（只读）" }));
  head.onclick = () => {
    if (state.collapsed.has("inherited-open")) state.collapsed.delete("inherited-open");
    else state.collapsed.add("inherited-open");
    saveCollapsed();
    renderInventory();
  };
  box.append(head);
  if (open) {
    // 横向 flex-wrap 紧凑排列，充分利用宽度。
    const list = ce("div", { style: "display:flex;flex-wrap:wrap;gap:6px 8px;margin-top:8px" });
    inh.forEach((i) => {
      const chip = ce("span", { style: "display:inline-flex;align-items:center;gap:5px;border:1px solid var(--line);border-radius:6px;padding:2px 8px;font-size:12px", title: "来自 " + i.from });
      chip.append(ce("span", { style: "color:var(--text)", textContent: cleanSkillName(i.name) }));
      chip.append(ce("span", { className: "muted", style: "font-size:10px", textContent: i.scope === "user" ? "用户级" : "上级项目" }));
      list.append(chip);
    });
    box.append(list);
  }
  root.append(box);
}

// abbrevLabel shortens a long source label to its hyphen-segment initials
// (compound-engineering → ce), per the team convention. Only labels longer than
// 10 chars that actually contain a hyphen are abbreviated; everything else is
// returned as-is. Callers keep the full label in the element's title (hover).
function abbrevLabel(s) {
  if (!s || s.length <= 10 || !s.includes("-")) return s;
  return s.split("-").filter(Boolean).map((p) => p[0]).join("");
}

// verTag renders a version badge, or null when there's no STANDARD version.
// Only a semver-shaped value (e.g. 3.13.1, 1.0, 2.1.0-beta.1, optional "v"
// prefix) counts as a version. Anything else — a git commit hash (commit-pinned
// plugins), "unknown"/"undefined", or empty — is NOT a version and shows no
// badge at all.
function verTag(v) {
  v = (v == null ? "" : String(v)).trim().replace(/^v/i, "");
  if (!/^\d+\.\d+(\.\d+)?([-+][0-9A-Za-z.-]+)?$/.test(v)) return null;
  return ce("span", { className: "ver-tag", textContent: "v" + v, title: "版本 " + v });
}

// repoDisplayName returns a git repo's alias if set, else its derived name. The
// namespace/selector is always the derived name — this is display-only.
function repoDisplayName(name) {
  const r = ((state.status && state.status.repos) || []).find((x) => x.name === name);
  return (r && r.alias) || name;
}

// sourceMeta maps a source namespace to a display label + badge class.
function sourceMeta(ns) {
  if (ns === AGENTS_NS) return { label: "skills.sh", cls: "src-skillssh" };
  if (ns === SKILLHUB_NS) return { label: "SkillHub", cls: "src-skillssh" };
  if (ns === LOCAL_NS) return { label: "local", cls: "src-local" };
  if (ns.startsWith("@dir:")) return { label: dirLabel(ns.slice(5)), cls: "src-local" };
  return { label: repoDisplayName(ns), cls: "src-git" }; // git repo (alias or name)
}

// renderSearchResults is the GLOBAL skill search: instead of filtering only the
// current directory, it searches every enable-able skill across all sources
// (git mirrors + local sources + skills.sh) and lets you enable/disable each
// into the current target in real time. Driven by the search box (non-empty term).
function renderSearchResults(root, term) {
  const target = currentTarget();
  // selectors already enabled (linked) in the current target.
  const enabledSel = new Set((state.inventory || []).filter((i) => i.managed && i.selector).map((i) => i.selector));
  const all = [];
  for (const [ns, skills] of Object.entries(state.skillsByRepo || {})) {
    (skills || []).forEach((sk) => all.push({ ns, name: sk.linkName || sk.logicalName, description: sk.description, version: sk.version }));
  }
  (state.skillsShSkills || []).forEach((sk) => all.push({ ns: AGENTS_NS, name: sk.linkName || sk.logicalName, description: sk.description, version: sk.version }));
  const items = all
    .filter((r) => r.name.toLowerCase().includes(term) || (r.description || "").toLowerCase().includes(term))
    .sort((a, b) => a.name.localeCompare(b.name) || a.ns.localeCompare(b.ns));

  const folded = state.searchFold.local;
  const head = ce("div", { className: "search-head clickable", title: folded ? "展开" : "收起" });
  head.append(ce("span", { className: "group-chevron", textContent: folded ? "▾" : "▴" }));
  head.append(ce("span", { className: "group-title", textContent: "搜索结果" }));
  head.append(ce("span", { className: "badge count", textContent: items.length + " skill" }));
  head.append(ce("span", { className: "group-spacer" }));
  head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: target ? "启用到当前目录：" + targetLabel(target) : "先选一个目录标签页 才能启用" }));
  head.onclick = () => { state.searchFold.local = !folded; renderInventory(); };
  root.append(head);

  if (folded) {
    // 本地区折叠：仍渲染在线区
  } else if (!items.length) {
    root.append(ce("div", { className: "empty", textContent: "没有匹配的本地 skill（已搜索全部 git / 本地 / skills.sh 源）" }));
  } else {
    const body = ce("div", { className: "inv-group-body" });
    items.forEach((r) => {
      const enabled = enabledSel.has(r.ns + "/" + r.name);
      const follow = enabledFollow(r.ns);
      const meta = sourceMeta(r.ns);
      const card = ce("div", { className: "skill inv clickable", title: "查看详情" });
      card.onclick = (e) => { if (e.target.closest("button, a")) return; openDetail(r.ns, r.name); };
      const main = ce("div", { className: "skill-main" });
      const r1 = ce("div", { className: "skill-row1" });
      r1.append(ce("span", { className: "skill-name", textContent: cleanSkillName(r.name), title: r.name }));
      { const vt = verTag(r.version); if (vt) r1.append(vt); }
      r1.append(ce("span", { className: "src-badge " + meta.cls, textContent: meta.label }));
      r1.append(ce("span", { className: "group-spacer" }));
      r1.append(enableControl(r.ns, r.name, follow, enabled, target, renderInventory));
      main.append(r1);
      if (r.description) main.append(ce("div", { className: "skill-desc", textContent: r.description }));
      card.append(main);
      body.append(card);
    });
    root.append(body);
  }
  // 「在线（skills.sh）」分区——始终渲染（只渲染 state.skillsShOnline、绝不在此发请求；
  // 线上查询只由显式「搜索」/回车触发，避免每次按键打 npx）。
  renderOnlineSection(root, all, enabledSel);
}

// fmtInstalls renders a threshold like 1000→"1K", 100000→"100K" for labels.
function fmtInstalls(n) {
  if (n >= 1e6) return (n / 1e6) + "M";
  if (n >= 1e3) return (n / 1e3) + "K";
  return "" + n;
}

// installedSkillsShNames returns lowercased skill names present in the skills.sh
// canonical (~/.agents/skills), used to dedup online results against what's
// already installed.
function installedSkillsShNames() {
  const s = new Set();
  (state.skillsShSkills || []).forEach((sk) => s.add((sk.linkName || sk.logicalName || "").toLowerCase()));
  return s;
}

// runOnlineSearch is the ONLY place that fetches online results (explicit trigger,
// never per-keystroke — KTD6). Bumps gen so a slow earlier response can't clobber
// a newer one.
async function runOnlineSearch() {
  const term = state.search.trim();
  const o = state.skillsShOnline;
  if (!term) { o.term = ""; o.results = []; o.error = ""; o.loading = false; o.available = true; renderInventory(); return; }
  const gen = ++o.gen;
  o.term = term; o.loading = true; o.error = ""; o.available = true;
  renderInventory();
  try {
    // 三个在线源并行：skills.sh（npx skills find）+ skillsmp（REST API）+
    // SkillHub（npx @astron-team/skillhub search）。各自失败不拖累其它源。
    const [sh, mp, hub] = await Promise.all([
      api("GET", "/api/skillssh/find?q=" + encodeURIComponent(term)).catch((e) => ({ error: e.message, results: [] })),
      api("GET", "/api/skillsmp/find?q=" + encodeURIComponent(term)).catch((e) => ({ error: e.message, results: [] })),
      api("GET", "/api/skillhub/find?q=" + encodeURIComponent(term)).catch((e) => ({ error: e.message, results: [] })),
    ]);
    if (gen !== o.gen) return; // a newer search superseded this one
    const results = [];
    (sh && sh.results || []).forEach((r) => { r.origin = "skills.sh"; results.push(r); });
    (mp && mp.results || []).forEach((r) => results.push({
      origin: "skillsmp", skill: r.skill, repoUrl: r.repoUrl, url: r.url,
      desc: r.description, author: r.author, installs: r.stars || 0,
      installsRaw: r.stars ? "★ " + r.stars : "",
    }));
    (hub && hub.results || []).forEach((r) => results.push({
      origin: "skillhub", skill: r.slug, name: r.name, namespace: r.namespace,
      version: r.version, desc: r.description, installs: 0, installsRaw: "",
    }));
    // skillsmp search is plain HTTP and remains useful without local Node.
    // Keep the section available whenever at least one source answered as usable.
    o.available = [sh, mp, hub].some((source) => source && source.available === true);
    o.results = results;
    // 容错：各源的错误分开存，互不遮蔽——任一源失败只在区内提示一条，不影响其它源结果。
    o.errSh = (sh && sh.error) || "";
    o.errMp = (mp && mp.error) || "";
    o.errHub = (hub && hub.error) || "";
    o.rateLimit = (mp && mp.rateLimit) || null; // skillsmp 当日配额（每次搜索刷新）
    o.error = ""; // 仅保留给下方 catch 的「整体失败」（正常路径不触发）
  } catch (e) {
    if (gen !== o.gen) return;
    o.available = true; o.results = []; o.errSh = ""; o.errMp = ""; o.errHub = ""; o.rateLimit = null; o.error = e.message;
  } finally {
    if (gen === o.gen) { o.loading = false; renderInventory(); }
  }
}

// Keep the content workspace on the same authenticated API and online search
// path as the existing manager. This bridge is published before load() starts.
window.SkillManagerAPI = Object.freeze({
  request: api,
  openExternal,
  currentTarget() { return currentTarget(); },
  async isSkillEnabled(linkName, target = currentTarget(), canonicalDir) {
    if (!linkName || !target || !canonicalDir) return false;
    const inventory = await api("GET", "/api/inventory?target=" + encodeURIComponent(target));
    const selector = AGENTS_NS + "/" + linkName;
    return !!inventory && inventory.target === target && Array.isArray(inventory.items) && inventory.items.some((item) =>
      item && item.name === linkName && item.selector === selector && item.linkTarget === canonicalDir && item.managed === true && item.enabled === true);
  },
  async refreshSkillsSh() {
    const [skills, status] = await Promise.all([
      api("GET", "/api/skillssh"),
      api("GET", "/api/status"),
    ]);
    state.skillsShSkills = Array.isArray(skills) ? skills : [];
    if (status && typeof status === "object") state.status = status;
    return state.skillsShSkills;
  },
  async searchSkills(term) {
    const value = String(term || "").trim().slice(0, 200);
    state.search = value;
    $("#search").value = value;
    window.SkillManagerContent?.showMarketSearch();
    await runOnlineSearch();
  },
});

// renderOnlineSection renders state.skillsShOnline (render-only; no fetch). Dedup
// against local install/enable state drives which control each result shows.
function renderOnlineSection(root, allLocal, enabledSel) {
  const o = state.skillsShOnline;
  // 来源筛选（skills.sh=下载数 / skillsmp=星数，指标不同，故按来源筛）+ 按热度（各源下载·星）排序。
  const src = state.onlineSrc || "both";
  const sort = state.onlineSort || "default";
  const limit = state.onlineLimit || 0;
  // 来源筛选码 → 结果里的 origin / 展示名。三源：skills.sh / skillsmp / SkillHub。
  const srcOrigin = { skillssh: "skills.sh", skillsmp: "skillsmp", skillhub: "skillhub" };
  const srcLabel = (s) => s === "skillssh" ? "skills.sh" : s === "skillsmp" ? "skillsmp" : s === "skillhub" ? "SkillHub" : s;
  let shown = src === "both" ? o.results.slice() : o.results.filter((r) => r.origin === srcOrigin[src]);
  if (sort === "desc") shown.sort((a, b) => (b.installs || 0) - (a.installs || 0));
  else if (sort === "asc") shown.sort((a, b) => (a.installs || 0) - (b.installs || 0));
  const totalMatched = shown.length;
  let capped = false;
  if (limit > 0) {
    // 数量限制：每个来源各取前 N——两源都保底露出、不会被对方挤占（沿用上面已排好的全局顺序）。
    const perSrc = {};
    shown = shown.filter((r) => {
      const k = r.origin || "";
      perSrc[k] = (perSrc[k] || 0) + 1;
      return perSrc[k] <= limit;
    });
    capped = shown.length < totalMatched;
  }
  const folded = state.searchFold.online;
  const head = ce("div", { className: "search-head online-head clickable", title: folded ? "展开" : "收起" });
  head.append(ce("span", { className: "group-chevron", textContent: folded ? "▾" : "▴" }));
  head.append(ce("span", { className: "group-title", textContent: "在线（npx skills）" }));
  if (o.loading) head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "搜索中…" }));
  else if (o.term) head.append(ce("span", { className: "badge count", textContent: shown.length + " skill" }));
  if (src !== "both") head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "· 仅 " + srcLabel(src) }));
  if (sort !== "default") head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "· 热度" + (sort === "desc" ? "↓" : "↑") }));
  if (limit > 0 && capped) head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "· " + (src === "both" ? "每源前 " + limit : "前 " + limit) + "（共 " + shown.length + "/" + totalMatched + "）" }));
  // skillsmp 当日配额（配了 key 才显示；每次搜索后由响应头刷新）。
  if (state.status && state.status.skillsmpKey && o.rateLimit && o.rateLimit.dailyLimit) {
    const rl = o.rateLimit, low = rl.dailyRemaining <= 50;
    head.append(ce("span", {
      className: "muted", style: "font-size:12px" + (low ? ";color:var(--err)" : ""),
      textContent: "· skillsmp 今日剩余 " + rl.dailyRemaining + "/" + rl.dailyLimit,
      title: "skillsmp 当日剩余可用次数 / 每日上限（来自 X-RateLimit-Daily-* 响应头）。注意：skillsmp 服务端计数为最终一致、更新有延迟——看着不动属正常，真正消耗后才回落；跨天 UTC 0 点重置。",
    }));
  }
  head.onclick = () => { state.searchFold.online = !folded; renderInventory(); };
  root.append(head);

  if (folded) return; // 在线区折叠：只显示标题
  if (!o.available) { root.append(ce("div", { className: "empty", textContent: "在线搜索当前不可用（请检查网络；在线安装与更新需要 Node.js ≥22.20.0）。本地搜索不受影响。" })); return; }
  if (o.loading) { root.append(ce("div", { className: "empty", textContent: "正在查询在线源（skills.sh + skillsmp）…" })); return; }
  if (o.error) { root.append(ce("div", { className: "empty", style: "color:var(--err)", textContent: "在线搜索失败：" + o.error })); return; }
  if (!o.term) { root.append(ce("div", { className: "empty", textContent: "输入关键词后点「搜索」或回车，查在线可安装的 skill（skills.sh + skillsmp）。" })); return; }
  // 单源失败提示（非阻塞）：失败的源不会遮蔽另一个源的结果。触发封控（403）时，
  // 在文案末尾给一个可点击的《配置 API key》——平时不打扰，需要时一键去配。
  const srcNote = (label, msg, cfgFn, cfgText) => {
    const d = ce("div", { className: "empty", style: "color:var(--err);font-size:12px;text-align:left" });
    d.append(document.createTextNode(label + " 查询失败：" + msg + "（不影响其它源）"));
    if (cfgFn) {
      d.append(document.createTextNode(" "));
      const a = ce("a", { className: "online-link", href: "#", textContent: cfgText || "《配置 API key》" });
      a.onclick = (e) => { e.preventDefault(); cfgFn(); };
      d.append(a);
    }
    return d;
  };
  const notes = [];
  if (o.errSh) notes.push(srcNote("skills.sh", o.errSh, null));
  if (o.errMp) notes.push(srcNote("skillsmp", o.errMp, /403|风控|拒绝访问|额度/.test(o.errMp) ? openSmpKeyModal : null));
  if (o.errHub) notes.push(srcNote("SkillHub", o.errHub, null));

  if (!o.results.length) {
    if (notes.length) notes.forEach((n) => root.append(n));
    else root.append(ce("div", { className: "empty", textContent: "在线没有匹配「" + o.term + "」的 skill。" }));
    return;
  }
  if (!shown.length) { root.append(ce("div", { className: "empty", textContent: "当前来源筛选（仅 " + srcLabel(src) + "）下无匹配，点 ⚙ 换来源。" })); notes.forEach((n) => root.append(n)); return; }

  const installed = installedSkillsShNames();
  const installedHub = installedSkillHubNames();
  const body = ce("div", { className: "inv-group-body" });
  shown.forEach((r) => {
    const mp = r.origin === "skillsmp";
    const hub = r.origin === "skillhub";
    const nm = (r.skill || "").toLowerCase();
    const isInstalled = hub ? installedHub.has(nm) : installed.has(nm);
    const isEnabled = enabledSel.has((hub ? SKILLHUB_NS : AGENTS_NS) + "/" + (r.skill || ""));
    const card = ce("div", { className: "skill inv online" + (r.url ? " clickable" : ""), title: r.url ? "查看详情" : "" });
    if (r.url) card.onclick = (e) => { if (e.target.closest("button, a")) return; openExternal(r.url); };
    const main = ce("div", { className: "skill-main" });
    const r1 = ce("div", { className: "skill-row1" });
    r1.append(ce("span", { className: "skill-name", textContent: r.name || r.skill || r.pkg, title: r.skill || r.pkg }));
    if (r.installsRaw) r1.append(ce("span", { className: "install-count", title: mp ? "GitHub stars" : "安装数", textContent: (mp ? "" : "↓ ") + r.installsRaw }));
    { const vt = verTag(r.version); if (vt) r1.append(vt); }
    // 来源徽章：区分 skills.sh / skillsmp / SkillHub（都走 npx，但发现源不同）。
    if (hub) {
      r1.append(ce("span", { className: "src-badge src-skillssh", title: "来自 SkillHub registry" + (r.namespace ? "（" + r.namespace + "）" : ""), textContent: "SkillHub" }));
    } else if (mp) {
      r1.append(ce("span", { className: "src-badge src-skillsmp", title: "来自 skillsmp.com" + (r.author ? "（" + r.author + "）" : ""), textContent: "skillsmp" }));
    } else {
      const ownerRepo = r.owner ? r.owner + "/" + r.repo : "skills.sh";
      r1.append(ce("span", { className: "src-badge src-skillssh online-repo", title: ownerRepo, textContent: r.repo || ownerRepo }));
    }
    r1.append(ce("span", { className: "group-spacer" }));
    r1.append(onlineAction(r, isInstalled, isEnabled));
    main.append(r1);
    main.append(ce("div", { className: "skill-desc online-sub", textContent: hub ? (r.desc || "") : mp ? (r.desc || r.repoUrl || "") : r.pkg }));
    if (r.url) {
      // 外链钉在卡片左下角（footer + margin-top:auto，同行卡片对齐）。
      const foot = ce("div", { className: "online-foot" });
      const link = ce("a", { href: r.url, textContent: mp ? "↗ skillsmp" : "↗ skills.sh", className: "online-link", title: "在系统浏览器中打开" });
      foot.append(link);
      main.append(foot);
    }
    card.append(main);
    body.append(card);
  });
  root.append(body);
  notes.forEach((n) => root.append(n)); // 有结果时仍提示哪个源失败了（不影响已显示的结果）
}

// onlineAction returns the right control for one online result: an install button
// when not installed, or a status label when already installed (distinguishing
// installed-but-not-enabled from installed-and-enabled).
function onlineAction(r, isInstalled, isEnabled) {
  if (isInstalled && isEnabled) return ce("span", { className: "src-badge src-ok", textContent: "已安装且已启用" });
  if (isInstalled) return ce("span", { className: "muted online-installed", title: "已装到 skills.sh，可在本地搜索结果里启用到当前目录", textContent: "已安装（未启用）" });
  const btn = ce("button", { className: "small", textContent: "安装" });
  if (installingPkg) btn.disabled = true; // 并发锁：有安装在跑时其它按钮禁用
  btn.onclick = () => installOnline(r, btn);
  return btn;
}

// installOnline runs the two-layer install:
//   1. npx skills add -g -y -a universal  → install ONLY to canonical (~/.agents/skills)
//   2. SkillManager's own linker            → symlink (enable) into the CURRENT tab dir
// Layer 2 is SkillManager's capability, not npx's: the link is manifest-owned and
// can be 停用 like any other. So "install where you are" at least links to the
// directory you're looking at. Button state machine: idle→安装中→已安装 / fail(复原)。
async function installOnline(r, btn) {
  if (nodeDependencyUI && !nodeDependencyUI.requireNode(btn)) return;
  if (installingPkg) return;
  const hub = r.origin === "skillhub";
  const ns = hub ? SKILLHUB_NS : AGENTS_NS;
  const target = currentTarget();
  installingPkg = hub ? ("skillhub#" + r.skill) : (r.pkg || (r.repoUrl + "#" + r.skill)); // 并发锁键（skillsmp/skillhub 无 pkg）
  btn.disabled = true; btn.textContent = "安装中…";
  try {
    // skills.sh：npx skills add <pkg>；skillsmp：npx skills add <repoUrl> --skill <name>；
    // SkillHub：npx @astron-team/skillhub install <slug> --dir <受管 store>。
    let d;
    if (hub) {
      d = await api("POST", "/api/skillhub/install", { slug: r.skill });
    } else {
      const addBody = r.origin === "skillsmp" ? { url: r.repoUrl, skill: r.skill } : { pkg: r.pkg };
      d = await api("POST", "/api/skillssh/add", addBody);
    }
    if (!(d && d.ok)) throw new Error((d && (d.error || d.stderr)) || "未知错误");
    // Installed to its store. Now enable into the current directory via our linker.
    let enabled = false, enableErr = "";
    if (target) {
      try {
        await api("POST", "/api/enabled", { skill: ns + "/" + r.skill, target, mode: "snapshot" });
        await api("POST", "/api/apply");
        enabled = true;
      } catch (e) { enableErr = e.message; }
    }
    btn.textContent = "已安装"; // success：不复原，靠 load() 重渲成去重态
    installingPkg = null;
    if (enabled) toast("已安装 " + (r.skill || r.pkg) + " 并启用到 " + targetLabel(target));
    else if (target) banner("已安装 " + (r.skill || r.pkg) + "，但启用到当前目录失败：" + enableErr, true);
    else toast("已安装 " + (r.skill || r.pkg) + " 到 skills.sh（未选目录，未启用）");
    await load(); // 刷新 skills.sh + 现状 + 状态，去重/启用态生效
  } catch (e) {
    installingPkg = null;
    btn.disabled = false; btn.textContent = "安装"; // fail：复原可重试
    banner("安装 " + (r.skill || r.pkg) + " 失败：" + e.message, true);
  }
}

function renderInventory() {
  renderStats(); // keep the header「收录 skills」count in sync on every re-render
  const root = $("#skills"); root.innerHTML = "";
  const term = state.search.trim().toLowerCase();
  // A search term switches to GLOBAL search across all sources (not just this
  // directory) with real-time enable. This must come BEFORE the no-targets /
  // loading / error early-returns: online (skills.sh) search does not depend on
  // having any sync directory — otherwise a fresh install with no directory yet
  // would fetch online results but silently never render them (enabling to a
  // directory still needs a tab, which renderSearchResults notes inline).
  if (term) { renderSearchResults(root, term); return; }
  if (state.targets.length === 0) {
    root.append(ce("div", { className: "empty", textContent: "还没有同步目录。点右上角「+」添加一个，再回到这里查看现状。" }));
    return;
  }
  if (state.invLoading) { root.append(ce("div", { className: "empty", textContent: "正在扫描…" })); return; }
  if (state.invError) {
    const e = ce("div", { className: "empty", style: "color:var(--err)" });
    e.append(ce("span", { textContent: "扫描失败：" + state.invError }));
    const retry = ce("button", { className: "small", textContent: "重试", style: "margin-left:10px" });
    retry.onclick = fetchInventory;
    e.append(retry);
    root.append(e);
    return;
  }
  // 项目级：先渲染只读的「继承自上级」区块（即使本目录暂无 skill 也显示，方便看父级有什么）。
  renderInheritedSection(root);
  let items = state.inventory.slice();
  // 注入当前 tab 所属 harness 的插件 skill（只读），作为「目录现状」底部「插件」组。
  const curHarness = (state.targets.find((t) => t.dir === currentTarget()) || {}).harness;
  (state.pluginSkills || []).forEach((p) => {
    if (curHarness && p.harness && p.harness !== curHarness) return;
    items.push({ kind: "plugin", name: p.name, description: p.description, plugin: p.plugin, version: p.version, harness: p.harness });
  });
  if (items.length === 0) {
    const box = ce("div", { className: "empty" });
    box.append(ce("div", { textContent: "该目录暂无 skill。" }));
    box.append(ce("div", { className: "muted", style: "margin-top:6px", textContent: "用上方搜索框跨所有源查找并启用，或在目录里创建 SKILL.md 后刷新。" }));
    root.append(box);
    return;
  }
  // 顶部：当前目录「已应用的分组」标签（该组 skill 全部已启用在本目录即算应用）。
  const applied = appliedPresets(currentTarget());
  if (applied.length) {
    const bar = ce("div", { className: "applied-presets" });
    bar.append(ce("span", { className: "ap-label", textContent: "已应用分组：" }));
    applied.forEach((p) => {
      const tag = ce("span", { className: "ap-tag", title: "点击查看 / 编辑该分组", textContent: p.name });
      tag.onclick = () => { setAsideTab("presets"); openPresetPicker(p); };
      bar.append(tag);
    });
    root.append(bar);
  }
  // Group by source (R3.2 / 用户要求「按仓库分类」): 本地 → 各 git 仓 → skills.sh →
  // 插件 → 未备份 → 未知软链.
  const groups = new Map();
  items.forEach((i) => {
    const g = groupOf(i);
    if (!groups.has(g.key)) groups.set(g.key, { key: g.key, title: g.title, order: g.order, help: g.help, items: [] });
    groups.get(g.key).items.push(i);
  });
  const ordered = [...groups.values()].sort((a, b) => a.order - b.order || a.title.localeCompare(b.title));
  // 各组独立展开 / 收起，默认全部展开（不再手风琴）。只记录被「收起」的组 key——
  // 没记录的即展开，新出现的组也默认展开。
  ordered.forEach((g) => {
    const collapsed = !!state.collapsedGroups[g.key];
    const grp = ce("div", { className: "inv-group" + (collapsed ? " collapsed" : "") });
    const head = ce("div", { className: "inv-group-head", title: collapsed ? "展开" : "收起" });
    head.append(ce("span", { className: "group-title", textContent: g.title }));
    if (g.help) {
      const q = ce("span", { className: "group-help", textContent: "?", title: "点击查看说明" });
      q.onclick = (e) => { e.stopPropagation(); infoModal(g.help.title, g.help.paras); }; // 点 ? 弹窗说明，不触发折叠
      head.append(q);
    }
    head.append(ce("span", { className: "badge count", textContent: g.items.length + " skill" }));
    // 插件组：在标题后说明「全局、与当前目录无关」（+ 无法检测更新），与 skills.sh 的组说明同位置。
    if (g.key === "plugin") {
      let txt = "全局插件（harness 级，与当前目录无关）";
      if (state.status && state.status.claudeCli) txt += " · 无法检测更新，按需在第一行手动委托";
      head.append(ce("span", {
        className: "group-note",
        textContent: txt,
        title: "插件不是按目录管理的：它们统一装在 ~/.claude/plugins（cc）/ ~/.codex/plugins（codex）。这里列的是该 harness 全局已装插件带的 skill，对任何目录都一样——既不是当前目录、也不是其父目录带的。所以给新目录加目录后看到一堆插件 skill 属正常。SkillManager 只读，不接管。",
      }));
    }
    // skills.sh 组：它归 npx skills 自己的台账管理，本工具只读、不主动联网比对，
    // 因此无法显示「是否有更新」。在组头给出说明 + 一个手动「更新」入口（代调 npx）。
    if (g.key === "skillssh") {
      head.append(ce("span", {
        className: "group-note",
        textContent: "由 npx skills 自管、无更新检查接口，无法获取实时更新状态，请在卡片里手动更新",
        title: "skills.sh(npx skills) 不提供「检查是否有更新」的接口，本工具又不接管它，所以无法像 git 源那样预判更新。更新请在各 skill 卡片上点「更新」（代调 npx skills update），每日定时更新也会自动刷新它。",
      }));
    }
    // 右侧上下箭头：展开时朝上（点击收起），收起时朝下（点击展开）。
    head.append(ce("span", { className: "group-chevron", textContent: collapsed ? "▾" : "▴" }));
    head.onclick = () => {
      // 只切换本组的展开 / 收起，互不影响其它组。
      if (collapsed) delete state.collapsedGroups[g.key];
      else state.collapsedGroups[g.key] = true;
      renderInventory();
    };
    grp.append(head);
    if (!collapsed) {
      const body = ce("div", { className: "inv-group-body" });
      if (g.key === "plugin") body.append(pluginToolbar(g.items)); // 第一行：逐插件更新 + 全部更新
      g.items.forEach((i) => body.append(inventoryCard(i)));
      grp.append(body);
    }
    root.append(grp);
  });
}

// repoFromUrl derives "owner/repo" from a git URL (skills.sh sourceUrl), so a
// skills.sh skill is grouped under the repo it came from — it's a 库 too, just
// installed by a different tool.
function repoFromUrl(u) {
  if (!u) return "";
  let p = u.replace(/^[a-z]+:\/\//i, "").replace(/\.git$/i, "");
  const slash = p.indexOf("/");
  if (slash >= 0) p = p.slice(slash + 1); // drop host
  return p;
}

// SOURCE_ORDER is the single global source ordering used by EVERY surface
// (inventory groups, the「+ 添加」drawer, the sidebar) so sources always list the
// same way: 未知软链(unknown) → git → npx skills(skills.sh) → 本地未备份(handwritten)
// → 本地(local) → plugins。未知排最上（异常项最该先看到）。改这一处即可全站统一。
const SOURCE_ORDER = { unknown: 1, git: 2, "skills.sh": 3, handwritten: 4, local: 5, dir: 6, plugin: 7 };

// dirLabel resolves a local directory-source id to its display label (falls back
// to the id when the source list hasn't loaded yet).
function dirLabel(id) {
  const d = (state.dirSources || []).find((x) => x.id === id);
  return d ? d.label : id;
}

// groupOf maps an inventory item to its source group (title + sort order from SOURCE_ORDER).
function groupOf(i) {
  switch (i.kind) {
    case "git": return { key: "git:" + (i.repo || ""), title: i.repo ? repoDisplayName(i.repo) : "git 仓", order: SOURCE_ORDER.git };
    case "skills.sh":
      // 全部归到一个「skills.sh」组——它们都归 npx skills 管，不按来源仓拆分。
      // 具体来源仓（owner/repo）显示在每张卡片的徽章上。
      return { key: "skillssh", title: "npx skills", order: SOURCE_ORDER["skills.sh"] };
    // 本地（受管存储）与各本地目录源在「使用」时合并为一个「本地源」类，不按目录拆组。
    case "local":
    case "dir": return { key: "local", title: "本地源", order: SOURCE_ORDER.local };
    case "plugin": return { key: "plugin", title: "插件（plugins）", order: SOURCE_ORDER.plugin };
    case "handwritten": return { key: "hand", title: "未备份（可备份）", order: SOURCE_ORDER.handwritten, help: {
      title: "「备份」是做什么的？",
      paras: [
        { text: "这一组是直接在该目录里手写的 skill——真身文件就放在当前目录里，没有副本。它不在 SkillManager 的受管范围，也不跟随自动更新；一旦误删目录或机器故障，就彻底丢了。" },
        { h: "点卡片上的「备份」会：", text: "① 把真身目录移动到受管存储 ~/.skillmanage/local/<名字>；② 在原位置建一个软链指回去，所以各 harness 看到的路径和内容完全不变，照常生效；③ 从此它归 SkillManager 管理，纳入统一的启用/停用与同步。" },
        { h: "影响：", text: "原目录会从「真实文件」变成「软链」，内容不变、不会中断使用。备份后它会从「未备份」移到「本地（已备份）」分组。这一步只搬运、不删除、不联网，可随时再停用拆掉软链。" },
      ],
    } };
    default: return { key: "unknown", title: "未知软链", order: SOURCE_ORDER.unknown };
  }
}

function inventoryCard(i) {
  const s = SRC[i.kind] || SRC.unknown;
  const row = ce("div", { className: "skill inv clickable " + s.cls, title: "查看详情" });
  // openDetailFor opens this skill's SKILL.md the right way for its kind
  // (managed → source repo; plugin → plugin tree; else → physical path) and
  // fills the modal footer with this skill's contextual actions.
  const openDetailFor = () => {
    const p = i.managed && i.selector ? openDetail(i.selector.split("/")[0], i.name)
      : i.kind === "plugin" ? openPluginDetail(i.plugin, i.name, i.harness)
      : openDetailAt(i.name);
    setInventoryActions(i); // card actions live in the detail modal footer now
    return p;
  };
  // Clicking the card opens 操作/详情; clicks on the action button are excluded.
  row.onclick = (e) => { if (e.target.closest("button, a")) return; openDetailFor(); };
  const main = ce("div", { className: "skill-main" });
  const r1 = ce("div", { className: "skill-row1" });
  r1.append(ce("span", { className: "skill-name", textContent: cleanSkillName(i.name), title: i.name }));
  { const vt = verTag(i.version); if (vt) r1.append(vt); }

  // skills.sh 卡不放来源徽章：它已自成一组「skills.sh」，来源（台账 + URL）在详情卡的
  // 「台账来源」行里。卡上那条 .well-known/…/SKILL.md 徽章是冗余的，还会把名字挤成一两个字，
  // 故去掉——把整行宽度让给名称（名称仍 ellipsis 截断，不超框）。
  if (i.kind !== "skills.sh") {
    // 徽章默认显示全名——位置够就不缩写（CSS 在真正放不下时才 ellipsis 截断，全名见 hover）。
    let badgeText = s.label;
    if (i.kind === "plugin" && i.plugin) badgeText = i.plugin;
    // 本地源合并成一个组后，徽章带上各自来源：local（受管存储）或某个本地源文件夹名。
    if (i.kind === "local") badgeText = "local";
    if (i.kind === "dir") badgeText = dirLabel(i.repo);
    // 缩写只针对 plugin（名字常含连字符且偏长，如 compound-engineering → ce）；npx /
    // git / 本地源标签本身不长，保持全名。全名见 hover（plugin 的 title 在下面设置）。
    const badge = ce("span", { className: "src-badge " + s.cls, textContent: i.kind === "plugin" ? abbrevLabel(badgeText) : badgeText });
    if (i.kind === "plugin" && i.plugin) badge.title = i.plugin;
    if (i.kind === "dir") badge.title = dirLabel(i.repo) + "（本地源）";
    r1.append(badge);
  }

  if (i.collision) {
    const c = ce("span", { className: "src-badge src-shadow", textContent: "遮蔽" });
    c.title = "同名 skill 在全局与项目目录下各有一份软链，互相遮蔽（项目级生效）。若由 skills.sh/外部工具安装则只读，请用其原生方式或手动移除其一。";
    r1.append(c);
  }
  // 状态标签统一作为左侧 src-badge（如插件的「只读」），而不是右侧散文字/单独样式：
  // 右侧只放动作（详情 / 停用 / 更新 / 删除）。
  if (i.kind === "plugin") {
    const ro = ce("span", { className: "src-badge src-readonly", textContent: "只读" });
    ro.title = "由 harness 插件系统管理，SkillManager 只读不接管";
    r1.append(ro);
  }

  r1.append(ce("span", { className: "group-spacer" }));

  // 右侧 = skill 列表，不碰本体。卡片上始终带一个对应的快捷操作（本体操作——移动/删除
  // 本体/整仓同步/启用——只在左侧对应「源」弹窗里做）。点卡片空白处都能打开详情看 SKILL.md。
  // 一致性规则（每种来源都有明确的卡上动作）：
  //   · 整仓自动同步(follow)  → 「停用」显示但置灰，点击提示不可单独停用（followLockedBtn，与弹窗/搜索同款）；
  //   · 本工具启用的软链        → 「停用」（/api/enabled，可逆）；
  //   · skills.sh 裸投影         → 「停用」（/api/inventory/link）；
  //   · 杂散/未知软链            → 「删除」（只删链，不动目标）；
  //   · 手写未备份 / 插件 等     → 「操作」（详情里做 备份/删除 或只读说明）。
  if (i.follow && i.managed && i.selector) {
    r1.append(followLockedBtn(sourceMeta(i.selector.split("/")[0]).label));
  } else if (i.managed && i.selector) {
    const off = ce("button", { className: "danger small", textContent: "停用", title: "拆除当前目录下的软链（不影响真身与其它目录）" });
    off.onclick = () => disableSkill(i, off);
    r1.append(off);
  } else if (i.kind === "skills.sh") {
    const off = ce("button", { className: "danger small", textContent: "停用", title: "拆除 npx skills 在当前目录的投影软链（下次 update 可能重新投影回来）" });
    off.onclick = () => disableSkillsShLink(i, off);
    r1.append(off);
  } else if (i.kind === "unknown") {
    const del = ce("button", { className: "danger small", textContent: "删除", title: "只删此软链，不动它指向的目标" });
    del.onclick = () => deleteStrayLink(i, del);
    r1.append(del);
  } else if (i.kind === "plugin") {
    // 插件只读，由 harness 插件系统管理——卡片不放任何动作按钮（更新在插件组第一行委托）。
    // 点卡片空白处仍可打开详情查看 SKILL.md。
  } else {
    const op = ce("button", { className: "skill-detail-btn", textContent: "操作" });
    op.onclick = openDetailFor;
    r1.append(op);
  }

  main.append(r1);
  // 反查：show where an unknown stray symlink points.
  if (i.kind === "unknown" && i.linkTarget) main.append(ce("div", { className: "inv-linktarget", title: i.linkTarget, textContent: "→ " + i.linkTarget }));
  if (i.description) main.append(ce("div", { className: "skill-desc", textContent: i.description }));
  row.append(main);
  return row;
}

// deleteStrayLink removes a single unknown/stray symlink (the symlink only, never
// its target). Explicit, confirmed — the user-initiated exception to never-break.
async function deleteStrayLink(i, btn) {
  const where = i.linkTarget ? "（指向 " + i.linkTarget + "）" : "";
  if (!(await confirmModal("删除软链 " + i.name + " " + where + "？\n只删这条软链本身，不会动它指向的目标，也不影响其它目录。", "删除软链", true))) return;
  btn.disabled = true; btn.textContent = "删除中…";
  try {
    await api("DELETE", "/api/inventory/link", { target: currentTarget(), name: i.name });
    toast("已删除软链 " + i.name);
    await load(true); // 移除占用项后 reconcile：补建之前被挡住的链，并刷新 footer
  } catch (e) {
    btn.disabled = false; btn.textContent = "删除";
    banner("删除软链 " + i.name + " 失败：" + e.message, true);
  }
}

// disableSkillsShLink removes a skills.sh projection (the symlink in this target
// only) — never the canonical install under ~/.agents/skills. Same backend guard
// and endpoint as disabling a managed source's link (refuses real dirs and our
// own managed links). It's a disable, not a delete: skills.sh may re-project it
// on its next update, so we say so up front.
async function disableSkillsShLink(i, btn) {
  if (!(await confirmModal(
    "停用此目录下的软链「" + i.name + "」？\n\n只拆除这一处的软链，不动 ~/.agents/skills 里的真身，也不影响其它目录。\n注意：skills.sh 下次 update 可能会把它重新投影回来——彻底卸载请用 skills.sh 自己的命令。",
    "停用", true))) return;
  btn.disabled = true; btn.textContent = "停用中…";
  try {
    await api("DELETE", "/api/inventory/link", { target: currentTarget(), name: i.name });
    toast("已停用软链 " + i.name);
    await load(true); // 移除占用项后 reconcile：补建之前被挡住的链，并刷新 footer
  } catch (e) {
    btn.disabled = false; btn.textContent = "停用";
    banner("停用软链 " + i.name + " 失败：" + e.message, true);
  }
}

async function deleteHandwritten(i, btn) {
  if (!(await confirmModal(
    "永久删除手写 skill「" + i.name + "」的真身目录？\n\n它没有备份，删除后无法恢复。若只是想移出当前目录但保留，请改用「备份」。",
    "永久删除", true))) return;
  btn.disabled = true; btn.textContent = "删除中…";
  try {
    await api("DELETE", "/api/inventory/handwritten", { target: currentTarget(), name: i.name });
    toast("已删除 " + i.name);
    await fetchInventory();
  } catch (e) {
    btn.disabled = false; btn.textContent = "删除";
    banner("删除 " + i.name + " 失败：" + e.message, true);
  }
}

function summaryToast(sum, name, enable) {
  if (enable) {
    const made = (sum && sum.created || []).find((x) => x.name === name);
    toast(made ? "已在 " + targetLabel(currentTarget()) + " 建立软链 " + name : "已启用 " + name);
  } else {
    toast("已移除软链 " + name);
  }
}

// disableSkill tears down a managed skill's link in the current target. Enabling
// happens in the "+ 添加" drawer, so inventory only ever disables.
async function disableSkill(i, btn) {
  btn.disabled = true; btn.textContent = "停用中…";
  try {
    await api("DELETE", "/api/enabled", { skill: i.selector, target: currentTarget() });
    const sum = await api("POST", "/api/apply"); // returns reconcile Summary
    summaryToast(sum, i.name, false);
    await load();
  } catch (e) {
    btn.disabled = false; btn.textContent = "停用";
    banner("停用 " + i.name + " 失败：" + e.message, true);
  }
}

async function updateSkillSh(name, btn) {
  const old = btn.textContent;
  btn.disabled = true; btn.textContent = "更新中…";
  try {
    const r = await fetch("/api/dirsource/update", {
      method: "POST",
      headers: { Authorization: "Bearer " + TOKEN, "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || r.statusText);
    if (d.ok) toast("已更新 " + name);
    else banner("更新 " + name + " 失败：" + (d.stderr || d.error || "未知错误"), true);
  } catch (e) {
    banner("更新 " + name + " 失败：" + e.message, true);
  }
  btn.disabled = false; btn.textContent = old;
  await fetchInventory();
}

// openMoveModal is the single「移动」flow for every skill, opened from the detail
// modal's footer action. ctx describes the source:
//   {name, desc, sourceKind: "handwritten"|"local"|"git", root?, fromRepo?}
// A unified 目标位置 dropdown lists `local` (the managed store) + every git repo;
// 确定 moves the skill there. `local` only backs up (no push); a git target
// commits + pushes (MIC-0 …). It routes to the right endpoint by source+target.
function openMoveModal(ctx) {
  const m = $("#contribute-modal");
  const repos = state.status.repos || [];
  const sel = $("#contribute-repo"), descIn = $("#contribute-desc");
  const noRepo = $("#contribute-norepo"), okBtn = $("#contribute-ok");
  const descLabel = descIn.previousElementSibling; // the「简述」label
  // 未备份/手写 → 「备份」（首次收进受管库）；已在 local/git 的 → 「移动」（在源之间挪位）。
  // 两者都把名称 + 简述记入清单。
  const verb = ctx.sourceKind === "handwritten" ? "备份" : "移动";
  $("#contribute-title").textContent = verb + "：" + cleanSkillName(ctx.name);

  // Destination options: `local`（受管库；源自身是 @local 时不列）+ 注册本地源
  //（排除源自身）+ 全部 git 仓。
  sel.innerHTML = "";
  if (ctx.sourceKind !== "local") sel.append(ce("option", { value: "local", textContent: "local（本地受管库）" }));
  (state.status.localSources || []).forEach((ls) => {
    if (ctx.sourceKind === "dir" && ls.id === ctx.fromDir) return; // 不能移动到自身
    sel.append(ce("option", { value: "@dir:" + ls.id, textContent: ls.label + "（本地源）" }));
  });
  repos.forEach((rp) => sel.append(ce("option", { value: rp.name, textContent: rp.name })));
  // Default to local when offered, else the first available target.
  sel.value = ctx.sourceKind !== "local" ? "local" : ((sel.options[0] && sel.options[0].value) || "");

  descIn.value = ctx.desc || "";
  descIn.disabled = sel.disabled = false;
  const noDest = sel.options.length === 0; // no place to move to (e.g. @local with no other source)
  noRepo.classList.toggle("hidden", !noDest);
  if (noDest) noRepo.textContent = "尚无可移动到的目标——请先添加一个 git 源仓或本地源再移动。";
  okBtn.disabled = noDest; okBtn.textContent = "确定";

  // 备份总要记录名称 + 简述到清单（不论目标是 local 还是 git 仓），所以简述字段
  // 对所有目标都显示——否则从 local 往 git 移动时会缺简述。
  descIn.style.display = "";
  if (descLabel) descLabel.style.display = "";
  m.classList.remove("hidden");

  const cleanup = () => { okBtn.onclick = sel.onchange = $("#contribute-cancel").onclick = $("#contribute-close").onclick = m.onclick = null; };
  const close = () => { m.classList.add("hidden"); cleanup(); };
  $("#contribute-cancel").onclick = close;
  $("#contribute-close").onclick = close;
  m.onclick = (e) => { if (e.target.id === "contribute-modal") close(); };

  okBtn.onclick = async () => {
    const dest = sel.value;
    okBtn.disabled = true; descIn.disabled = sel.disabled = true;
    okBtn.textContent = verb + "中…";
    const res = await doMove(ctx, dest, descIn.value);
    if (res.ok) {
      const gitDest = !(dest === "local" || dest.startsWith("@dir:"));
      const destText = dest === "local" ? "local" : (dest.startsWith("@dir:") ? dirLabel(dest.slice(5)) + "（本地源）" : dest);
      toast("已" + verb + "到 " + destText + (gitDest ? "（暂存，待该仓「同步仓库」推送）" : "") + "：" + cleanSkillName(ctx.name) + (res.warning ? "（" + res.warning + "）" : ""), res.warning ? "info" : "ok");
      close(); await load();
      if (ctx.onDone) ctx.onDone(); // re-render the source popup so the moved skill drops off the list
    } else if (res.partial) {
      banner(verb + "：" + (CONTRIB_ERR[res.code] || res.message), true);
      close(); await load();
      if (ctx.onDone) ctx.onDone();
    } else {
      okBtn.disabled = false; descIn.disabled = sel.disabled = false; okBtn.textContent = "确定";
      banner(verb + "失败：" + (CONTRIB_ERR[res.code] || ADOPT_ERR[res.code] || res.message), true);
    }
  };
}

// doMove routes a move to the right endpoint by (source kind, target). A push
// failure (502 with committed:true) is a partial success — the skill is durable
// in the target, only the push failed. A git→X move may also return ok+warning
// (target done, source-repo cleanup push pending).
async function doMove(ctx, dest, description) {
  // local-like target = the @local store or a registered "@dir:<id>" folder (no
  // git push); anything else is a git repo name.
  const localLike = dest === "local" || dest.startsWith("@dir:");
  let path, body;
  if (ctx.sourceKind === "handwritten") {
    if (localLike) { path = "/api/adopt"; body = { id: ctx.name, root: ctx.root, dest, description }; }
    else { path = "/api/contribute"; body = { id: ctx.name, root: ctx.root, repo: dest, description }; }
  } else if (ctx.sourceKind === "local" || ctx.sourceKind === "dir") {
    // store-like source (@local or a registered folder) → any target.
    const from = ctx.sourceKind === "dir" ? "@dir:" + ctx.fromDir : "local";
    path = "/api/move-local"; body = { id: ctx.name, from, to: dest, description };
  } else { // git source
    path = "/api/move"; body = { name: ctx.name, fromRepo: ctx.fromRepo, toRepo: dest, description };
  }
  const r = await fetch(path, {
    method: "POST",
    headers: { Authorization: "Bearer " + TOKEN, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = await r.json().catch(() => ({}));
  if (r.ok) return { ok: true, warning: data.warning };
  return { ok: false, partial: !!data.committed, code: data.error_code, message: data.error || r.statusText };
}

// --- enable / 自动同步 helpers (used by the per-source skill modals) ---

// shadowTargetForEnable returns the dir of an already-enabled same-name skill in
// another target of the SAME harness — i.e. enabling linkName into `target` now
// would create a global↔project shadow (project wins, the other becomes dead).
// Returns null when no shadow would result. Only considers OUR managed enables.
function shadowTargetForEnable(linkName, target) {
  const tgt = state.targets.find((t) => t.dir === target);
  const h = tgt && tgt.harness;
  if (!h) return null;
  for (const e of state.status.enabled || []) {
    if (e.target === target) continue;
    const ot = state.targets.find((t) => t.dir === e.target);
    if (!ot || ot.harness !== h) continue;
    const nm = String(e.skill).split("/").pop();
    if (nm !== "*" && nm === linkName) return e.target;
  }
  return null;
}

// confirmShadowEnable warns before linking when the enable would shadow a same-name
// skill already enabled in another same-harness directory (Q2: confirm before linking).
async function confirmShadowEnable(linkName, target) {
  const other = shadowTargetForEnable(linkName, target);
  if (!other) return true;
  return confirmModal(
    "「" + linkName + "」已在 " + targetLabel(other) + " 启用。\n" +
      "再在 " + targetLabel(target) + " 启用会形成同名遮蔽：同一 harness 下只有项目级目录里的那份生效，另一份冗余。\n仍要继续？",
    "仍然启用"
  );
}

// healthUnitCount counts the resolvable units the health modal shows: shadow
// conflicts sharing a target-pair collapse into ONE card; other conflicts and each
// sync error count individually. Keeps the footer badge in step with the modal so
//「健康度 N 项待处理」N equals the number of cards/items actually rendered.
function healthUnitCount(s) {
  const conflicts = (s && s.conflicts) || [];
  const errors = (s && s.errors) || [];
  const pairs = new Set();
  let units = 0;
  conflicts.forEach((c) => {
    const ts = [...new Set(c.targets || [])];
    if (c.kind === "shadow" && ts.length === 2) {
      const key = ts.slice().sort().join(" ");
      if (pairs.has(key)) return; // same 父⇄子 pair → already counted as one card
      pairs.add(key);
    }
    units++;
  });
  return units + errors.length;
}

function renderSummary() {
  const f = $("#summary"); f.innerHTML = "";
  const s = state.status.lastSummary;
  if (!s) { f.textContent = "尚未运行同步。"; return; }
  const parts = [];
  if (s.created && s.created.length) parts.push("新增 " + s.created.length);
  if (s.removed && s.removed.length) parts.push("移除 " + s.removed.length);
  if (s.pruned && s.pruned.length) parts.push("清理悬空 " + s.pruned.length);
  f.append(ce("span", { textContent: parts.length ? "上次同步：" + parts.join("，") : "上次同步：无变化" }));
  // 不再在页脚堆砌每条冲突/错误（无价值的一墙告警）。只在有问题时显示一个可点击的
  // 「健康度」入口，详情与解决都在弹窗里完成。计数与弹窗呈现的「可处理单元」一致：
  // 同一对目录的遮蔽合并成一项，其余冲突与每条同步错误各算一项。
  const issues = healthUnitCount(s);
  if (issues) {
    const h = ce("span", { className: "health-warn", textContent: "⚠ 健康度 " + issues + " 项待处理", title: "撞名 / 遮蔽 / 嵌套 / 同步错误——点击查看并解决" });
    h.onclick = openHealthModal;
    f.append(h);
  }
}

// humanizeSyncError turns a raw reconcile error into a clear cause + suggestion.
// The raw string stays available via the row's title (hover) for debugging.
function humanizeSyncError(raw) {
  const r = String(raw || "");
  // link <target>/<name> -> <source>: <inner>
  const m = r.match(/^link\s+(.+?)\s+->\s+(.+?):\s+([\s\S]+)$/);
  if (m) {
    const name = m[1].split("/").pop();
    const inner = m[3];
    if (/non-owned path|foreign link|occupied/.test(inner)) {
      return {
        why: `跳过链接「${name}」：目标位置已被一个非本工具创建的文件/链接占用，按「绝不覆盖真身」原则未做改动。`,
        fix: `若要让 SkillManager 接管，请先手动移除该位置的占用项；或为本工具的 skill 起一个别名避开撞名。`,
      };
    }
    if (/real path|diverged/.test(inner)) {
      // m[1] is the full link path "<target>/<name>"; split on the last separator
      // (handle both / and \) so the relink action gets the target dir + name.
      const full = m[1];
      const idx = Math.max(full.lastIndexOf("/"), full.lastIndexOf("\\"));
      return {
        why: `跳过链接「${name}」：原本的软链位置已变成真实文件/目录（可能被其它工具替换）。`,
        fix: `库里已有该 skill 的归档，可一键移除本地这份并重建软链（不会丢内容）。`,
        relink: idx > 0 ? { target: full.slice(0, idx), name: full.slice(idx + 1) } : null,
      };
    }
    return { why: `链接「${name}」失败。`, fix: `查看本行提示（悬停可见原始信息）后重试。` };
  }
  // unlink <path>: <inner>
  const u = r.match(/^unlink\s+(.+?):\s+([\s\S]+)$/);
  if (u) {
    const name = u[1].split("/").pop();
    return { why: `移除软链「${name}」失败。`, fix: `检查文件权限或它是否被其它程序占用。` };
  }
  if (/^prune:/.test(r)) {
    return { why: `清理悬空链接时出错。`, fix: `稍后重试；若反复出现，检查目标目录权限。` };
  }
  return { why: r, fix: "" };
}

// setDetailSource shows the skills.sh lockfile source (台账来源) in the detail
// modal, or hides the line for skills that have none (git / local / plugin).
function setDetailSource(d) {
  const el = $("#modal-source");
  el.innerHTML = "";
  if (d && d.source) {
    el.append(ce("span", { className: "ms-label", textContent: "台账来源" }));
    el.append(ce("span", { className: "ms-val", textContent: d.source }));
    if (d.sourceUrl) {
      el.append(ce("span", { className: "ms-sep", textContent: "·" }));
      el.append(ce("span", { className: "ms-url", textContent: d.sourceUrl }));
    }
    el.title = d.sourceUrl || d.source;
    el.classList.remove("hidden");
  } else {
    el.classList.add("hidden");
  }
}

// setPluginActions fills the detail modal's action row for a plugin skill: a
// delegated「更新插件」button (only when the claude CLI is available, cc only)
// plus a note. Hidden / cleared for non-plugin skills.
function setPluginActions(plugin, harness) {
  const el = $("#modal-actions");
  el.innerHTML = "";
  if (!plugin) { el.classList.add("hidden"); return; }
  // 更新入口统一放在「插件」组第一行的工具条（只对确有更新的插件显示），详情里不再放
  // 按钮，避免对无法检测/无法更新的插件给出无效操作。
  el.append(ce("span", { className: "ma-note", textContent: "只读 · 由 harness 插件系统管理。有更新时在「插件」组第一行委托更新。" }));
  el.classList.remove("hidden");
}
function clearModalActions() { const el = $("#modal-actions"); el.innerHTML = ""; appendExportAction(el); el.classList.toggle("hidden", el.children.length === 0); }

// setInventoryActions fills the detail modal's footer with an inventory item's
// contextual actions (move/backup, quick-upload, disable, delete, update), keyed
// by kind. The card itself shows only「操作」; choosing an action closes the
// detail modal and runs it (each action gives its own toast/banner + refresh).
function setInventoryActions(i) {
  const el = $("#modal-actions");
  el.innerHTML = "";
  const closeDetail = () => $("#modal").classList.add("hidden");
  const add = (label, cls, title, fn) => {
    const b = ce("button", { className: cls, textContent: label });
    if (title) b.title = title;
    b.onclick = () => { closeDetail(); fn(b); };
    el.append(b);
  };
  if (i.kind === "plugin") {
    el.append(ce("span", { className: "ma-note", textContent: "只读 · 由 harness 插件系统管理。有更新时在「插件」组第一行委托更新。" }));
    el.classList.remove("hidden");
    return;
  }
  // 右侧详情不操作本体：移动/删除本体、整仓同步、启用一律回到左侧对应「源」弹窗（git
  // 仓 / skills.sh / 本地源）。这里只留「停用」（拆当前目录软链，可逆，不碰本体）；
  // 手写未备份是「尚未入源」的引导态，例外地保留「备份/删除」作为纳入源/丢弃的入口。
  if (i.managed && i.selector && !i.follow) {
    add("停用", "danger small inv-off", "拆除此目录下的软链（不影响真身与其它目录）", (b) => disableSkill(i, b));
  } else if (i.kind === "handwritten") {
    add("备份", "ghost small", "备份到本地受管库或某个 git 源仓（记入清单：名称 + 简述）", () => openMoveModal({ name: i.name, desc: i.description, sourceKind: "handwritten", root: currentTarget() }));
    add("删除", "danger small", "永久删除该手写 skill 的真身目录（不可恢复）", (b) => deleteHandwritten(i, b));
  } else if (i.kind === "skills.sh") {
    if (state.npxAvailable) add("更新", "ghost small", "调用 npx skills update 更新（由 skills.sh 管理）", (b) => updateSkillSh(i.name, b));
    add("停用", "danger small inv-off", "拆除此目录下的软链（skills.sh 下次 update 可能重新投影回来）", (b) => disableSkillsShLink(i, b));
  } else if (i.kind === "unknown") {
    add("删除", "danger small", "只删此软链，不动它指向的目标", (b) => deleteStrayLink(i, b));
  }
  // 整仓自动同步（follow）下，单个 skill 不能单独停用——它由源的「自动同步」整体控制。
  // 给出明确文案，而不是让详情底部空着，否则用户找不到「为什么没有停用」。
  if (i.managed && i.follow && i.selector) {
    const src = sourceMeta(i.selector.split("/")[0]).label;
    el.append(ce("span", { className: "ma-note", textContent: "「" + src + "」整仓自动同步中，不能单独停用该 skill。如需停用，请到左侧该源弹窗点「取消启用」（会移除该源在本目录的全部 skill）。" }));
  }
  appendExportAction(el);
  el.classList.toggle("hidden", el.children.length === 0);
}

// setSkillsShActions adds an「卸载」action to a skills.sh skill's detail. Uninstall
// removes the real file in ~/.agents/skills AND every symlink (ours + skills.sh's).
function setSkillsShActions(name) {
  const el = $("#modal-actions");
  el.innerHTML = "";
  el.append(ce("span", { className: "ma-note", textContent: "npx skills 管理（只读）。更新在卡片里委托；卸载见右。" }));
  appendExportAction(el);
  const btn = ce("button", { className: "danger small", textContent: "卸载" });
  btn.onclick = () => uninstallSkillsSh(name, btn);
  el.append(btn);
  el.classList.remove("hidden");
}

// uninstallSkillsSh delegates `npx skills remove <name> -g -y` (drops canonical +
// all agent symlinks skills.sh made) after SkillManager tears down its own links.
async function uninstallSkillsSh(name, btn, onDone) {
  if (nodeDependencyUI && !nodeDependencyUI.requireNode(btn)) return;
  if (!(await confirmModal("卸载 " + name + "？\n会删除 ~/.agents/skills 下的真身文件 + 所有软链（含本工具建立的、以及 skills.sh 装到各 agent 的）。此操作不可撤销。", "卸载", true))) return;
  const old = btn.textContent; btn.disabled = true; btn.textContent = "卸载中…";
  try {
    const d = await api("POST", "/api/skillssh/remove", { name });
    if (d && d.ok) {
      toast("已卸载 " + name + (d.removedLinks ? "（含 " + d.removedLinks + " 处本工具软链）" : ""));
      await load();
      if (onDone) await onDone(); // 列表弹窗：原地重渲；否则关闭详情弹窗
      else $("#modal").classList.add("hidden");
    } else {
      throw new Error((d && (d.error || d.stderr)) || "未知错误");
    }
  } catch (e) {
    btn.disabled = false; btn.textContent = old;
    banner("卸载 " + name + " 失败：" + e.message, true);
  }
}

// updatePlugin delegates a plugin update to the harness CLI (claude plugin update
// <id> -s <scope>). It never takes ownership; effect applies after a Claude Code
// restart. `t` is {name,id,scope} from the outdated check — the exact id + scope
// the CLI needs (a bare name / wrong scope makes it report "not found").
async function updatePlugin(t, btn) {
  const old = btn.textContent; btn.disabled = true; btn.textContent = "更新中…";
  try {
    const d = await api("POST", "/api/plugin/update", { id: t.id, scope: t.scope, harness: "cc" });
    if (d && d.ok) toast(d.status === "current" ? t.name + " 已是最新版本，无需更新" : "已更新 " + t.name + "，重启 Claude Code 后生效");
    else banner("更新插件 " + t.name + " 失败：" + ((d && (d.stderr || d.error)) || "未知错误"), true);
  } catch (e) {
    banner("更新插件 " + t.name + " 失败：" + e.message, true);
  } finally {
    btn.disabled = false; btn.textContent = old;
  }
}

// pluginToolbar is the first row of the「插件」组: one「更新」button per installed
// plugin in this group + 「全部更新」right-aligned. This is a MANUAL model (like
// skills.sh): the CLI can't tell us which plugins have updates (no comparable
// marketplace version), so we don't claim — we just offer to委托 update each, with
// a note. id + scope come from `claude plugin list`.
function pluginToolbar(items) {
  const bar = ce("div", { className: "plugin-toolbar" });
  if (!(state.status && state.status.claudeCli)) {
    bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "未找到 claude CLI，无法在此更新插件（可用 /plugin 更新）" }));
    return bar;
  }
  const pi = state.pluginInstalled;
  if (pi === null) { fetchPluginInstalled(); bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "加载插件信息中…" })); return bar; }
  if (!pi.available) { bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "无法获取插件信息，暂不提供委托更新" })); return bar; }
  const names = new Set(items.filter((x) => x.harness === "cc" && x.plugin).map((x) => x.plugin));
  const targets = (pi.list || []).filter((t) => names.has(t.name));
  if (!targets.length) { bar.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "本组无可委托更新的 Claude Code 插件" })); return bar; }
  // 逐插件按钮放在一个会换行的容器里（占满左侧），「全部更新」固定在最右、不随换行下移。
  const btns = ce("div", { className: "plugin-toolbar-btns" });
  targets.forEach((t) => {
    const b = ce("button", { className: "ghost small", textContent: "更新 " + abbrevLabel(t.name), title: "委托 claude plugin update 更新「" + t.name + "」插件，重启后生效" });
    b.onclick = () => updatePlugin(t, b);
    btns.append(b);
  });
  bar.append(btns);
  const allBtn = ce("button", { className: "small", textContent: "全部更新", title: "逐个委托更新这 " + targets.length + " 个插件" });
  allBtn.onclick = () => updateAllPlugins(targets, allBtn);
  bar.append(allBtn);
  return bar;
}

async function fetchPluginInstalled() {
  if (state.pluginInstalledLoading) return;
  state.pluginInstalledLoading = true;
  try {
    const d = await api("GET", "/api/plugins/installed");
    state.pluginInstalled = d && d.available ? { available: true, list: d.plugins || [] } : { available: false, list: [] };
  } catch { state.pluginInstalled = { available: false, list: [] }; }
  finally { state.pluginInstalledLoading = false; renderInventory(); }
}

async function updateAllPlugins(targets, btn) {
  if (!targets || !targets.length) return;
  const names = targets.map((t) => t.name);
  if (!(await confirmModal("将逐个委托 claude plugin update 更新 " + targets.length + " 个插件：\n" + names.join("、") + "\n更新后重启 Claude Code 生效。继续？", "全部更新"))) return;
  const old = btn.textContent; btn.disabled = true; btn.textContent = "更新中…";
  let updated = 0, current = 0; const fail = [];
  for (const t of targets) {
    try { const d = await api("POST", "/api/plugin/update", { id: t.id, scope: t.scope, harness: "cc" }); if (d && d.ok) { if (d.status === "current") current++; else updated++; } else fail.push(t.name); }
    catch { fail.push(t.name); }
  }
  btn.disabled = false; btn.textContent = old;
  if (fail.length) banner("插件更新：更新 " + updated + " 个，已最新 " + current + " 个，失败 " + fail.length + "（" + fail.join("、") + "）", true);
  else if (updated) toast("已更新 " + updated + " 个插件（另 " + current + " 个已最新），重启 Claude Code 后生效");
  else toast(current + " 个插件均已是最新，无需更新");
}

async function openDetail(repo, name) {
  $("#modal-title").textContent = name;
  $("#modal-desc").textContent = "";
  resetAuthorship();
  setDetailSource(null);
  exportCtx = { repo, name }; // set before the action row renders; source skills are real dirs
  if (repo === AGENTS_NS) setSkillsShActions(name); else clearModalActions();
  $("#modal-content").textContent = "加载中…";
  $("#modal").classList.remove("hidden");
  try {
    const d = await api("GET", "/api/skill?repo=" + encodeURIComponent(repo) + "&name=" + encodeURIComponent(name));
    $("#modal-desc").textContent = d.description || "";
    setDetailSource(d);
    $("#modal-content").textContent = d.content || "(空)";
    loadAuthorship(repo, name); // git-source only; best-effort, fills in async
  } catch (e) {
    $("#modal-content").textContent = "加载失败：" + e.message;
  }
}

// exportCtx identifies the skill the detail modal currently shows so the
// 「导出 zip」action knows what to export: {repo,name} for a source skill,
// {target,name} for a project-side skill, or null for plugins (no export). The
// export endpoint resolves the real directory — following a project-side
// symlink/junction to its target — so export works whether the skill is a real
// dir or a managed link. appendExportAction adds the button into #modal-actions
// alongside the other contextual actions; each action-setter calls it (the row's
// innerHTML is rebuilt per open, so it can't live as a static node).
let exportCtx = null;
function appendExportAction(el) {
  if (!exportCtx) return;
  // act-export carries margin-left:auto so 导出 zip sits at the right edge of the
  // action row, regardless of which other actions precede it.
  const b = ce("button", { className: "ghost small act-export", textContent: "导出 skill", title: "把该 skill 打包成 zip，保存到 ~/.skillmanage/exports/（跨平台一致，使用 Go 原生 zip）" });
  b.onclick = () => doExport(exportCtx, b);
  el.append(b);
}
async function doExport(params, b) {
  const old = b.textContent;
  b.disabled = true; b.textContent = "导出中…";
  try {
    const d = await api("POST", "/api/export", params);
    toast("已导出 " + d.file + " → " + d.dir, "ok");
  } catch (e) {
    banner("导出失败：" + e.message, true);
  }
  b.disabled = false; b.textContent = old;
}

// resetAuthorship hides the 创建人/最近修改 line (the detail modal is shared).
function resetAuthorship() {
  const el = $("#modal-authorship");
  if (el) { el.textContent = ""; el.classList.add("hidden"); }
}

// loadAuthorship fills the detail modal's 创建人 / 最近修改 line for a git-source
// skill (other sources have no git history, so it stays hidden). Best-effort.
async function loadAuthorship(repo, name) {
  if (!(state.status.repos || []).some((r) => r.name === repo)) return; // git repos only
  try {
    const d = await api("GET", "/api/skill-authorship?repo=" + encodeURIComponent(repo) + "&skill=" + encodeURIComponent(name));
    const a = d && d.authorship;
    if (!a) return;
    const parts = [];
    if (a.creator) parts.push("创建人：" + a.creator + (a.createdAt ? "（" + a.createdAt + "）" : ""));
    if (a.lastAuthor && (a.lastAuthor !== a.creator || a.lastAt !== a.createdAt)) {
      parts.push("最近修改：" + a.lastAuthor + (a.lastAt ? "（" + a.lastAt + "）" : ""));
    }
    if (!parts.length) return; // staged-but-uncommitted skill → no history yet
    const el = $("#modal-authorship");
    el.textContent = parts.join("　·　");
    el.classList.remove("hidden");
  } catch { /* authorship is best-effort */ }
}

// openPluginDetail reads a plugin skill's SKILL.md from its install tree.
async function openPluginDetail(plugin, name, harness) {
  $("#modal-title").textContent = name;
  $("#modal-desc").textContent = "";
  resetAuthorship();
  setDetailSource(null);
  exportCtx = null; // plugin skills are harness-managed — no export
  setPluginActions(plugin, harness);
  $("#modal-content").textContent = "加载中…";
  $("#modal").classList.remove("hidden");
  try {
    const d = await api("GET", "/api/plugin-skill?plugin=" + encodeURIComponent(plugin) + "&name=" + encodeURIComponent(name) + "&harness=" + encodeURIComponent(harness || ""));
    $("#modal-desc").textContent = d.description || "";
    $("#modal-content").textContent = d.content || "(空)";
  } catch (e) {
    $("#modal-content").textContent = "加载失败：" + e.message;
  }
}

// openDetailAt reads a skill's SKILL.md by its physical location in the current
// target dir — used for non-managed inventory kinds (skills.sh / 未备份 / 未知)
// that have no source-repo selector.
async function openDetailAt(name) {
  $("#modal-title").textContent = name;
  $("#modal-desc").textContent = "";
  resetAuthorship();
  setDetailSource(null);
  // 现状/inventory 视图里的条目都是软链（本地真身除外），只能操作链（启用/停用），
  // 不在此对「本体」下手（卸载等本体操作只属于「源」视图，如 skills.sh 列表弹窗）。
  exportCtx = { target: currentTarget(), name }; // resolves the symlink to its real target
  clearModalActions();
  $("#modal-content").textContent = "加载中…";
  $("#modal").classList.remove("hidden");
  try {
    const d = await api("GET", "/api/skill-at?target=" + encodeURIComponent(currentTarget()) + "&name=" + encodeURIComponent(name));
    $("#modal-desc").textContent = d.description || "";
    setDetailSource(d);
    $("#modal-content").textContent = d.content || "(空)";
  } catch (e) {
    $("#modal-content").textContent = "加载失败：" + e.message;
  }
}

// updateNow is the GIT update-all action (git 仓 group's「全量更新」). npx/skills.sh
// has its own update (updateSkillsShAll) — one git path, one npx path.
// showUpdatePanel 弹出「本次更新内容」面板：逐仓列出本次从上游拉取的 skill 变更
// （＋新增/～更新/－移除）。全量「更新全部」与单仓「同步仓库」共用，明细一致。
async function showUpdatePanel(ups) {
  const sym = { added: "＋ 新增", updated: "～ 更新", removed: "－ 移除" };
  const total = ups.reduce((n, u) => n + ((u.changes || []).length), 0);
  const blocks = ups.map((u) => {
    const items = (u.changes || []).map((c) => "    " + (sym[c.kind] || c.kind) + "  " + c.name).join("\n");
    return "【" + u.repo + "】\n" + items;
  });
  await confirmModal("本次更新内容（" + total + " 个 skill 变更）：\n\n" + blocks.join("\n\n"), "知道了", false);
}

async function updateNow(force) {
  toast("同步中…", "info");
  let sum = null;
  try { sum = await api("POST", "/api/update-now", { force: !!force }); }
  catch (e) { banner("同步失败：" + e.message, true); await load(); return; }
  await load();
  // Dirty mirrors were skipped (force=false) → offer to restore + update them
  // (this replaced the old separate 强制更新 button). Skip when already forcing.
  if (!force && sum && sum.dirtyRepos && sum.dirtyRepos.length) {
    if (await confirmModal(
      "以下镜像有本地改动，已跳过未更新：\n\n" + sum.dirtyRepos.join("、") +
      "\n\n镜像是只读副本，正常不该有本地改动。是否还原本地改动并更新（git reset --hard + clean -fd）？",
      "还原并更新", true)) {
      await updateNow(true);
      return;
    }
  }
  // 展示本次从上游拉取的 skill 变更（新增/更新/移除），逐仓列出。
  const ups = (sum && sum.updates) || [];
  if (ups.length) {
    await showUpdatePanel(ups);
  } else {
    const changed = sum && ((sum.created || []).length + (sum.removed || []).length + (sum.pruned || []).length);
    if (changed) toast("更新完成：链接 新增 " + (sum.created || []).length + " · 移除 " + (sum.removed || []).length + (sum.pruned && sum.pruned.length ? " · 清理 " + sum.pruned.length : ""));
    else toast("git 源已是最新，无变化");
  }
}

// events
$("#search").oninput = (e) => { state.search = e.target.value; renderInventory(); };
// Local search is real-time on input. The「搜索」button (and Enter) additionally
// fire the skills.sh online query — always, no opt-in (KTD6: online is explicit,
// never per-keystroke, because npx cold-start is slow).
$("#search").addEventListener("keydown", (e) => { if (e.key === "Enter") runOnlineSearch(); });
$("#online-search-btn").onclick = runOnlineSearch;

// 齿轮 → 在线结果筛选弹窗：两组单选——「来源」（全部/skills.sh/skillsmp，可扩展）+
// 「排序」（默认/按下载·星 降序/升序）。两源指标不同（下载 vs 星），故来源按源筛、排序
// 统一按各源的热度数值。纯客户端，持久化到 localStorage。
(function wireOnlineFilter() {
  const btn = $("#online-filter-btn"), modal = $("#online-filter-modal");
  const validSrc = { both: 1, skillssh: 1, skillsmp: 1, skillhub: 1 };
  const validSort = { default: 1, desc: 1, asc: 1 };
  const validLimit = { 0: 1, 10: 1, 20: 1, 50: 1 };
  state.onlineSrc = validSrc[localStorage.getItem("sm.onlineSrc")] ? localStorage.getItem("sm.onlineSrc") : "both";
  state.onlineSort = validSort[localStorage.getItem("sm.onlineSort")] ? localStorage.getItem("sm.onlineSort") : "desc";
  state.onlineLimit = validLimit[parseInt(localStorage.getItem("sm.onlineLimit"), 10)] ? parseInt(localStorage.getItem("sm.onlineLimit"), 10) : 0;
  const reflect = () => {
    modal.querySelectorAll('input[name="onlineSrc"]').forEach((r) => { r.checked = r.value === state.onlineSrc; });
    modal.querySelectorAll('input[name="onlineSort"]').forEach((r) => { r.checked = r.value === state.onlineSort; });
    modal.querySelectorAll('input[name="onlineLimit"]').forEach((r) => { r.checked = +r.value === state.onlineLimit; });
    btn.classList.toggle("active", state.onlineSrc !== "both" || state.onlineSort !== "default" || state.onlineLimit !== 0); // 有筛选时齿轮高亮
  };
  reflect();
  btn.onclick = () => { reflect(); modal.classList.remove("hidden"); };
  $("#online-filter-close").onclick = () => modal.classList.add("hidden");
  modal.onclick = (e) => { if (e.target.id === "online-filter-modal") modal.classList.add("hidden"); };
  // skillsmp key 入口（仅存本机）——从筛选 ⚙ 里进。SkillHub 无鉴权，无需入口。
  { const b = $("#of-smp-key"); if (b) b.onclick = () => { modal.classList.add("hidden"); openSmpKeyModal(); }; }
  modal.querySelectorAll('input[name="onlineSrc"]').forEach((r) => {
    r.onchange = () => {
      state.onlineSrc = validSrc[r.value] ? r.value : "both";
      localStorage.setItem("sm.onlineSrc", state.onlineSrc);
      reflect(); renderInventory();
    };
  });
  modal.querySelectorAll('input[name="onlineSort"]').forEach((r) => {
    r.onchange = () => {
      state.onlineSort = validSort[r.value] ? r.value : "default";
      localStorage.setItem("sm.onlineSort", state.onlineSort);
      reflect(); renderInventory();
    };
  });
  modal.querySelectorAll('input[name="onlineLimit"]').forEach((r) => {
    r.onchange = () => {
      const v = parseInt(r.value, 10) || 0;
      state.onlineLimit = validLimit[v] ? v : 0;
      localStorage.setItem("sm.onlineLimit", String(state.onlineLimit));
      reflect(); renderInventory();
    };
  });
})();

// skillsmp API key（每位用户填自己的免费 key，绝不内置/共享；仅存本机、不随导出）。
// 不放在筛选弹窗里（避免一进来就被配置项吓到）——平时匿名直接用，仅当触发封控（403）
// 时，错误提示里出现《配置 API key》入口，点开此独立小弹窗再填。
function openSmpKeyModal() {
  const m = $("#smp-key-modal");
  if (!m) return;
  refreshSmpKeyStatus();
  $("#smp-key-input").value = "";
  m.classList.remove("hidden");
  setTimeout(() => $("#smp-key-input").focus(), 0);
}
async function refreshSmpKeyStatus() {
  const el = $("#smp-key-status");
  if (!el) return;
  try {
    const r = await api("GET", "/api/skillsmp/key");
    if (r && r.configured) el.innerHTML = "已配置 <b>" + (r.hint || "") + "</b> · 认证额度 500 次/天";
    else el.textContent = "未配置 · 匿名 50 次/天，易触发 403 风控";
  } catch { /* 状态获取失败不阻塞 */ }
}
(function wireSmpKey() {
  const m = $("#smp-key-modal");
  if (!m) return;
  $("#smp-key-close").onclick = () => m.classList.add("hidden");
  m.onclick = (e) => { if (e.target.id === "smp-key-modal") m.classList.add("hidden"); };
  $("#smp-key-save").onclick = async () => {
    const key = ($("#smp-key-input").value || "").trim();
    if (!key) { toast("请先粘贴 key"); return; }
    try {
      await api("POST", "/api/skillsmp/key", { key });
      toast("已保存 skillsmp API key（仅存本机）");
      m.classList.add("hidden");
      if (state.skillsShOnline && state.skillsShOnline.term) runOnlineSearch(); // 用新额度立刻重查
    } catch (e) { toast("保存失败：" + e.message); }
  };
  $("#smp-key-clear").onclick = async () => {
    try {
      await api("POST", "/api/skillsmp/key", { key: "" });
      toast("已清除 skillsmp API key");
      await refreshSmpKeyStatus();
      $("#smp-key-input").value = "";
    } catch (e) { toast("清除失败：" + e.message); }
  };
})();

// submitGitRepo handles the git-source add form. The HTTPS credential is entered
// INLINE in the same modal (no second popup): if the URL is an https host we have
// no stored cred for and the user typed a token, save it first so the very first
// sync authenticates. A host we already have a cred for is reused silently. Empty
// token = treat as public — the per-repo「填写凭据」button remains the fallback if
// it turns out private (addRepoAndSync surfaces authHint).
async function submitGitRepo(url, branch) {
  if (!url) return;
  const host = httpsHost(url);
  if (host && !Object.prototype.hasOwnProperty.call(state.credHosts, host)) {
    const token = $("#repo-cred-token").value;
    if (token) {
      const username = $("#repo-cred-user").value.trim();
      try { await api("POST", "/api/credentials", { host, username, token }); }
      catch (err) { banner("保存凭据失败：" + err.message, true); return; }
    }
  }
  await addRepoAndSync(url, branch);
}

// updateRepoCredSection reacts to the URL field: shows the inline credential
// inputs only for an https host without a stored cred, a reuse note when one
// already exists, and nothing for SSH/git@ (which authenticates via SSH key).
function updateRepoCredSection() {
  const host = httpsHost($("#repo-url").value.trim());
  const sec = $("#repo-cred-section"), fields = $("#repo-cred-fields"), status = $("#repo-cred-status");
  if (!host) { sec.classList.add("hidden"); return; }
  sec.classList.remove("hidden");
  if (Object.prototype.hasOwnProperty.call(state.credHosts, host)) {
    status.textContent = "将复用 " + host + " 的已存凭据，无需重复填写。";
    fields.classList.add("hidden");
  } else {
    status.textContent = host + "：私有仓在下面填令牌；公开仓留空直接添加。";
    fields.classList.remove("hidden");
  }
}

function openGitRepoModal() {
  $("#repo-url").value = "";
  $("#repo-branch").value = "";
  $("#repo-cred-user").value = "";
  $("#repo-cred-token").value = "";
  updateRepoCredSection();
  $("#git-repo-modal").classList.remove("hidden");
  $("#repo-url").focus();
}
function closeGitRepoModal() { $("#git-repo-modal").classList.add("hidden"); }

// --- 健康度 (health): 同步层冲突 撞名 / 遮蔽 / 嵌套 + 同步错误 ---
// 这些是「手动启用/自动同步」操作落到目标目录后才会出现的链接层问题，可在弹窗里
// 逐条停用解决。与顶部「冲突」（库内跨源重复/重叠，见下方）是两件事。
const CONFLICT_KIND = {
  collision: "撞名（多个源争用同一目录下的同名 skill，只能保留一份）",
  shadow: "遮蔽（同一 harness 的全局与项目目录都有同名，项目级生效、另一份冗余）",
  nested: "嵌套（链到 Codex 的源含子 skill，可能污染 Codex 列表）",
};
function badgeClassForLabel(label) {
  return label === "skills.sh" ? "src-skillssh" : label === "local" ? "src-local" : "src-git";
}
async function openHealthModal() {
  const body = $("#health-body");
  body.innerHTML = "加载中…";
  $("#health-modal").classList.remove("hidden");
  try { renderHealthBody(await api("GET", "/api/conflicts")); }
  catch (e) { body.innerHTML = ""; body.append(ce("div", { className: "empty", textContent: "加载失败：" + e.message })); }
}
function closeHealthModal() { $("#health-modal").classList.add("hidden"); }
function renderHealthBody(list) {
  const body = $("#health-body"); body.innerHTML = "";
  const errors = (state.status.lastSummary && state.status.lastSummary.errors) || [];
  list = list || [];
  if (!list.length && !errors.length) { body.append(ce("div", { className: "empty", textContent: "一切正常 🎉" })); return; }
  // 遮蔽冲突（父↔子成对）按【文件夹对】分组：每组是「父 ⇄ 一个子」的关系，框里列出
  // 这一对之间所有同名 skill，并给两个一键取舍按钮（保留父 / 保留子）。撞名（同目录
  // 多源）、嵌套不属于文件夹对，仍按单条处理。
  const shadowPairs = list.filter((c) => c.kind === "shadow" && c.candidates && new Set(c.candidates.map((x) => x.target)).size === 2);
  const others = list.filter((c) => !shadowPairs.includes(c));

  // group shadow conflicts by their unordered (父,子) target pair.
  const groups = new Map();
  shadowPairs.forEach((c) => {
    const ts = [...new Set(c.candidates.map((x) => x.target))].sort();
    const key = ts.join(" ");
    let g = groups.get(key);
    if (!g) {
      const meta = {};
      c.candidates.forEach((x) => { meta[x.target] = { label: x.targetLabel, scope: x.scope, follow: x.follow, source: x.sourceLabel }; });
      g = { targets: ts, meta, items: [] };
      groups.set(key, g);
    }
    g.items.push(c);
  });

  // roleOf maps a target's scope to a 父/子 label, or "" when scope is unknown
  // (defensive: a normal shadow pair is always one user + one project, but stale
  // or malformed data must not mislabel both sides — then we show paths only).
  const roleOf = (scope) => (scope === "user" ? "父" : scope === "project" ? "子" : "");
  for (const g of groups.values()) {
    // order父(user) first, 子(project) second; fall back to given order.
    const ordered = g.targets.slice().sort((a, b) => (g.meta[a].scope === "user" ? -1 : 1) - (g.meta[b].scope === "user" ? -1 : 1));
    const box = ce("div", { className: "cf-pair" });
    const head = ce("div", { className: "cf-pair-head" });
    ordered.forEach((t, i) => {
      if (i) head.append(ce("span", { className: "cf-pair-arrow", textContent: "⇄" }));
      const tag = ce("span", { className: "cf-pair-folder" });
      const role = roleOf(g.meta[t].scope);
      if (role) tag.append(ce("span", { className: "cf-pair-role", textContent: role }));
      tag.append(ce("span", { textContent: g.meta[t].label }));
      head.append(tag);
    });
    box.append(head);
    box.append(ce("div", { className: "cf-pair-sub", textContent: g.items.length + " 个同名 skill 在这两处重复（项目级生效，另一份冗余）" }));
    const btns = ce("div", { className: "cf-batch-btns" });
    ordered.forEach((t) => {
      const role = roleOf(g.meta[t].scope);
      const b = ce("button", { className: "small", textContent: "全部保留 " + (role ? role + "（" + g.meta[t].label + "）" : g.meta[t].label) });
      b.onclick = () => resolveHealthBatch(g.items, t);
      btns.append(b);
    });
    box.append(btns);
    const sk = ce("div", { className: "cf-pair-skills" });
    g.items.forEach((c) => {
      const row = ce("div", { className: "cf-pair-skill" });
      const src = (c.candidates[0] && c.candidates[0].sourceLabel) || "";
      row.append(ce("span", { className: "cf-skill-name", textContent: c.linkName }));
      if (src) row.append(ce("span", { className: "src-badge " + badgeClassForLabel(src), textContent: src }));
      if (g.meta[g.targets[0]].follow || g.meta[g.targets[1]].follow) row.append(ce("span", { className: "muted", style: "font-size:11px", textContent: "自动同步" }));
      sk.append(row);
    });
    box.append(sk);
    body.append(box);
  }

  // collision (撞名) / nested / anything not a folder-pair → per-item resolution.
  others.forEach((c) => {
    const sec = ce("div", { className: "cf-item" });
    const head = ce("div", { className: "cf-head" });
    head.append(ce("span", { className: "cf-name", textContent: c.linkName }));
    head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: CONFLICT_KIND[c.kind] || c.kind }));
    sec.append(head);
    if (!c.candidates || !c.candidates.length) {
      sec.append(ce("div", { className: "muted", style: "font-size:12px;margin-top:6px", textContent: "找不到可操作的来源条目（可能来自整仓自动同步，请到对应源弹窗里调整）。" }));
      body.append(sec); return;
    }
    const multi = c.candidates.length > 1;
    c.candidates.forEach((cand) => {
      const row = ce("div", { className: "cf-cand" });
      const info = ce("div", { className: "cf-cand-info" });
      info.append(ce("span", { className: "src-badge " + badgeClassForLabel(cand.sourceLabel), textContent: cand.sourceLabel }));
      info.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "→ " + cand.targetLabel + (cand.follow ? " · 自动同步" : "") }));
      row.append(info);
      const btn = ce("button", { className: multi ? "small" : "danger small", textContent: multi ? "保留这个" : "停用", title: multi ? "保留这一份，停用其余" : "停用这一份" });
      btn.onclick = () => resolveConflict(c, cand);
      row.append(btn);
      sec.append(row);
    });
    body.append(sec);
  });

  // 同步错误（reconcile 失败）：弹窗标题里承诺了「同步错误」，这里逐条列出（人话 +
  // 原文 hover）。它们不是按钮可解的——下次成功同步会自动消失——但要让用户看见，
  // 否则页脚计数把它们算进去、弹窗却不显示，就会「N 项待处理」对不上。
  errors.forEach((raw) => {
    const rawStr = typeof raw === "string" ? raw : JSON.stringify(raw); // 防御：Errors 应为字符串
    const h = humanizeSyncError(rawStr); // 返回 {why, fix}
    const sec = ce("div", { className: "cf-item" });
    const head = ce("div", { className: "cf-head" });
    head.append(ce("span", { className: "cf-name", textContent: "同步错误" }));
    head.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "下次成功同步后自动消失" }));
    sec.append(head);
    sec.append(ce("div", { className: "muted", style: "font-size:12px;margin-top:6px;white-space:pre-wrap", textContent: h.why || rawStr, title: rawStr }));
    if (h.fix) sec.append(ce("div", { className: "muted", style: "font-size:12px;margin-top:3px", textContent: "建议：" + h.fix }));
    // 「真身占位」类可一键解决：库里已有归档，删本地这份再重建软链，不丢内容。
    if (h.relink) {
      const btn = ce("button", { className: "small", style: "margin-top:8px", textContent: "移除并重新链接",
        title: "删除目标位置的真实文件/目录，重建指向库中归档的软链。库里已有归档，删除本地不会丢内容。" });
      btn.onclick = async () => {
        if (!(await confirmModal(
          "将删除目标位置的真实文件/目录：\n" + h.relink.target + "/" + h.relink.name +
          "\n\n库里已有该 skill 的归档，删除本地不会丢内容，之后会重建软链指向归档。继续？",
          "移除并重新链接", true))) return;
        btn.disabled = true;
        try { await api("POST", "/api/relink-archived", h.relink); }
        catch (e) { banner("重新链接失败：" + e.message, true); btn.disabled = false; return; }
        toast("已移除本地并重新链接 " + h.relink.name);
        await load();
        try { renderHealthBody(await api("GET", "/api/conflicts")); }
        catch { closeHealthModal(); }
      };
      sec.append(btn);
    }
    body.append(sec);
  });
}
async function resolveConflict(c, keep) {
  const drop = c.candidates.length > 1
    ? c.candidates.filter((x) => !(x.selector === keep.selector && x.target === keep.target))
    : [keep];
  const followDrop = drop.find((d) => d.follow);
  if (followDrop && !(await confirmModal(
    "「" + followDrop.sourceLabel + "」是整仓自动同步，停用它会移除该源在「" + followDrop.targetLabel + "」的全部 skill（不止这一个）。继续？",
    "继续停用", true))) return;
  try {
    for (const d of drop) await api("DELETE", "/api/enabled", { skill: d.selector, target: d.target });
    await api("POST", "/api/apply");
  } catch (e) { banner("解决冲突失败：" + e.message, true); return; }
  toast("已处理冲突 " + c.linkName);
  await load();
  try { renderHealthBody(await api("GET", "/api/conflicts")); }
  catch { closeHealthModal(); }
}
// resolveHealthBatch keeps one target folder across many cross-folder conflicts and
// drops the same-named candidates in every other folder in one pass.
async function resolveHealthBatch(batchable, keepTarget) {
  const keepLabel = (batchable[0].candidates.find((x) => x.target === keepTarget) || {}).targetLabel || keepTarget;
  const drop = [];
  batchable.forEach((c) => {
    if (!c.candidates.some((x) => x.target === keepTarget)) return; // 该冲突不含保留目录，跳过
    c.candidates.filter((x) => x.target !== keepTarget).forEach((d) => drop.push({ ...d, linkName: c.linkName }));
  });
  if (!drop.length) { toast("没有可批量停用的项"); return; }
  const follows = drop.filter((d) => d.follow);
  const followNote = follows.length ? "\n其中 " + follows.length + " 项来自整仓自动同步，停用会移除该源在对应目录的全部 skill。" : "";
  if (!(await confirmModal(
    "将保留「" + keepLabel + "」中的同名 skill，停用其它文件夹里的 " + drop.length + " 项。" + followNote + "\n继续？",
    "批量停用", true))) return;
  let ok = 0;
  try {
    for (const d of drop) { await api("DELETE", "/api/enabled", { skill: d.selector, target: d.target }); ok++; }
    await api("POST", "/api/apply");
  } catch (e) { banner("批量处理失败（已停用 " + ok + " 项）：" + e.message, true); }
  toast("已批量保留「" + keepLabel + "」，停用 " + ok + " 项");
  await load();
  try { renderHealthBody(await api("GET", "/api/conflicts")); }
  catch { closeHealthModal(); }
}

// --- 冲突 (conflict): 库内跨源「重复 / 重叠」检测 ---
// 针对全部数据源（git 源 + 本地源 + npx skills.sh）汇总后的 skill 库，纯前端启发式
// 识别两类问题：① 名称重复（同名 skill 来自 ≥2 个不同源）；② 关键词重叠（名称+描述
// 分词后 Jaccard ≥ 0.5 的不同名 skill 对，可能功能/提示词重叠）。这是「库里有没有冗余」
// 的体检，和「健康度」（链接层撞名/遮蔽）不同——这里只识别+指引，不自动改库（只读镜像）。
const OVERLAP_THRESHOLD = 0.5;
const STOPWORDS = new Set([
  "the", "a", "an", "and", "or", "of", "to", "for", "in", "on", "with", "skill",
  "this", "that", "use", "used", "using", "when", "你", "的", "了", "和", "与",
]);
function tokenSet(s) {
  const out = new Set();
  for (const t of (s || "").toLowerCase().split(/[^a-z0-9一-鿿]+/)) {
    if (t && t.length > 1 && !STOPWORDS.has(t)) out.add(t);
  }
  return out;
}
function jaccard(a, b) {
  if (!a.size || !b.size) return 0;
  let inter = 0;
  for (const t of a) if (b.has(t)) inter++;
  return inter / (a.size + b.size - inter);
}
// computeLibraryConflicts gathers every skill across all sources (same shape as
// the global search) and returns { dups, overlaps } for the top「冲突」stat + modal.
function computeLibraryConflicts() {
  const all = [];
  for (const [ns, skills] of Object.entries(state.skillsByRepo || {})) {
    (skills || []).forEach((sk) => all.push({ ns, name: sk.linkName || sk.logicalName, description: sk.description }));
  }
  (state.skillsShSkills || []).forEach((sk) => all.push({ ns: AGENTS_NS, name: sk.linkName || sk.logicalName, description: sk.description }));
  // ① 名称重复：同一 lowercase 名称、来自 ≥2 个不同源。
  const byName = new Map();
  for (const r of all) {
    const k = (r.name || "").toLowerCase();
    if (!k) continue;
    (byName.get(k) || byName.set(k, []).get(k)).push(r);
  }
  const dups = [];
  const dupNames = new Set();
  for (const [k, items] of byName) {
    const sources = new Set(items.map((i) => i.ns));
    if (items.length > 1 && sources.size > 1) { dups.push({ name: items[0].name, items }); dupNames.add(k); }
  }
  // ② 关键词重叠：不同名 skill 对，Jaccard(名称+描述分词) ≥ 阈值。
  const toks = all.map((r) => tokenSet((r.name || "") + " " + (r.description || "")));
  const overlaps = [];
  for (let i = 0; i < all.length; i++) {
    for (let j = i + 1; j < all.length; j++) {
      if ((all[i].name || "").toLowerCase() === (all[j].name || "").toLowerCase()) continue; // 同名归入 dup
      const score = jaccard(toks[i], toks[j]);
      if (score >= OVERLAP_THRESHOLD) overlaps.push({ a: all[i], b: all[j], score });
    }
  }
  overlaps.sort((x, y) => y.score - x.score);
  return { dups, overlaps };
}
function openConflictModal() {
  const body = $("#conflict-body"); body.innerHTML = "";
  $("#conflict-modal").classList.remove("hidden");
  const { dups, overlaps } = computeLibraryConflicts();
  if (!dups.length && !overlaps.length) {
    body.append(ce("div", { className: "empty", textContent: "库里没有发现跨源重复或明显重叠 🎉" }));
    return;
  }
  const srcChip = (ns) => { const m = sourceMeta(ns); return ce("span", { className: "src-badge " + m.cls, textContent: m.label }); };
  if (dups.length) {
    body.append(ce("div", { className: "cf-sec-head", textContent: "名称重复（" + dups.length + "）" }));
    body.append(ce("div", { className: "muted", style: "font-size:12px;margin:-4px 0 8px", textContent: "同名 skill 来自多个源，启用时只能保留一份。" }));
    dups.forEach((d) => {
      const sec = ce("div", { className: "cf-item" });
      const dh = ce("div", { className: "cf-head" });
      dh.append(ce("span", { className: "cf-name", textContent: d.name }));
      sec.append(dh);
      d.items.forEach((it) => {
        const row = ce("div", { className: "cf-cand clickable" });
        row.onclick = () => { closeConflictModal(); openDetail(it.ns, it.name); };
        const info = ce("div", { className: "cf-cand-info" });
        info.append(srcChip(it.ns));
        if (it.description) info.append(ce("span", { className: "muted", style: "font-size:12px", textContent: it.description.slice(0, 60) }));
        row.append(info);
        row.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "查看 ›" }));
        sec.append(row);
      });
      body.append(sec);
    });
  }
  if (overlaps.length) {
    body.append(ce("div", { className: "cf-sec-head", textContent: "可能重叠（" + overlaps.length + "）" }));
    body.append(ce("div", { className: "muted", style: "font-size:12px;margin:-4px 0 8px", textContent: "名称/描述关键词高度重合，可能功能或提示词重复，建议人工确认。" }));
    overlaps.forEach((o) => {
      const sec = ce("div", { className: "cf-item" });
      const oh = ce("div", { className: "cf-head" });
      oh.append(ce("span", { className: "cf-name", textContent: o.a.name + "  ⇄  " + o.b.name }));
      oh.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "重合 " + Math.round(o.score * 100) + "%" }));
      sec.append(oh);
      [o.a, o.b].forEach((it) => {
        const row = ce("div", { className: "cf-cand clickable" });
        row.onclick = () => { closeConflictModal(); openDetail(it.ns, it.name); };
        const info = ce("div", { className: "cf-cand-info" });
        info.append(srcChip(it.ns));
        info.append(ce("span", { className: "muted", style: "font-size:12px", textContent: it.name }));
        row.append(info);
        row.append(ce("span", { className: "muted", style: "font-size:12px", textContent: "查看 ›" }));
        sec.append(row);
      });
      body.append(sec);
    });
  }
}
function closeConflictModal() { $("#conflict-modal").classList.add("hidden"); }

async function addRepoAndSync(url, branch) {
  try {
    await api("POST", "/api/repos", { url, branch });
    await updateNow(false);
    const host = httpsHost(url);
    if (host) {
      const repo = (state.status.repos || []).find((r) => r.url === url);
      if (repo && repo.authHint) {
        banner("该仓库需要有效凭据，请填写。", true);
        openCredModal(host, state.credHosts[host] || "");
      }
    }
  } catch (err) { banner("添加失败：" + err.message, true); }
}
// chooseHarnessModal: 添加的文件夹既不是 Claude Code、Codex 也不是 pi 的 skill 目录时，
// 让用户选在它下面创建哪种。返回 ["cc"] / ["codex"] / ["pi"] 的任意组合，或 null(取消)。
function chooseHarnessModal(dir) {
  return new Promise((resolve) => {
    const m = ce("div", { className: "modal" });
    const card = ce("div", { className: "modal-card", style: "width:min(480px,100%)" });
    card.append(ce("div", { className: "modal-head", innerHTML: "<h3>创建 skill 目录</h3>" }));
    const body = ce("div", { className: "help-body" });
    body.append(ce("p", { style: "white-space:pre-wrap;color:var(--text)", textContent: "「" + dir + "」\n不是 Claude Code 或 Codex 的 skill 目录。要在它下面创建哪种？" }));
    body.append(ce("p", { className: "muted", style: "font-size:12px", textContent: "Claude Code → .claude/skills；Codex → .agents/skills（项目级 Codex 读取处）；pi → .pi/skills。" }));
    // 横向多选：Claude Code / Codex / pi（默认都勾）。
    const opts = ce("div", { className: "row", style: "gap:24px;margin-top:8px" });
    const mk = (label) => {
      const cb = ce("input", { type: "checkbox", checked: true });
      const l = ce("label", { style: "display:flex;align-items:center;gap:6px;cursor:pointer;color:var(--text)" });
      l.append(cb, ce("span", { textContent: label }));
      return { l, cb };
    };
    const cc = mk("Claude Code"), cx = mk("Codex"), pi = mk("pi");
    opts.append(cc.l, cx.l, pi.l);
    body.append(opts);
    card.append(body);
    const row = ce("div", { className: "row", style: "justify-content:flex-end;gap:8px;padding:0 18px 18px" });
    const done = (v) => { m.remove(); resolve(v); };
    const cancel = ce("button", { className: "ghost", textContent: "取消" });
    cancel.onclick = () => done(null);
    const ok = ce("button", { textContent: "确认" });
    ok.onclick = () => {
      const kinds = [];
      if (cc.cb.checked) kinds.push("cc");
      if (cx.cb.checked) kinds.push("codex");
      if (pi.cb.checked) kinds.push("pi");
      if (!kinds.length) { toast("请至少选择一项", "info"); return; }
      done(kinds);
    };
    row.append(cancel, ok);
    card.append(row);
    m.append(card);
    m.onclick = (e) => { if (e.target === m) done(null); };
    document.body.append(m);
  });
}

async function addTarget(dir, alias, create) {
  try {
    const res = await api("POST", "/api/targets", { dir, alias, create });
    if (res && res.needsChoice) {
      const chosen = await chooseHarnessModal(res.dir || dir);
      if (!chosen) return; // 用户取消，不创建、不添加
      await addTarget(dir, alias, chosen);
      return;
    }
    if (res && res.added && res.added.length) state.activeTarget = res.added[0];
    await load();
  } catch (err) { banner("添加同步目录失败：" + err.message, true); }
}
$("#add-target").onsubmit = async (e) => {
  e.preventDefault();
  const dir = $("#target-path").value.trim();
  if (!dir) return;
  const alias = $("#target-alias").value.trim();
  closeTargetModal();
  await addTarget(dir, alias);
};
$("#target-path").onkeydown = (e) => {
  if (e.key === "Enter") { e.preventDefault(); browseTo($("#target-path").value.trim()); }
};
$("#target-path").onpaste = () => { setTimeout(() => browseTo($("#target-path").value.trim()), 0); };
$("#do-export").onsubmit = async (e) => {
  e.preventDefault();
  const dir = $("#export-path").value.trim();
  closeExportModal();
  await doExportRepos(dir);
};
$("#export-path").onkeydown = (e) => {
  if (e.key === "Enter") { e.preventDefault(); browseTo($("#export-path").value.trim(), "#export-browser", "#export-path"); }
};
$("#export-path").onpaste = () => { setTimeout(() => browseTo($("#export-path").value.trim(), "#export-browser", "#export-path"), 0); };
$("#export-modal-close").onclick = closeExportModal;
$("#export-modal-cancel").onclick = closeExportModal;
$("#export-modal").onclick = (e) => { if (e.target.id === "export-modal") closeExportModal(); };
$("#add-repo").onsubmit = async (e) => {
  e.preventDefault();
  const url = $("#repo-url").value.trim(), branch = $("#repo-branch").value.trim();
  if (!url) return;
  closeGitRepoModal();
  await submitGitRepo(url, branch);
};
$("#repo-url").oninput = updateRepoCredSection;
$("#git-repo-modal-close").onclick = closeGitRepoModal;
$("#git-repo-modal-cancel").onclick = closeGitRepoModal;
$("#git-repo-modal").onclick = (e) => { if (e.target.id === "git-repo-modal") closeGitRepoModal(); };
$("#health-modal-close").onclick = closeHealthModal;
$("#health-modal").onclick = (e) => { if (e.target.id === "health-modal") closeHealthModal(); };
$("#conflict-modal-close").onclick = closeConflictModal;
$("#conflict-modal").onclick = (e) => { if (e.target.id === "conflict-modal") closeConflictModal(); };
$("#cred-form").onsubmit = async (e) => {
  e.preventDefault();
  const host = $("#cred-host").dataset.host;
  const username = $("#cred-user").value.trim();
  const token = $("#cred-token").value;
  if (!host || !token) { banner("密码 / 令牌不能为空（公开仓可点「跳过」）", true); return; }
  const pending = state.credPending;
  try { await api("POST", "/api/credentials", { host, username, token }); }
  catch (err) { banner("保存凭据失败：" + err.message, true); return; }
  closeCredModal();
  if (pending) { await addRepoAndSync(pending.url, pending.branch); }
  else { banner("凭据已保存，正在重试更新…"); await api("POST", "/api/update-now", {}); await load(); }
};
$("#cred-skip").onclick = async () => {
  const pending = state.credPending;
  closeCredModal();
  if (pending) await addRepoAndSync(pending.url, pending.branch);
};
$("#cred-close").onclick = closeCredModal;
$("#cred-cancel").onclick = closeCredModal;
$("#cred-modal").onclick = (e) => { if (e.target.id === "cred-modal") closeCredModal(); };
$("#info-close").onclick = () => $("#info-modal").classList.add("hidden");
$("#info-modal").onclick = (e) => { if (e.target.id === "info-modal") $("#info-modal").classList.add("hidden"); };
$("#repo-skills-close").onclick = () => $("#repo-skills-modal").classList.add("hidden");
$("#repo-skills-modal").onclick = (e) => { if (e.target.id === "repo-skills-modal") $("#repo-skills-modal").classList.add("hidden"); };
$("#upload-close").onclick = () => $("#upload-modal").classList.add("hidden");
$("#upload-cancel").onclick = () => $("#upload-modal").classList.add("hidden");
$("#upload-modal").onclick = (e) => { if (e.target.id === "upload-modal") $("#upload-modal").classList.add("hidden"); };
$("#upload-ok").onclick = (e) => doUpdateAndUpload(e.currentTarget);
$("#upload-update-only").onclick = doUpdateOnly;
$("#upload-msg").oninput = () => { $("#upload-msg").dataset.touched = "1"; };
$("#help-btn").onclick = () => $("#help-modal").classList.remove("hidden");

// 同步本地：重扫磁盘实际状态（后端各 /api 都是实时 ScanInventory），刷新工具显示。
// 用于用户在工具外直接增删本地文件后，让显示与磁盘一致。
$("#resync-btn").onclick = async (e) => {
  const b = e.currentTarget, old = b.textContent;
  b.disabled = true; b.textContent = "同步中…";
  try { await load(); toast("已同步本地状态"); }
  finally { b.disabled = false; b.textContent = old; }
};
$("#help-close").onclick = () => $("#help-modal").classList.add("hidden");
$("#help-modal").onclick = (e) => { if (e.target.id === "help-modal") $("#help-modal").classList.add("hidden"); };
$("#tab-add").onclick = openTargetModal;
$("#target-modal-close").onclick = closeTargetModal;
$("#target-modal-cancel").onclick = closeTargetModal;
$("#add-localsrc").onsubmit = async (e) => {
  e.preventDefault();
  const dir = $("#localsrc-path").value.trim();
  if (!dir) return;
  const label = $("#localsrc-alias").value.trim();
  closeLocalSrcModal();
  await addLocalSource(dir, label);
};
$("#localsrc-path").onkeydown = (e) => {
  if (e.key === "Enter") { e.preventDefault(); browseTo($("#localsrc-path").value.trim(), "#localsrc-browser", "#localsrc-path"); }
};
$("#localsrc-path").onpaste = () => { setTimeout(() => browseTo($("#localsrc-path").value.trim(), "#localsrc-browser", "#localsrc-path"), 0); };
$("#localsrc-modal-close").onclick = closeLocalSrcModal;
$("#localsrc-modal-cancel").onclick = closeLocalSrcModal;
$("#localsrc-modal").onclick = (e) => { if (e.target.id === "localsrc-modal") closeLocalSrcModal(); };
$("#target-modal").onclick = (e) => { if (e.target.id === "target-modal") closeTargetModal(); };
// checkUpdates contacts each repo's remote (ls-remote, no pull) to populate the
// 「有更新」badges. There is no manual button anymore: it runs once on page load
// and then automatically every hour while the app is open (see the interval at
// the bottom). Always silent — passive, failures swallowed, a toast only when
// updates are found.
async function checkUpdates() {
  if (!state.status || (state.status.repos || []).length === 0) return;
  try {
    const r = await api("POST", "/api/check-updates");
    if (r && r.error) return;
    await load();
    const n = (r && r.updates) || 0;
    if (n > 0) toast(n + " 个仓有更新，点「全量更新」拉取");
  } catch (e) { /* passive check — swallow */ }
}
// 导出 / 导入仅针对 git 仓库列表（用于在另一台机器重建来源）——属于 git 源的能力，
// 故渲染在「git 仓」分区里（见 renderRepos），不挂在顶层「源」标题上。
// exportRepos opens a folder-picker (same in-app directory browser as「添加同步目录」)
// so the user chooses where the export lands. The backend writes the file and
// reveals it in the file manager — we do NOT build a Blob + <a download>, which
// the desktop app's WKWebView silently ignores.
function exportRepos() {
  $("#export-path").value = "";
  $("#export-modal").classList.remove("hidden");
  $("#export-path").focus();
  browseTo("~/Downloads", "#export-browser", "#export-path");
}
function closeExportModal() { $("#export-modal").classList.add("hidden"); }
async function doExportRepos(dir) {
  try {
    const q = dir ? "?dir=" + encodeURIComponent(dir) : "";
    const res = await api("GET", "/api/repos/export" + q);
    banner("已导出 " + res.count + " 个 git 源到 " + res.path + "（已在文件管理器中显示）");
  } catch (err) {
    banner("导出失败：" + err.message, true);
  }
}
function importRepos() {
  const inp = ce("input", { type: "file", accept: ".json" });
  inp.onchange = async () => {
    const txt = await inp.files[0].text();
    let repos;
    try { repos = JSON.parse(txt); } catch { banner("导入文件不是合法 JSON", true); return; }
    try {
      const res = await api("POST", "/api/repos/import", { repos });
      banner("导入：新增 " + res.added + "，跳过 " + res.skipped);
      await load();
    } catch (err) { banner("导入被拒（含非法 URL，整体拒绝）：" + err.message, true); }
  };
  inp.click();
}
$("#modal-close").onclick = () => $("#modal").classList.add("hidden");
$("#modal").onclick = (e) => { if (e.target.id === "modal") $("#modal").classList.add("hidden"); };

// 首次使用、以及每次版本更新后，自动弹出使用指南一次（按版本号记忆：换了新版就
// 再弹一次，让用户看到本版改了什么；同版本内只弹一次）。版本号取自更新日志头部的
// .cl-ver（页面里的单一来源，随发布自然更新）。
(function maybeShowGuide() {
  try {
    const ver = ($(".cl-ver") && $(".cl-ver").textContent.trim()) || "";
    if (localStorage.getItem("sm_guide_ver") !== ver) {
      $("#help-modal").classList.remove("hidden");
      localStorage.setItem("sm_guide_ver", ver);
    }
  } catch { /* localStorage 不可用时静默跳过 */ }
})();

// 左栏分段（来源 / 分组）+ 来源分区折叠状态恢复。
loadCollapsed();
$("#seg-sources").onclick = () => setAsideTab("sources");
$("#seg-presets").onclick = () => setAsideTab("presets");

// On page entry: reconcile-then-render so the footer is current, then auto-check
// updates once (ls-remote only, no pull). While the app stays open, re-check
// every hour so the「有更新」badges stay current without a manual button.
load(true).then(() => checkUpdates());
setInterval(() => checkUpdates(), 60 * 60 * 1000);

// App self-update check: ask the configured feed whether a newer
// SkillManager release exists and, if so, show a sticky notice. Disabled builds
// (no feed) return {enabled:false} → no-op, no banner, no network. Checked on
// entry + hourly + on window focus.
async function checkAppUpdate() {
  try {
    const d = await api("GET", "/api/update-check");
    const btn = $("#update-btn");
    if (d && d.enabled && d.newer) {
      if (d.canSelfUpdate && btn) {
        // desktop on a supported OS → one-click self-update button in the header
        btn.classList.remove("hidden");
        btn.disabled = false;
        btn.textContent = "↑ 更新到 v" + d.latest;
        btn.title = (d.notes ? d.notes + "　·　" : "") + "下载并安装,完成后自动重启";
        btn.onclick = () => applyUpdate(d.latest, d.notes);
      } else {
        // web build / unsupported → just notify (manual download)
        if (btn) btn.classList.add("hidden");
        banner("SkillManager 有新版本 v" + d.latest + "（当前 v" + d.current + "）" + (d.notes ? "：" + d.notes : "") + "　·　请前往 GitHub Releases 下载更新", false, true);
      }
    } else if (btn) {
      btn.classList.add("hidden");
    }
  } catch { /* update check is best-effort; never disrupt the app */ }
}

// applyUpdate triggers the desktop self-update: confirm → POST /api/update-apply
// (downloads + verifies + spawns the detached updater) → the app exits and the
// updater swaps the bundle and relaunches it.
async function applyUpdate(latest, notes) {
  if (!(await confirmModal("更新到 v" + latest + "？" + (notes ? "\n\n" + notes : "") + "\n\n会下载新版本并自动重启应用。", "更新", false))) return;
  const btn = $("#update-btn");
  if (btn) { btn.disabled = true; btn.textContent = "更新中…"; }
  banner("正在下载并安装 v" + latest + "，完成后会自动重启…", false, true);
  try {
    const d = await api("POST", "/api/update-apply");
    if (d && d.restarting) banner("更新就绪，应用正在重启…", false, true);
    else if (d && d.upToDate) { banner("已是最新版本", false); if (btn) btn.classList.add("hidden"); }
  } catch (e) {
    banner("更新失败：" + e.message, true);
    if (btn) { btn.disabled = false; btn.textContent = "↑ 更新到 v" + latest; }
  }
}
checkAppUpdate();
setInterval(checkAppUpdate, 60 * 60 * 1000);
window.addEventListener("focus", checkAppUpdate);

// 自动同步本地：用户可能在工具外直接增删 / 改本地 skill 文件，导致工具显示与磁盘
// 不一致。每 15s 静默探测一次磁盘指纹，仅在确有变化时整体刷新一次（无变化绝不重绘，
// 因此空闲时完全无感——不闪烁、不跳滚动条）。只在标签页可见且没有打开任何弹窗时运行，
// 避免打断用户操作 / 输入。切回标签页或窗口重新获得焦点时立即探测一次，秒级反映改动。
setInterval(autoSyncTick, 15 * 1000);
document.addEventListener("visibilitychange", () => { if (!document.hidden) autoSyncTick(); });
window.addEventListener("focus", autoSyncTick);
