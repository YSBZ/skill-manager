"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");
const helperPath = path.join(__dirname, "skill-check.js");
function element(tag = "div") {
  let text = "";
  const classes = new Set();
  return { tagName: tag.toUpperCase(), children: [], attributes: {}, className: "", disabled: false,
    classList: { add: name => classes.add(name), remove: name => classes.delete(name), contains: name => classes.has(name) },
    focus() { this.focused = true; },
    append(...items) { this.children.push(...items); },
    replaceChildren(...items) { text = ""; this.children = items; },
    setAttribute(k, v) { this.attributes[k] = String(v); },
    addEventListener(k, f) { this[k] = f; },
    get textContent() { return text + this.children.map(x => x.textContent).join(""); },
    set textContent(v) { text = String(v); this.children = []; },
    click() { if (!this.disabled && this.onclick) return this.onclick(); },
  };
}
function setup() {
  const window = {};
  if (fs.existsSync(helperPath)) vm.runInNewContext(fs.readFileSync(helperPath, "utf8"), { window, URLSearchParams });
  assert.ok(window.SkillManagerSkillCheck, "Skill local check UI helper must be implemented");
  return { ui: window.SkillManagerSkillCheck, document: { createElement: element }, host: element() };
}
function all(node) { return [node, ...node.children.flatMap(all)]; }
function button(host, text) { return all(host).find(x => x.tagName === "BUTTON" && x.textContent === text); }
const checked = { state: "checked", path: { entry: "/target/skill", resolved: "/source/skill", state: "ok", kind: "symlink", linkTarget: "../source/skill" }, dependencies: [], findings: [] };
const issue = { ...checked, state: "issues", path: { ...checked.path, state: "broken" }, dependencies: [{ name: "test:grilling", state: "missing", evidence: 'REQUIRED SUB-SKILL: Use test:grilling', source: "/source/SKILL.md" }], findings: [{ code: "missing_dependency", level: "error", message: "缺少依赖", evidence: "required test:grilling", dependency: "test:grilling" }], compatibility: "Requires a POSIX shell", allowedTools: ["Bash(git:*)"] };
test("badges count actionable findings and state that local checks are not execution", () => {
  const { ui, document } = setup();
  const badge = ui.badge(document, checked);
  assert.equal(badge.textContent, "本地检查通过");
  assert.match(badge.title, /未运行验证/);
  assert.equal(ui.badge(document, issue).textContent, "待处理 1");
  assert.equal(ui.badge(document, { state: "review", findings: [{ level: "warning" }] }).textContent, "需确认 1");
  assert.equal(ui.badge(document, null).textContent, "尚未检查");
});
test("detail renders exact paths, dependency evidence, environment declarations and explicit search action safely", () => {
  const { ui, document, host } = setup();
  const searches = [];
  ui.render(document, host, { report: issue, onSearch: name => searches.push(name), onRefresh() {} });
  for (const text of ["/target/skill", "../source/skill", "/source/skill", "test:grilling", "REQUIRED SUB-SKILL", "Requires a POSIX shell", "Bash(git:*)", "非必需依赖", "未运行验证", "恢复来源", "移除链接"]) assert.ok(host.textContent.includes(text), text);
  assert.equal(searches.length, 0);
  button(host, "查找依赖").click();
  assert.deepEqual(searches, ["test:grilling"]);
  assert.ok(!all(host).some(x => x.tagName === "SCRIPT"));
});
test("loading, error and absent reports replace previous details and offer retry", () => {
  const { ui, document, host } = setup();
  ui.render(document, host, { report: issue });
  ui.render(document, host, { loading: true });
  assert.match(host.textContent, /检查中/);
  assert.ok(!host.textContent.includes("test:grilling"));
  ui.render(document, host, { error: "permission denied", onRefresh() {} });
  assert.match(host.textContent, /检查失败.*permission denied/);
  assert.ok(button(host, "重新检查"));
  ui.render(document, host, {});
  assert.match(host.textContent, /暂无检查数据/);
});
test("switching detail and rechecking ignore stale responses and refresh inventory without mutation", async () => {
  const { ui, document, host } = setup();
  const pending = [], requests = [], searches = [];
  let refreshed = 0;
  const controller = ui.createController(document, host, {
    request: query => { requests.push(query); return new Promise((resolve, reject) => pending.push({ resolve, reject })); },
    onRefresh: async () => { refreshed++; }, onSearch: name => searches.push(name),
  });
  const first = controller.open({ target: "/target", name: "first" });
  const second = controller.open({ target: "/target", name: "second" });
  pending[1].resolve(checked); await second;
  pending[0].resolve(issue); await first;
  assert.ok(!host.textContent.includes("test:grilling"));
  assert.match(requests[1], /target=%2Ftarget/);
  const again = button(host, "重新检查").click();
  await new Promise(setImmediate);
  assert.equal(refreshed, 1);
  pending[2].resolve(issue); await again;
  button(host, "查找依赖").click();
  assert.deepEqual(searches, ["test:grilling"]);
  assert.equal(requests.length, 3);
});
test("request scopes reject earlier content and authorship responses, fingerprints include evidence", () => {
  const { ui } = setup();
  const scope = ui.createRequestScope();
  const a = scope.begin(), b = scope.begin();
  assert.equal(scope.isCurrent(a), false);
  assert.equal(scope.isCurrent(b), true);
  scope.cancel(); assert.equal(scope.isCurrent(b), false);
  assert.notEqual(ui.fingerprint(issue), ui.fingerprint({ ...issue, findings: [{ ...issue.findings[0], evidence: "changed" }] }));
  assert.equal(ui.fingerprint({ ...checked, dependencies: null }), ui.fingerprint(checked));
});
test("helper is loaded before app and managed inventory checks the target entry", () => {
  const { ui } = setup();
  const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
  assert.ok(html.indexOf('src="/skill-check.js"') < html.indexOf('src="/app.js"'));
  const app = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  assert.match(app, /const p = i\.kind === "plugin" \? openPluginDetail[\s\S]*?: openDetailAt\(i\.name\)/);
  assert.ok(ui.fingerprint(checked));
});

