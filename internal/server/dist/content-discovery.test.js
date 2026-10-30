"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");
const vm = require("node:vm");

const root = __dirname;
const lifecycleSource = fs.readFileSync(path.join(root, "content-workspace.js"), "utf8");
const contentUISource = fs.existsSync(path.join(root, "content-ui.js")) ? fs.readFileSync(path.join(root, "content-ui.js"), "utf8") : "";
const newsSource = fs.readFileSync(path.join(root, "content-news.js"), "utf8");
const discoveryPath = path.join(root, "content-discovery.js");
const discoverySource = fs.existsSync(discoveryPath) ? fs.readFileSync(discoveryPath, "utf8") : "";
const cssSource = fs.readFileSync(path.join(root, "content-workspace.css"), "utf8");
const appSource = fs.readFileSync(path.join(root, "app.js"), "utf8");
const nodeDependencySource = fs.readFileSync(path.join(root, "node-dependency.js"), "utf8");
const indexSource = fs.readFileSync(path.join(root, "index.html"), "utf8");

function makeElement(id, hidden = false) {
  let ownText = "";
  return {
    id,
    hidden,
    dataset: {},
    attributes: {},
    listeners: {},
    children: [],
    className: "",
    classList: {
      values: new Set(),
      add(name) { this.values.add(name); },
      remove(name) { this.values.delete(name); },
      contains(name) { return this.values.has(name); },
    },
    scrollTop: 0,
    value: "",
    addEventListener(name, handler) { this.listeners[name] = handler; },
    setAttribute(name, value) { this.attributes[name] = String(value); },
    getAttribute(name) { return this.attributes[name] || null; },
    focus() { if (this.ownerDocument) this.ownerDocument.activeElement = this; },
    append(...nodes) { this.children.push(...nodes); nodes.forEach((node) => { node.parent = this; }); },
    replaceChildren(...nodes) { ownText = ""; this.children = []; this.append(...nodes); },
    contains(target) { return this.navButtons ? this.navButtons.includes(target) : this.children.includes(target); },
    closest(selector) {
      if (selector === "[data-skills-view]" && this.dataset.skillsView) return this;
      if (selector === "[data-discovery-view]" && this.dataset.discoveryView) return this;
      return null;
    },
    remove() { if (this.parent) this.parent.children = this.parent.children.filter((child) => child !== this); },
    click() {
      const event = { target: this, currentTarget: this, bubbles: true, stopped: false, preventDefault() {}, stopPropagation() { this.stopped = true; } };
      if (typeof this.onclick === "function") this.onclick(event);
      let current = this;
      while (current) {
        event.currentTarget = current;
        if (current.listeners.click) current.listeners.click(event);
        if (event.stopped) break;
        current = current.parent;
      }
    },
    get textContent() { return ownText + this.children.map((child) => child.textContent || "").join(""); },
    set textContent(value) { ownText = String(value ?? ""); this.children = []; },
  };
}

function makeWorkspace(options = {}) {
  const ids = [
    "workspace-switcher", "workspace-news", "workspace-discovery", "workspace-manage", "news-workspace", "skills-workspace", "discovery-workspace", "manage-workspace",
    "return-article-control", "news-list", "news-cards", "news-detail", "news-update-hint",
    "news-meta-notices", "news-status", "discovery-columns", "discovery-list", "discovery-cards", "discovery-detail", "discovery-update-hint",
    "discovery-meta-notices", "discovery-status", "news-view-nav", "news-window-nav", "news-filter-form",
    "news-search-query", "news-category-filter", "news-window-filter", "news-query-submit", "news-category-options",
    "discovery-rail", "discovery-view-nav", "discovery-search-form", "discovery-search-query",
    "discovery-market-search", "discovery-scene-filter",
  ];
  const initiallyHidden = new Set(["news-workspace", "discovery-workspace", "news-update-hint", "discovery-update-hint", "return-article-control"]);
  const elements = Object.fromEntries(ids.map((id) => [id, makeElement(id, initiallyHidden.has(id))]));
  const skillViews = ["discovery", "manage"].map((value) => {
    const button = elements[value === "discovery" ? "workspace-discovery" : "workspace-manage"];
    button.dataset.skillsView = value;
    return button;
  });
  elements["workspace-switcher"].navButtons = skillViews;
  elements["workspace-switcher"].append(elements["workspace-news"], ...skillViews, elements["return-article-control"]);
  const makeButtons = (parentID, values, key) => {
    const parent = elements[parentID];
    parent.navButtons = values.map((value) => Object.assign(makeElement(value), { dataset: { [key]: value } }));
    parent.append(...parent.navButtons);
    return parent.navButtons;
  };
  const newsViews = makeButtons("news-view-nav", ["selected", "all", "topic", "daily", "weekly", "monthly"], "newsView");
  const newsWindows = makeButtons("news-window-nav", ["24h", "7d"], "newsWindow");
  const discoveryViews = makeButtons("discovery-view-nav", ["weekly", "new", "scenarios"], "discoveryView");
  elements["news-workspace"].append(elements["news-status"]);
  elements["discovery-workspace"].append(elements["discovery-status"]);

  const listeners = { document: {}, window: {} };
  const intervals = [];
  const document = {
    hidden: false,
    activeElement: null,
    addEventListener(name, handler) { listeners.document[name] = handler; },
    getElementById: (id) => elements[id],
    createElement(tag) { return Object.assign(makeElement(tag), { tagName: String(tag).toUpperCase() }); },
    querySelectorAll(selector) {
      if (selector === ".content-status") return [elements["news-status"], elements["discovery-status"]];
      if (selector === "[data-skills-view]") return skillViews;
      if (selector === "[data-news-view]") return newsViews;
      if (selector === "[data-news-window]") return newsWindows;
      if (selector === "[data-discovery-view]") return discoveryViews;
      if (selector === "[data-entry-id]") return [elements["news-cards"], elements["discovery-cards"]].flatMap((parent) => parent.children.filter((child) => child.dataset.entryId));
      return [];
    },
  };
  document.body = Object.assign(makeElement("body"), { ownerDocument: document });
  const createElement = document.createElement;
  document.createElement = (tag) => Object.assign(createElement(tag), { ownerDocument: document });
  const requests = [];
  const externalURLs = [];
  const searchTerms = [];
  const dispatch = async (method, url, body, requestOptions = {}) => {
    const call = { method, url, body, options: requestOptions };
    requests.push(call);
    if (options.request) return options.request(call);
    if (url === "/api/content/status") return options.status || { sources: [], coverage: {}, notices: [] };
    return { status: 200, data: feed([]), etag: "" };
  };
  const window = {
    addEventListener(name, handler) { listeners.window[name] = handler; },
    requestAnimationFrame(callback) { callback(); },
    setInterval(callback, delay) { intervals.push({ callback, delay }); return intervals.length; },
    SkillManagerAPI: {
      request: dispatch,
      openExternal(url) { externalURLs.push(url); },
      async searchSkills(term) { searchTerms.push(term); window.SkillManagerContent.showMarketSearch(); },
    },
  };
  vm.runInNewContext(nodeDependencySource, { window });
  const nodeDependencyUI = window.SkillManagerNodeDependency.createController(document, async (url) => externalURLs.push(url));
  window.SkillManagerNodeDependencyUI = nodeDependencyUI;
  nodeDependencyUI.setStatus(options.nodeStatus || { npxAvailable: true, nodeDependency: { state: "ready", version: "22.20.0", requiredVersion: "22.20.0" } });
  const localStorage = { getItem() { return null; }, setItem() {} };
  class FakeDate extends Date { static now() { return options.now ? options.now() : 1_000_000; } }
  const context = { document, window, localStorage, Date: FakeDate, URL, URLSearchParams, encodeURIComponent };
  vm.runInNewContext(lifecycleSource, context);
  vm.runInNewContext(contentUISource, context);
  vm.runInNewContext(newsSource, context);
  vm.runInNewContext(discoverySource, context);
  return { elements, window, document, listeners, intervals, requests, externalURLs, searchTerms, newsViews, newsWindows, skillViews, discoveryViews, dispatch, nodeDependencyUI };
}