test("a source without a configured target does not claim dependency availability", async () => {
  const { ui, document, host } = setup();
  let requested = false;
  const controller = ui.createController(document, host, { request() { requested = true; return checked; } });
  await controller.open({ repo: "source", name: "skill", target: "" });
  assert.equal(requested, false);
  assert.match(host.textContent, /选择同步目录后可检查依赖与客户端/);
});

test("real detail entry points ignore stale content, errors and authorship across source, plugin and target", async () => {
  const { ui, document } = setup();
  const nodes = {};
  const $ = id => nodes[id] ||= element();
  Object.values(nodes).forEach(n => n.classList = { add() {}, remove() {} });
  const get = id => { const n = $(id); n.classList ||= { add() {}, remove() {} }; return n; };
  const pending = [];
  const context = { window: { SkillManagerSkillCheck: ui }, document, $: get, URLSearchParams,
    state: { status: { repos: [{ name: "repo" }] } }, AGENTS_NS: "@agents", exportCtx: null,
    currentTarget: () => "/target", fetchInventory() {}, resetAuthorship() {}, setDetailSource() {},
    clearModalActions() {}, setSkillsShActions() {}, setPluginActions() {},
    api(method, url) { if (url.startsWith("/api/skill-check?")) return Promise.resolve(checked); return new Promise((resolve, reject) => pending.push({ url, resolve, reject })); },
  };
  const app = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const shared = app.slice(app.indexOf("const detailScope ="), app.indexOf("// exportCtx identifies"));
  const entries = app.slice(app.indexOf("async function loadAuthorship("), app.indexOf("// updateNow is"));
  vm.runInNewContext(shared + entries + "\nglobalThis.entries = {openDetail,openPluginDetail,openDetailAt};", context);
  const source = context.entries.openDetail("repo", "source");
  pending[0].resolve({ content: "source body" }); await source;
  assert.ok(pending[1].url.includes("skill-authorship"));
  const plugin = context.entries.openPluginDetail("plugin", "plug", "codex");
  assert.equal(new URL(pending[2].url, "http://localhost").searchParams.get("target"), "/target", "plugin text must use the same target scope as its dependency check");
  const target = context.entries.openDetailAt("broken");
  assert.equal(get("#modal-content").textContent, "加载中…");
  pending[3].resolve({ content: "" }); await target;
  pending[2].reject(new Error("stale failure")); await plugin;
  pending[1].resolve({ authorship: { creator: "stale author" } });
  await new Promise(setImmediate);
  assert.match(get("#modal-content").textContent, /查看上方本地检查/);
  assert.ok(!get("#modal-authorship").textContent.includes("stale author"));
  assert.equal(get("#modal-title").textContent, "broken");
});