// Execute the actual bridge declaration from app.js with the discovery test's
// local API dispatcher. This keeps enable-state tests tied to the production
// bridge implementation instead of stubbing isSkillEnabled as a boolean.
function installRealAppBridge(workspace, target) {
  const start = appSource.indexOf("window.SkillManagerAPI = Object.freeze({");
  const marker = "\n});\n\n// renderOnlineSection";
  const end = appSource.indexOf(marker, start);
  assert.ok(start >= 0 && end > start, "app.js should expose the production SkillManagerAPI bridge");
  const state = { inventory: [], status: null, skillsShSkills: [] };
  const bridge = appSource.slice(start, end + "\n});".length);
  vm.runInNewContext(bridge, {
    window: workspace.window,
    state,
    AGENTS_NS: "@agents",
    currentTarget: () => target,
    api: workspace.dispatch,
    openExternal() {},
    runOnlineSearch: async () => {},
  });
  return { api: workspace.window.SkillManagerAPI, state };
}

function feed(items, nextCursor = "", meta = {}) {
  return { items, nextCursor, meta: { stale: false, notices: [], sources: [], coverage: {}, ...meta } };
}

function projectEntry(id, title, project, extra = {}) {
  return { id, kind: "project", title, summary: "Project capability description", reason: "Verified fields support this recommendation", canonicalUrl: "https://github.com/acme/tools", discoveredAt: "2026-10-27T12:00:00Z", paragraphs: [], project, ...extra };
}

function skillRef(pathname, overrides = {}) {
  return { repositoryId: 42, repo: "acme/tools", path: pathname, name: "review-helper", verified: true, ...overrides };
}

function standardProject(overrides = {}) {
  return {
    repositoryId: 42,
    repo: "acme/tools",
    type: "skill",
    stars: 184,
    createdAt: "2026-10-20T10:00:00Z",
    scenes: ["development", "testing"],
    growth: { metric: "stars_added", value: 14, windowStart: "2026-10-20T00:00:00Z", windowEnd: "2026-10-27T00:00:00Z", observedDays: 7, complete: true },
    skills: [skillRef("skills/review/SKILL.md")],
    ...overrides,
  };
}

async function settle() {
  await new Promise((resolve) => setImmediate(resolve));
  await Promise.resolve();
}

function descendants(node) { return (node.children || []).flatMap((child) => [child, ...descendants(child)]); }
function buttons(node) { return descendants(node).filter((child) => child.tagName === "BUTTON"); }

function activateDiscovery(workspace) {
  workspace.window.SkillManagerContent.onMainStatus({ firstRun: false });
  workspace.skillViews.find((button) => button.dataset.skillsView === "discovery").click();
}

test("index wires all project discovery views and loads its renderer before app.js", () => {
  for (const [id, label] of [["discovery-view-nav", "本周飙升"], ["discovery-view-nav", "新项目发现"], ["discovery-view-nav", "场景探索"], ["discovery-search-query", ""], ["discovery-scene-filter", ""]]) {
    assert.match(indexSource, new RegExp(`id="${id}"`), `${id} should exist in the real shell`);
    if (label) assert.match(indexSource, new RegExp(label));
  }
  const lifecycle = indexSource.indexOf('src="/content-workspace.js');
  const sharedUI = indexSource.indexOf('src="/content-ui.js');
  const news = indexSource.indexOf('src="/content-news.js');
  const discovery = indexSource.indexOf('src="/content-discovery.js');
  const app = indexSource.indexOf('src="/app.js');
  assert.ok(lifecycle >= 0 && sharedUI > lifecycle && news > sharedUI && discovery > news && app > discovery, "C04 lifecycle, shared helper, News, Discovery, then manager must load in order");
  const discoveryList = indexSource.indexOf('id="discovery-list"');
  const discoveryViews = indexSource.indexOf('id="discovery-view-nav"');
  assert.ok(discoveryList >= 0 && discoveryViews > discoveryList, "project views should sit inside the discovery list instead of a third rail");
  assert.doesNotMatch(indexSource, /id="discovery-rail"|>项目发现</);
  assert.match(indexSource, /class="content-columns discovery-columns" id="discovery-columns" data-layout="compact"/);
  assert.match(indexSource, /id="discovery-detail"[^>]* hidden>/);
  for (const [value, label] of [["development", "开发"], ["design", "设计"], ["testing", "测试"], ["documentation", "文档"], ["agent-workflow", "Agent 工作流"]]) {
    assert.match(indexSource, new RegExp(`value="${value}">${label}`), `${label} should be a fixed scene option`);
  }
});

test("empty project feed hides the unused detail pane and expands the list", async () => {
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status"
    ? { sources: [], coverage: {}, notices: [] }
    : { status: 200, data: feed([]), etag: '"empty"' } });
  activateDiscovery(workspace);
  await settle();

  assert.equal(workspace.elements["discovery-columns"].attributes["data-layout"], "compact");
  assert.equal(workspace.elements["discovery-detail"].hidden, true);
});

test("loaded project feed keeps reader open with selection guidance", async () => {
  const entry = projectEntry("project_reader", "reader project", standardProject());
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status"
    ? { sources: [], coverage: {}, notices: [] }
    : { status: 200, data: feed([entry]), etag: '"reader"' } });
  activateDiscovery(workspace);
  await settle();

  assert.equal(workspace.elements["discovery-columns"].attributes["data-layout"], "reader");
  assert.equal(workspace.elements["discovery-detail"].hidden, false);
  assert.match(workspace.elements["discovery-detail"].textContent, /选择一个项目/);
});

test("first project collection failure collapses the blank reader and offers retry and market search", async () => {
  const workspace = makeWorkspace({ request: async ({ url }) => {
    if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
    throw Object.assign(new Error("503 Service Unavailable"), { status: 503 });
  } });
  activateDiscovery(workspace);
  await settle();

  assert.equal(workspace.elements["discovery-columns"].attributes["data-layout"], "compact");
  assert.equal(workspace.elements["discovery-detail"].hidden, true);
  assert.match(workspace.elements["discovery-cards"].textContent, /首次收录暂时不可用/);
  assert.match(workspace.elements["discovery-cards"].textContent, /搜索技能市场/);
  assert.ok(buttons(workspace.elements["discovery-cards"]).some((button) => button.textContent === "重试"));
  assert.doesNotMatch(workspace.elements["discovery-cards"].textContent, /0 个候选/);
  assert.equal(workspace.elements["discovery-market-search"].hidden, false);
});

test("transient list failure retains the selected cached project detail", async () => {
  const entry = projectEntry("project_cached", "cached project", standardProject());
  let unavailable = false;
  const workspace = makeWorkspace({ request: async ({ url }) => {
    if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
    if (unavailable) throw Object.assign(new Error("503 Service Unavailable"), { status: 503 });
    return url.includes("/api/content/feed")
      ? { status: 200, data: feed([entry]), etag: '"cached"' }
      : { status: 200, data: { entry, meta: { sources: [], coverage: {} } }, etag: '"detail"' };
  } });
  activateDiscovery(workspace);
  await settle();
  buttons(workspace.elements["discovery-cards"])[0].click();
  await settle();
  const detail = workspace.elements["discovery-detail"].textContent;
  unavailable = true;
  await workspace.window.SkillManagerContent.refreshFeed(true);

  assert.equal(workspace.elements["discovery-columns"].attributes["data-layout"], "reader");
  assert.equal(workspace.elements["discovery-detail"].hidden, false);
  assert.equal(workspace.elements["discovery-detail"].textContent, detail);
});

test("project cards stay compact while the reader keeps localized dates and full growth context", async () => {
  const longSummary = "A project summary that is intentionally long so the list can clamp its preview while the detail reader keeps the full description.";
  const entry = projectEntry("project_compact", "compact project", standardProject(), { summary: longSummary });
  const metadata = {
    sources: [{ name: "github", state: "stale" }],
    coverage: { candidates: 28, completeGrowth: 3 },
  };
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status" ? metadata
    : url.includes("/api/content/feed") ? { status: 200, data: feed([entry], "", metadata), etag: '"compact"' }
      : { status: 200, data: { entry, meta: metadata }, etag: '"detail"' } });
  activateDiscovery(workspace);
  await settle();
  const card = buttons(workspace.elements["discovery-cards"])[0];
  assert.match(card.textContent, /compact project/);
  assert.match(card.textContent, /acme\/tools/);
  assert.match(card.textContent, /近 7 日新增 Star \+14/);
  assert.doesNotMatch(card.textContent, /2026-10|总 Star|GitHub|适用场景/);
  assert.match(cssSource, /\.discovery-project-card \.content-card-summary[\s\S]*?line-clamp:\s*2/);
  card.click();
  await settle();
  const detail = workspace.elements["discovery-detail"].textContent;
  assert.match(detail, new RegExp(longSummary));
  assert.match(detail, /项目创建：[^\n]*2026/);
  assert.match(detail, /总 Star：184/);
  assert.match(detail, /2026/);
  assert.doesNotMatch(detail, /2026-10-\d\dT/);
  assert.match(detail, /完整周数据|候选项目/);
  assert.match(detail, /GitHub 数据状态：尚无可用数据/);
});

test("partial discovery coverage does not turn a missing count into a false zero", async () => {
  const entry = projectEntry("project_coverage", "coverage project", standardProject());
  const metadata = { coverage: { candidates: 5 }, sources: [{ name: "github", state: "fresh" }] };
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status" ? metadata
    : { status: 200, data: feed([entry], "", metadata), etag: '"partial-coverage"' } });
  activateDiscovery(workspace);
  await settle();

  assert.match(workspace.elements["discovery-meta-notices"].textContent, /5 个候选项目/);
  assert.doesNotMatch(workspace.elements["discovery-meta-notices"].textContent, /0 个有完整周数据/);
});

test("project views and fixed scenes update the C04 query, reset cursors, and keep market search editable", async () => {
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status" ? { sources: [], coverage: {}, notices: [] } : { status: 200, data: feed([], "next"), etag: '"page"' } });
  activateDiscovery(workspace);
  await settle();
  let query = new URL(workspace.requests.filter((call) => call.url.includes("/api/content/feed")).at(-1).url, "http://local").searchParams;
  assert.equal(query.get("kind"), "project");
  assert.equal(query.get("view"), "weekly");

  workspace.window.SkillManagerContent.setFeedCursor("cursor-one");
  await settle();
  workspace.window.SkillManagerContent.session.project.entryId = "project_old";
  workspace.discoveryViews.find((button) => button.dataset.discoveryView === "scenarios").click();
  await settle();
  workspace.elements["discovery-scene-filter"].value = "testing";
  workspace.elements["discovery-scene-filter"].listeners.change({ target: workspace.elements["discovery-scene-filter"] });
  await settle();
  query = new URL(workspace.requests.filter((call) => call.url.includes("/api/content/feed")).at(-1).url, "http://local").searchParams;
  assert.equal(query.get("view"), "scenarios");
  assert.equal(query.get("scene"), "testing");
  assert.equal(workspace.window.SkillManagerContent.session.project.cursor, "");
  assert.deepEqual(Array.from(workspace.window.SkillManagerContent.session.project.cursorStack), []);
  assert.equal(workspace.window.SkillManagerContent.session.project.entryId, "");

  const search = workspace.elements["discovery-search-query"];
  search.value = "agent tools";
  workspace.elements["discovery-search-form"].listeners.submit({ preventDefault() {} });
  await settle();
  query = new URL(workspace.requests.filter((call) => call.url.includes("/api/content/feed")).at(-1).url, "http://local").searchParams;
  assert.equal(query.get("q"), "agent tools");
  search.value = "editable market term";
  workspace.elements["discovery-market-search"].click();
  await settle();
  assert.deepEqual(Array.from(workspace.searchTerms), ["editable market term"]);
  assert.equal(workspace.elements["manage-workspace"].hidden, false);
});