test("full namespace search finds local/plugin dependencies without conflating other namespaces", () => {
  const { document } = setup();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const renderFn = source.slice(source.indexOf("function renderSearchResults("), source.indexOf("// fmtInstalls"));
  const opened = [];
  const ctx = { document, state: { inventory: [], searchFold: {}, skillsByRepo: { trusted: [{ linkName: "grilling" }], other: [{ linkName: "grilling" }] }, pluginSkills: [{ name: "planning", plugin: "superpowers", harness: "codex" }], skillsShSkills: [] },
    ce: (tag, props = {}) => Object.assign(element(tag), props), currentTarget: () => "/target", targetLabel: x => x,
    enabledFollow: () => false, sourceMeta: ns => ({ cls: "", label: ns }), cleanSkillName: x => x, verTag() {},
    enableControl: () => element("button"), renderInventory() {}, renderOnlineSection() {},
    openDetail: (ns, name) => opened.push(ns + ":" + name), openPluginDetail: (ns, name) => opened.push(ns + ":" + name),
    AGENTS_NS: "@agents" };
  vm.runInNewContext(renderFn, ctx);
  const root = element(); ctx.renderSearchResults(root, "trusted:grilling");
  assert.match(root.textContent, /1 skill/);
  assert.ok(!root.textContent.includes("other"));
  const plugins = element(); ctx.renderSearchResults(plugins, "superpowers:planning");
  assert.match(plugins.textContent, /1 skill/);
  const card = all(plugins).find(x => x.title === "查看详情");
  assert.ok(card); card.onclick({ target: { closest: () => false } });
  assert.deepEqual(opened, ["superpowers:planning"]);
});

test("rechecking refreshes the visible skill text without applying changes", async () => {
  const { ui, document } = setup();
  const app = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const nodes = {};
  const $ = id => { const node = nodes[id] ||= element(); node.classList ||= { add() {}, remove() {} }; return node; };
  const calls = [];
  const ctx = { window: { SkillManagerSkillCheck: ui }, document, $, URLSearchParams, state: { status: { repos: [] } }, AGENTS_NS: "@agents", exportCtx: null,
    currentTarget: () => "/target", fetchInventory: async () => calls.push("inventory"),
    resetAuthorship() {}, setDetailSource() {}, clearModalActions() {}, setSkillsShActions() {}, setPluginActions() {},
    async api(method, url) { calls.push(method + " " + url); if (url.startsWith("/api/skill-check")) return checked; return { content: "version " + calls.filter(x => x.includes("/api/skill-at")).length }; },
  };
  const shared = app.slice(app.indexOf("const detailScope ="), app.indexOf("// exportCtx identifies"));
  const entries = app.slice(app.indexOf("async function loadAuthorship("), app.indexOf("// updateNow is"));
  vm.runInNewContext(shared + entries + "\nglobalThis.entries = {openDetailAt};", ctx);
  await ctx.entries.openDetailAt("skill");
  assert.equal($("#modal-content").textContent, "version 1");
  await new Promise(setImmediate);
  await button($("#modal-check"), "重新检查").click();
  assert.equal($("#modal-content").textContent, "version 2");
  assert.ok(calls.includes("inventory"));
  assert.ok(!calls.some(x => x.startsWith("POST")));
});