test("compact project cards leave dates, repo Stars, growth windows and freshness for the reader", async () => {
  const entry = projectEntry("project_42", "acme/tools", standardProject());
  const metadata = {
    stale: false,
    fetchedAt: "2099-01-01T00:00:00Z",
    coverage: { candidates: 28, completeGrowth: 3 },
    sources: [{ name: "aihot:news", state: "fresh", lastSuccessAt: "2099-01-01T00:00:00Z" }, { name: "github", state: "rate_limited", lastSuccessAt: "2026-10-28T09:00:00Z" }],
    notices: [],
  };
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status" ? metadata : url.includes("/api/content/feed") ? { status: 200, data: feed([entry], "", metadata), etag: '"feed"' } : { status: 200, data: { entry, meta: metadata }, etag: '"detail"' } });
  activateDiscovery(workspace);
  await settle();
  const card = buttons(workspace.elements["discovery-cards"])[0];
  assert.match(card.textContent, /Skill/);
  assert.match(card.textContent, /acme\/tools/);
  assert.match(card.textContent, /近 7 日新增 Star \+14/);
  assert.doesNotMatch(card.textContent, /2026-10|总 Star|GitHub|适用场景/);
  assert.match(workspace.elements["discovery-meta-notices"].textContent, /28.*3/);
  card.click();
  await settle();
  const detail = workspace.elements["discovery-detail"];
  assert.match(detail.textContent, /项目创建：[^\n]*2026/);
  assert.match(detail.textContent, /首次收录：[^\n]*2026/);
  assert.doesNotMatch(detail.textContent, /2026-10-\d\dT/);
  assert.match(detail.textContent, /总 Star：184（仓库级）/);
  assert.match(detail.textContent, /近 7 日新增 Star \+14/);
  assert.match(detail.textContent, /2026/);
  assert.match(detail.textContent, /28.*3/);
  assert.match(detail.textContent, /GitHub 数据状态：等待更新/);
  assert.match(detail.textContent, /最近成功更新 .*2026.*UTC/);
  assert.doesNotMatch(detail.textContent, /2026-10-28T/);
  assert.doesNotMatch(detail.textContent, /2099-01-01/, "AIHOT and generic fetchedAt timestamps must not determine GitHub freshness");
});

test("Skill references and their total appear before collapsed project facts", async () => {
  const refs = Array.from({ length: 13 }, (_, index) => skillRef(
    `skills/ref-${index}/SKILL.md`,
    { name: index === 0 || index === 1 ? "review-helper" : `helper-${index}`, verified: index !== 2 },
  ));
  const entry = projectEntry("project_many_skills", "many skills", standardProject({ skills: refs }));
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status"
    ? { sources: [], coverage: {}, notices: [] }
    : url.includes("/api/content/feed") ? { status: 200, data: feed([entry]), etag: '"many-skills"' }
      : { status: 200, data: { entry, meta: { sources: [], coverage: {} } }, etag: '"many-skills-detail"' } });
  activateDiscovery(workspace);
  await settle();

  const card = buttons(workspace.elements["discovery-cards"])[0];
  assert.match(card.textContent, /Skill 引用：13/);
  card.click();
  await settle();

  const detail = workspace.elements["discovery-detail"];
  const skillList = detail.children.find((node) => node.className === "discovery-skill-list");
  const projectInfo = detail.children.find((node) => node.tagName === "DETAILS");
  assert.ok(skillList && projectInfo, "Skill rows and project facts should be separate reader sections");
  assert.match(skillList.children[0].textContent, /仓库内的 Skill（13）/);
  assert.equal(descendants(skillList).filter((node) => node.className === "discovery-skill-ref").length, 13);
  const summaryIndex = detail.children.findIndex((node) => node.tagName === "P");
  assert.equal(detail.children.indexOf(skillList), summaryIndex + 1, "the counted Skill list should follow the summary");
  assert.ok(detail.children.indexOf(projectInfo) > detail.children.indexOf(skillList), "project facts should follow the Skill list");
  assert.equal(projectInfo.children[0].tagName, "SUMMARY");
  assert.equal(projectInfo.children[0].textContent, "项目与增长信息");
  assert.equal(projectInfo.attributes.open, undefined, "native disclosure starts closed");
  assert.match(projectInfo.textContent, /总 Star：184（仓库级）/);
  const projectLink = buttons(detail).find((button) => button.textContent === "查看项目说明");
  assert.ok(projectLink, "the safe project link remains available near the title");
});

test("growth labels report only complete seven-day source additions as near-seven-day growth", async () => {
  const entries = [
    projectEntry("project_full", "full", standardProject()),
    projectEntry("project_short", "short", standardProject({ growth: { metric: "stars_added", value: 5, observedDays: 3, complete: false, windowStart: "2026-10-24T00:00:00Z", windowEnd: "2026-10-27T00:00:00Z" } })),
    projectEntry("project_net", "net", standardProject({ growth: { metric: "stars_net", value: -2, observedDays: 2, complete: false, windowStart: "2026-10-25T00:00:00Z", windowEnd: "2026-10-27T00:00:00Z" } })),
    projectEntry("project_instant", "instant", standardProject({ growth: { metric: "stars_added", value: 1, observedDays: 0, complete: false, windowStart: "2026-10-27T11:00:00Z", windowEnd: "2026-10-27T12:00:00Z" } })),
  ];
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status" ? { sources: [], coverage: {}, notices: [] } : { status: 200, data: feed(entries), etag: '"growth"' } });
  activateDiscovery(workspace);
  await settle();
  const cards = buttons(workspace.elements["discovery-cards"]);
  assert.match(cards[0].textContent, /近 7 日新增 Star \+14/);
  assert.match(cards[1].textContent, /观察 3 天新增 Star \+5/);
  assert.match(cards[2].textContent, /观察 2 天净增 −2/);
  assert.match(cards[3].textContent, /观察不足 1 天新增 Star \+1/);
  for (const card of cards.slice(1)) assert.doesNotMatch(card.textContent, /近 7 日新增 Star/);
});

test("only verified unique safe same-repository SkillRefs can start installation with exact identity", async () => {
  const entry = projectEntry("project_42", "acme/tools", standardProject({ skills: [
    skillRef("skills/review/SKILL.md"),
    skillRef("old/review/SKILL.md"),
    skillRef("unverified/SKILL.md", { name: "unverified", verified: false }),
    skillRef("../escape/SKILL.md", { name: "escape" }),
    skillRef("elsewhere/SKILL.md", { name: "other-repo", repo: "other/repo" }),
  ] }));
  let installCall;
  const workspace = makeWorkspace({ request: async ({ method, url, body }) => {
    if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
    if (url.includes("/api/content/feed")) return { status: 200, data: feed([entry]), etag: '"feed"' };
    if (method === "POST" && url === "/api/skillssh/add") { installCall = { method, url, body }; return { ok: true, identityStatus: "exact" }; }
    if (method === "GET" && url === "/api/skillssh") return [{ linkName: "review-helper", identityStatus: "exact", contentIdentity: { repositoryId: 42, path: "safe/nested/SKILL.md", sourceRepo: "acme/tools", name: "review-helper", canonicalDir: "/physical/skills-store/review-helper" } }];
    return { status: 200, data: { entry, meta: { sources: [], coverage: {} } }, etag: '"detail"' };
  } });
  activateDiscovery(workspace);
  await settle();
  buttons(workspace.elements["discovery-cards"])[0].click();
  await settle();
  const rows = workspace.elements["discovery-detail"].children.filter((node) => node.tagName === "SECTION");
  const installable = buttons(workspace.elements["discovery-detail"]).filter((button) => button.textContent.includes("安装 Skill"));
  assert.equal(installable.length, 0, "duplicate names must make all name-only install refs ambiguous");
  assert.ok(rows.some((row) => row.textContent.includes("skills/review/SKILL.md")));

  const unique = projectEntry("project_unique", "unique", standardProject({ skills: [skillRef("safe/nested/SKILL.md")] }));
  workspace.window.SkillManagerContent.renderSelectedEntry(unique, { sources: [], coverage: {} }, "skills");
  const button = buttons(workspace.elements["discovery-detail"]).find((candidate) => candidate.textContent === "安装 Skill");
  assert.ok(button, "a verified unique ref should expose install");
  button.click();
  await settle();
  assert.deepEqual(JSON.parse(JSON.stringify(installCall.body)), { contentIdentity: { entryId: "project_unique", repositoryId: 42, path: "safe/nested/SKILL.md", name: "review-helper" } });
  assert.match(workspace.elements["discovery-detail"].textContent, /已安装 · 未启用/);
});

test("a verified discovery install stays actionable but opens Node guidance and makes no API request when Node is missing", async () => {
  const entry = projectEntry("project_unique", "unique", standardProject({ skills: [skillRef("safe/nested/SKILL.md")] }));
  const workspace = makeWorkspace({
    nodeStatus: { npxAvailable: false, nodeDependency: { state: "missing", requiredVersion: "22.20.0" } },
    request: async ({ url }) => url === "/api/content/status" ? { sources: [], coverage: {}, notices: [] } : { status: 200, data: { entry, meta: { sources: [], coverage: {} } }, etag: '"detail"' },
  });
  workspace.window.SkillManagerContent.onMainStatus({ firstRun: false });
  workspace.window.SkillManagerContent.renderSelectedEntry(entry, { sources: [], coverage: {} }, "skills");
  const button = buttons(workspace.elements["discovery-detail"]).find((candidate) => candidate.textContent === "安装 Skill");
  assert.ok(button, "the install entry should remain available to explain the dependency");
  button.click();
  await settle();

  assert.equal(workspace.requests.some((call) => call.method === "POST" && call.url === "/api/skillssh/add"), false,
    "the UI should stop before making an install request");
  assert.match(workspace.document.body.textContent, /未检测到可用的 Node\.js/);
  assert.match(workspace.document.body.textContent, /22\.20\.0/);
  assert.notEqual(button.disabled, true, "the install action should remain retryable after installing Node");
});

test("verified installs reload skills.sh and enable separately by actual LinkName and current target", async () => {
  const entry = projectEntry("project_unique", "unique", standardProject({ skills: [skillRef("safe/nested/SKILL.md")] }));
  const installed = {
    linkName: "actual-link-name",
    identityStatus: "exact",
    contentIdentity: { repositoryId: 42, path: "safe/nested/SKILL.md", sourceRepo: "acme/tools", name: "review-helper", canonicalDir: "/physical/skills-store/review-helper" },
  };
  let enabled = false;
  let enableCall = null;
  const workspace = makeWorkspace({ request: async ({ method, url, body }) => {
    if (method === "POST" && url === "/api/skillssh/add") return { ok: true, status: "installed", identityStatus: "exact" };
    if (method === "GET" && url === "/api/skillssh") return [installed];
    if (method === "POST" && url === "/api/enabled") { enableCall = { method, url, body }; enabled = true; return { ok: true }; }
    if (method === "POST" && url === "/api/apply") return { ok: true };
    return { sources: [], coverage: {}, notices: [] };
  } });
  const api = workspace.window.SkillManagerAPI;
  api.currentTarget = () => "/work/current-project/.claude/skills";
  api.isSkillEnabled = (linkName, target) => enabled && linkName === "actual-link-name" && target === api.currentTarget();
  api.refreshSkillsSh = async () => api.request("GET", "/api/skillssh");

  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: false });
  workspace.elements["workspace-news"].click();
  content.captureReturnArticle("news_article_17", () => {});
  workspace.skillViews.find((button) => button.dataset.skillsView === "manage").click();
  workspace.skillViews.find((button) => button.dataset.skillsView === "discovery").click();
  content.renderSelectedEntry(entry, { sources: [], coverage: {} }, "skills");
  const install = buttons(workspace.elements["discovery-detail"]).find((button) => button.textContent === "安装 Skill");
  install.click();
  await settle();

  assert.ok(workspace.requests.some((call) => call.method === "GET" && call.url === "/api/skillssh"), "install confirmation must reload the daemon's live skills.sh listing");
  assert.match(workspace.elements["discovery-detail"].textContent, /已安装 · 未启用/);
  assert.equal(content.session.returnArticle.id, "news_article_17", "install keeps the related-article return context");
  assert.equal(workspace.elements["return-article-control"].hidden, false);
  const enable = buttons(workspace.elements["discovery-detail"]).find((button) => button.textContent === "启用到当前目录");
  assert.ok(enable, "verified but disabled skills expose a separate enable action");
  enable.click();
  await settle();
  assert.deepEqual(JSON.parse(JSON.stringify(enableCall.body)), {
    skill: "@agents/actual-link-name", target: "/work/current-project/.claude/skills", mode: "snapshot",
  });
  const enableIndex = workspace.requests.findIndex((call) => call.method === "POST" && call.url === "/api/enabled");
  const applyIndex = workspace.requests.findIndex((call) => call.method === "POST" && call.url === "/api/apply");
  assert.ok(enableIndex >= 0 && applyIndex > enableIndex, "enable applies only after POST /api/enabled");
  assert.match(workspace.elements["discovery-detail"].textContent, /已安装 · 已启用/);
});