test("the actual manager signature detects changed check evidence for inventory and plugins", () => {
  const { ui } = setup();
  const app = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const ctx = { window: { SkillManagerSkillCheck: ui } };
  vm.runInNewContext(app.slice(app.indexOf("function computeSig("), app.indexOf("// fetchSnapshot")), ctx);
  const changed = { ...issue, findings: [{ ...issue.findings[0], evidence: "new evidence" }] };
  for (const list of ["inventory", "pluginSkills"]) {
    assert.notEqual(ctx.computeSig({ [list]: [{ name: "skill", check: issue }] }), ctx.computeSig({ [list]: [{ name: "skill", check: changed }] }), list + " report changes must refresh UI");
  }
});

test("finding a dependency closes both detail and source modals and enters local search without an online request", async () => {
  const { ui, document } = setup();
  const app = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const nodes = {};
  const $ = id => nodes[id] ||= element();
  const requests = [];
  let rendered = 0;
  const ctx = { window: { SkillManagerSkillCheck: ui, SkillManagerContent: { showMarketSearch() {} } }, document, $, URLSearchParams,
    state: { status: { repos: [] } }, AGENTS_NS: "@agents", exportCtx: null, currentTarget: () => "/target",
    fetchInventory() {}, resetAuthorship() {}, setDetailSource() {}, clearModalActions() {}, setSkillsShActions() {}, setPluginActions() {},
    renderInventory() { rendered++; },
    async api(method, url) { requests.push(url); return url.startsWith("/api/skill-check") ? issue : { content: "body" }; },
  };
  const shared = app.slice(app.indexOf("const detailScope ="), app.indexOf("// exportCtx identifies"));
  vm.runInNewContext(shared + "\nglobalThis.open = openDetail;", ctx);
  await ctx.open("repo", "skill"); await new Promise(setImmediate);
  button($("#modal-check"), "查找依赖").click();
  assert.equal($("#modal").classList.contains("hidden"), true);
  assert.equal($("#repo-skills-modal").classList.contains("hidden"), true);
  assert.equal(ctx.state.search, "test:grilling");
  assert.equal($("#search").value, "test:grilling");
  assert.ok(rendered > 0);
  assert.ok(!requests.some(x => x.includes("/find?")));
});

test("a SkillHub-only dependency can be found locally and enabled using its actual @skillhub selector", async () => {
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const renderFn = source.slice(source.indexOf("function renderSearchResults("), source.indexOf("// fmtInstalls"));
  const enableFn = source.slice(source.indexOf("function enableControl("), source.indexOf("// driftLabel"));
  const calls = [], opened = [];
  const ctx = {
    state: { inventory: [], searchFold: {}, skillsByRepo: {}, skillsShSkills: [], pluginSkills: [],
      skillHubSkills: [{ linkName: "grilling", logicalName: "display-title", description: "SkillHub dependency" }] },
    ce: (tag, props = {}) => Object.assign(element(tag), props), currentTarget: () => "/current/skills", targetLabel: x => x,
    enabledFollow: () => false, sourceMeta: ns => ({ cls: "", label: ns }), cleanSkillName: x => x, verTag() {},
    renderInventory() {}, renderOnlineSection() {}, confirmShadowEnable: async () => true,
    async api(method, url, body) { calls.push({ method, url, body }); return {}; }, summaryToast() {}, async load() {},
    openDetail: (ns, name) => opened.push(ns + "/" + name), AGENTS_NS: "@agents", SKILLHUB_NS: "@skillhub",
  };
  vm.runInNewContext(enableFn + renderFn, ctx);
  const root = element(); ctx.renderSearchResults(root, "grilling");
  assert.match(root.textContent, /1 skill/);
  assert.match(root.textContent, /SkillHub dependency/);
  assert.equal(calls.length, 0, "finding a dependency is read-only until explicit enable");
  const card = all(root).find(x => x.title === "查看详情");
  card.onclick({ target: { closest: () => false } });
  assert.deepEqual(opened, ["@skillhub/grilling"]);
  await button(root, "启用").click();
  assert.equal(calls[0].method, "POST");
  assert.equal(calls[0].url, "/api/enabled");
  assert.equal(calls[0].body.skill, "@skillhub/grilling");
  assert.equal(calls[0].body.target, "/current/skills");
  assert.equal(calls[0].body.mode, "snapshot");
});