test("already enabled verified installs show the live enabled state without another enable action", async () => {
  const entry = projectEntry("project_unique", "unique", standardProject({ skills: [skillRef("safe/nested/SKILL.md")] }));
  const workspace = makeWorkspace({ request: async ({ method, url }) => {
    if (method === "POST" && url === "/api/skillssh/add") return { ok: true, identityStatus: "exact" };
    if (method === "GET" && url === "/api/skillssh") return [{
      linkName: "actual-link-name", identityStatus: "exact",
      contentIdentity: { repositoryId: 42, path: "safe/nested/SKILL.md", sourceRepo: "acme/tools", name: "review-helper", canonicalDir: "/physical/skills-store/review-helper" },
    }];
    return { sources: [], coverage: {}, notices: [] };
  } });
  const api = workspace.window.SkillManagerAPI;
  api.currentTarget = () => "/work/selected";
  api.isSkillEnabled = () => true;
  api.refreshSkillsSh = async () => api.request("GET", "/api/skillssh");
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: false });
  content.renderSelectedEntry(entry, { sources: [], coverage: {} }, "skills");
  buttons(workspace.elements["discovery-detail"]).find((button) => button.textContent === "安装 Skill").click();
  await settle();
  assert.match(workspace.elements["discovery-detail"].textContent, /已安装 · 已启用/);
  assert.equal(buttons(workspace.elements["discovery-detail"]).some((button) => button.textContent === "启用到当前目录"), false);
});

test("the production bridge requires a materialized @agents link, not a desired selector", async () => {
  const entry = projectEntry("project_unique", "unique", standardProject({ skills: [skillRef("safe/nested/SKILL.md")] }));
  const installed = {
    linkName: "actual-link-name", identityStatus: "exact",
    contentIdentity: { repositoryId: 42, path: "safe/nested/SKILL.md", sourceRepo: "acme/tools", name: "review-helper", canonicalDir: "/physical/skills-store/review-helper" },
  };
  const target = "/work/selected";
  const cases = [
    { name: "desired only", materialized: false, inventoryTarget: target, linkTarget: "/physical/skills-store/review-helper", wantEnabled: false },
    { name: "matching materialized link", materialized: true, inventoryTarget: target, linkTarget: "/physical/skills-store/review-helper", wantEnabled: true },
    { name: "different target", materialized: true, inventoryTarget: "/work/other", linkTarget: "/physical/skills-store/review-helper", wantEnabled: false },
    { name: "different canonical source", materialized: true, inventoryTarget: target, linkTarget: "/physical/skills-store/other-skill", wantEnabled: false },
  ];
  for (const scenario of cases) {
    const inventory = scenario.materialized ? [{
      name: "actual-link-name", kind: "skills.sh", selector: "@agents/actual-link-name",
      managed: true, enabled: true, linkTarget: scenario.linkTarget,
    }] : [];
    const workspace = makeWorkspace({ request: async ({ method, url }) => {
      if (method === "POST" && url === "/api/skillssh/add") return { ok: true, identityStatus: "exact" };
      if (method === "GET" && url === "/api/skillssh") return [installed];
      if (method === "GET" && url === "/api/status") return { enabled: [{ skill: "@agents/actual-link-name", target }] };
      if (method === "GET" && url.startsWith("/api/inventory?")) return { target: scenario.inventoryTarget, items: inventory };
      return { sources: [], coverage: {}, notices: [] };
    } });
    installRealAppBridge(workspace, target);
    const content = workspace.window.SkillManagerContent;
    content.onMainStatus({ firstRun: false });
    content.renderSelectedEntry(entry, { sources: [], coverage: {} }, "skills");
    buttons(workspace.elements["discovery-detail"]).find((button) => button.textContent === "安装 Skill").click();
    await settle();
    assert.equal(workspace.elements["discovery-detail"].textContent.includes("已安装 · 已启用"), scenario.wantEnabled,
      `${scenario.name} must match the current target and canonical source`);
    assert.equal(buttons(workspace.elements["discovery-detail"]).some((button) => button.textContent === "启用到当前目录"), !scenario.wantEnabled);
    assert.ok(workspace.requests.some((call) => call.method === "GET" && call.url === `/api/inventory?target=${encodeURIComponent(target)}`),
      "the actual target inventory must be checked");
  }
});

test("apply errors and target collisions keep enable retry available with honest state", async () => {
  const entry = projectEntry("project_unique", "unique", standardProject({ skills: [skillRef("safe/nested/SKILL.md")] }));
  const installed = {
    linkName: "actual-link-name", identityStatus: "exact",
    contentIdentity: { repositoryId: 42, path: "safe/nested/SKILL.md", sourceRepo: "acme/tools", name: "review-helper", canonicalDir: "/physical/skills-store/review-helper" },
  };
  const target = "/work/selected";
  const cases = [
    { name: "apply permission error", summary: { errors: ["permission denied while creating link"], conflicts: [] }, message: /permission denied/ },
    { name: "same-target collision", summary: { errors: [], conflicts: [{ kind: "collision", linkName: "actual-link-name", targets: [target], sources: ["other/source"] }] }, message: /冲突|collision/i },
  ];
  for (const scenario of cases) {
    let desired = [];
    let applyCount = 0;
    const workspace = makeWorkspace({ request: async ({ method, url, body }) => {
      if (method === "POST" && url === "/api/skillssh/add") return { ok: true, identityStatus: "exact" };
      if (method === "GET" && url === "/api/skillssh") return [installed];
      if (method === "GET" && url === "/api/status") return { enabled: desired };
      if (method === "GET" && url.startsWith("/api/inventory?")) return { target, items: [] };
      if (method === "POST" && url === "/api/enabled") {
        desired = [{ skill: body.skill, target: body.target }];
        return { ok: true };
      }
      if (method === "POST" && url === "/api/apply") { applyCount++; return scenario.summary; }
      return { sources: [], coverage: {}, notices: [] };
    } });
    installRealAppBridge(workspace, target);
    const content = workspace.window.SkillManagerContent;
    content.onMainStatus({ firstRun: false });
    content.renderSelectedEntry(entry, { sources: [], coverage: {} }, "skills");
    buttons(workspace.elements["discovery-detail"]).find((button) => button.textContent === "安装 Skill").click();
    await settle();
    const enable = buttons(workspace.elements["discovery-detail"]).find((button) => button.textContent === "启用到当前目录");
    assert.ok(enable, `${scenario.name}: exact install should offer separate enable`);
    enable.click();
    await settle();
    assert.equal(applyCount, 1, `${scenario.name}: apply is attempted`);
    assert.match(workspace.elements["discovery-detail"].textContent, scenario.message, `${scenario.name}: show the apply outcome`);
    assert.doesNotMatch(workspace.elements["discovery-detail"].textContent, /已安装 · 已启用/,
      `${scenario.name}: desired config alone must not look enabled`);
    assert.ok(buttons(workspace.elements["discovery-detail"]).some((button) => button.textContent === "重试启用"),
      `${scenario.name}: keep the action retryable`);
  }
});

test("an exact install response is not enough without a matching exact live listing", async () => {
  const entry = projectEntry("project_unique", "unique", standardProject({ skills: [skillRef("safe/nested/SKILL.md")] }));
  const workspace = makeWorkspace({ request: async ({ method, url }) => {
    if (method === "POST" && url === "/api/skillssh/add") return { ok: true, identityStatus: "exact" };
    if (method === "GET" && url === "/api/skillssh") return [{
      linkName: "wrong-physical-skill", identityStatus: "source_same_pending",
      contentIdentity: { repositoryId: 42, path: "safe/nested/SKILL.md", sourceRepo: "acme/tools", name: "review-helper" },
    }];
    return { sources: [], coverage: {}, notices: [] };
  } });
  const api = workspace.window.SkillManagerAPI;
  api.currentTarget = () => "/work/selected";
  api.isSkillEnabled = () => false;
  api.refreshSkillsSh = async () => api.request("GET", "/api/skillssh");
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: false });
  content.renderSelectedEntry(entry, { sources: [], coverage: {} }, "skills");
  buttons(workspace.elements["discovery-detail"]).find((button) => button.textContent === "安装 Skill").click();
  await settle();
  assert.match(workspace.elements["discovery-detail"].textContent, /实时 skills\.sh 列表未确认此身份/);
  assert.doesNotMatch(workspace.elements["discovery-detail"].textContent, /已安装 · 已启用|已安装 · 未启用/);
  assert.equal(buttons(workspace.elements["discovery-detail"]).some((button) => button.textContent === "启用到当前目录"), false);
});

test("agent and tool details show only safe project links, while project text stays text", async () => {
  const entries = [
    projectEntry("project_agent", "<img src=x>", standardProject({ type: "agent", skills: [skillRef("workflow/SKILL.md")], scenes: ["agent-workflow"] }), { canonicalUrl: "javascript:alert(1)" }),
    projectEntry("project_tool", "Acme tool", standardProject({ type: "tool", skills: [skillRef("tool/SKILL.md")] }), { canonicalUrl: "https://github.com/acme/tools" }),
  ];
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status" ? { sources: [], coverage: {}, notices: [] } : url.includes("/api/content/feed") ? { status: 200, data: feed(entries), etag: '"feed"' } : { status: 200, data: { entry: entries.find((item) => url.endsWith(item.id)), meta: { sources: [], coverage: {} } }, etag: '"detail"' } });
  activateDiscovery(workspace);
  await settle();
  buttons(workspace.elements["discovery-cards"])[0].click();
  await settle();
  assert.match(workspace.elements["discovery-detail"].textContent, /Agent 项目/);
  assert.match(workspace.elements["discovery-detail"].textContent, /<img src=x>/);
  assert.equal(descendants(workspace.elements["discovery-detail"]).some((node) => node.tagName === "IMG"), false);
  assert.equal(descendants(workspace.elements["discovery-detail"]).some((node) => node.className === "discovery-skill-list"), false);
  assert.equal(buttons(workspace.elements["discovery-detail"]).some((button) => button.textContent.includes("安装")), false);
  assert.equal(buttons(workspace.elements["discovery-detail"]).some((button) => button.textContent === "查看项目说明"), false);

  buttons(workspace.elements["discovery-cards"])[1].click();
  await settle();
  assert.match(workspace.elements["discovery-detail"].textContent, /工具项目/);
  assert.equal(descendants(workspace.elements["discovery-detail"]).some((node) => node.className === "discovery-skill-list"), false);
  assert.equal(buttons(workspace.elements["discovery-detail"]).some((button) => button.textContent.includes("安装")), false);
  const projectLink = buttons(workspace.elements["discovery-detail"]).find((button) => button.textContent === "查看项目说明");
  assert.ok(projectLink);
  projectLink.click();
  assert.deepEqual(Array.from(workspace.externalURLs), ["https://github.com/acme/tools"]);
});

test("empty weekly feed explains that full week data is still being collected and points to other views", async () => {
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status" ? { sources: [], coverage: {}, notices: [] } : { status: 200, data: feed([], "", { coverage: { candidates: 28, completeGrowth: 0 } }), etag: '"empty"' } });
  activateDiscovery(workspace);
  await settle();
  assert.match(workspace.elements["discovery-cards"].textContent, /正在积累周数据/);
  const toNew = buttons(workspace.elements["discovery-cards"]).find((button) => button.textContent === "新项目发现");
  assert.ok(toNew);
  toNew.click();
  await settle();
  assert.equal(workspace.window.SkillManagerContent.session.project.view, "new");
});

test("pending and conflict install identities remain distinct from verified installation", async () => {
  const entry = projectEntry("project_pending", "pending", standardProject({ skills: [skillRef("review/SKILL.md")] }));
  for (const [identityStatus, expected] of [
    ["source_same_pending", /来源相同，安装路径待核对/],
    ["conflict", /检测到同名或来源冲突/],
    ["unverified", /身份尚未核验；未确认安装状态/],
  ]) {
    const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/skillssh/add"
      ? { ok: true, identityStatus }
      : url === "/api/content/status" ? { sources: [], coverage: {}, notices: [] }
        : { status: 200, data: feed([]), etag: '"empty"' } });
    const content = workspace.window.SkillManagerContent;
    content.onMainStatus({ firstRun: false });
    content.renderSelectedEntry(entry, { sources: [], coverage: {} }, "skills");
    const button = buttons(workspace.elements["discovery-detail"]).find((candidate) => candidate.textContent === "安装 Skill");
    button.click();
    await settle();
    assert.match(workspace.elements["discovery-detail"].textContent, expected);
    assert.doesNotMatch(workspace.elements["discovery-detail"].textContent, /已安装，身份已核实/);
  }
});

test("feed and install failures stay visible without claiming an installation", async () => {
  const unavailable = makeWorkspace({ request: async ({ url }) => {
    if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
    throw Object.assign(new Error("network unavailable"), { status: 503 });
  } });
  activateDiscovery(unavailable);
  await settle();
  assert.match(unavailable.elements["discovery-cards"].textContent, /首次收录暂时不可用/);
  assert.ok(buttons(unavailable.elements["discovery-cards"]).some((button) => button.textContent === "重试"));

  const entry = projectEntry("project_failure", "failure", standardProject({ skills: [skillRef("review/SKILL.md")] }));
  const failedInstall = makeWorkspace({ request: async ({ url }) => {
    if (url === "/api/skillssh/add") throw Object.assign(new Error("install transport unavailable"), { status: 503 });
    return url === "/api/content/status" ? { sources: [], coverage: {}, notices: {} } : { status: 200, data: feed([]), etag: '"empty"' };
  } });
  const content = failedInstall.window.SkillManagerContent;
  content.onMainStatus({ firstRun: false });
  content.renderSelectedEntry(entry, { sources: [], coverage: {} }, "skills");
  const button = buttons(failedInstall.elements["discovery-detail"]).find((candidate) => candidate.textContent === "安装 Skill");
  button.click();
  await settle();
  assert.match(failedInstall.elements["discovery-detail"].textContent, /安装未确认：install transport unavailable/);
  assert.doesNotMatch(failedInstall.elements["discovery-detail"].textContent, /已安装/);
  assert.equal(button.disabled, false, "failed requests can be retried");
});

test("expired project cursors restart the same query at page one with an inline hint", async () => {
  const calls = [];
  const workspace = makeWorkspace({ request: async ({ url }) => {
    if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
    calls.push(url);
    if (url.includes("cursor=expired")) throw Object.assign(new Error("cursor expired"), { status: 409, code: "cursor_expired" });
    return { status: 200, data: feed([], "expired"), etag: '"page"' };
  } });
  activateDiscovery(workspace);
  await settle();
  workspace.window.SkillManagerContent.setFeedFilter("q", "agent");
  await settle();
  workspace.window.SkillManagerContent.setFeedCursor("expired");
  await settle();
  assert.ok(calls.at(-2).includes("q=agent"));
  assert.ok(calls.at(-2).includes("cursor=expired"));
  assert.ok(calls.at(-1).includes("q=agent"));
  assert.equal(calls.at(-1).includes("cursor="), false);
  assert.match(workspace.elements["discovery-update-hint"].textContent, /已从第一页重新加载/);
  assert.equal(workspace.window.SkillManagerContent.session.project.q, "agent");
});

test("market search from discovery preserves the related-news article return through real delegated subnav transitions", async () => {
  const story = { id: "news_return", kind: "news", title: "Agent workflow release", summary: "A release", category: "agents", canonicalUrl: "https://aihot.news/story", paragraphs: [] };
  const project = projectEntry("project_42", "Acme workflow", standardProject());
  const workspace = makeWorkspace({ request: async ({ url }) => {
    if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
    if (url.includes("kind=project")) return { status: 200, data: feed([project]), etag: '"projects"' };
    if (url.includes("/api/content/feed")) return { status: 200, data: feed([story]), etag: '"story"' };
    return { status: 200, data: { entry: story, meta: { stale: false, notices: [] } }, etag: '"story-detail"' };
  } });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  buttons(workspace.elements["news-cards"])[0].click();
  await settle();
  const related = descendants(workspace.elements["news-detail"]).find((node) => node.tagName === "BUTTON" && node.textContent === "查找相关 Skill");
  assert.ok(related);
  related.click();
  await settle();
  const saved = content.session.returnArticle;
  assert.equal(saved.id, story.id);
  assert.equal(workspace.elements["manage-workspace"].hidden, false);
  workspace.skillViews.find((button) => button.dataset.skillsView === "discovery").click();
  await settle();
  assert.equal(workspace.elements["discovery-workspace"].hidden, false, "real delegated click must enter Discovery");
  assert.equal(workspace.elements["manage-workspace"].hidden, true);
  assert.equal(content.session.returnArticle, saved);
  workspace.elements["discovery-search-query"].value = "market from discovery";
  workspace.elements["discovery-market-search"].click();
  await settle();
  assert.equal(workspace.elements["manage-workspace"].hidden, false, "market search must actually reach manage");
  assert.equal(content.session.returnArticle, saved);
  assert.equal(workspace.elements["return-article-control"].hidden, false);
  workspace.elements["return-article-control"].click();
  await settle();
  assert.equal(content.session.returnArticle, null);
  assert.equal(workspace.elements["news-workspace"].hidden, false);
  assert.match(workspace.elements["news-detail"].textContent, /Agent workflow release/);
});


test("complete net-only weekly fallback has its own explicit ranking title", async () => {
  const entry = projectEntry("project_net_full", "net fallback", standardProject({ growth: { metric: "stars_net", value: 12, observedDays: 7, complete: true, windowStart: "2026-10-20T00:00:00Z", windowEnd: "2026-10-27T00:00:00Z" } }));
  const workspace = makeWorkspace({ request: async ({ url }) => url === "/api/content/status" ? { sources: [], coverage: {}, notices: [] } : { status: 200, data: feed([entry]), etag: '"net"' } });
  activateDiscovery(workspace);
  await settle();
  assert.match(workspace.elements["discovery-cards"].textContent, /近 7 日净增 Star 榜/);
  assert.doesNotMatch(workspace.elements["discovery-cards"].textContent, /正在积累周数据/);
  workspace.window.SkillManagerContent.setFeedFilter("view", "scenarios");
  await settle();
  assert.doesNotMatch(workspace.elements["discovery-cards"].textContent, /近 7 日净增 Star 榜/);
});
