"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");
const vm = require("node:vm");

const contentSource = fs.readFileSync(path.join(__dirname, "content-workspace.js"), "utf8");
const contentUISource = fs.readFileSync(path.join(__dirname, "content-ui.js"), "utf8");
const appSource = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
const indexSource = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");

function sourceBetween(source, startMarker, endMarker) {
  const start = source.indexOf(startMarker);
  const end = source.indexOf(endMarker, start);
  assert(start >= 0 && end > start, `source markers should exist: ${startMarker}`);
  return source.slice(start, end);
}

function makeElement(id, hidden = false) {
  let text = "";
  const element = {
    id,
    hidden,
    dataset: {},
    attributes: {},
    listeners: {},
    children: [],
    scrollTop: 0,
    addEventListener(name, handler) { this.listeners[name] = handler; },
    setAttribute(name, value) { this.attributes[name] = value; },
    querySelectorAll(selector) {
      if (selector === "[data-skills-view]") return this.navButtons || [];
      return [];
    },
    contains(target) { return (this.navButtons || []).includes(target); },
    append(...nodes) { this.children.push(...nodes); nodes.forEach((node) => { node.parent = this; }); },
    replaceChildren(...nodes) { text = ""; this.children = []; this.append(...nodes); },
    remove() {
      if (this.parent) this.parent.children = this.parent.children.filter((child) => child !== this);
    },
    click() {
      if (typeof this.onclick === "function") this.onclick({ target: this });
      if (this.listeners.click) this.listeners.click({ target: this, preventDefault() {} });
    },
    get textContent() { return text + this.children.map((child) => child.textContent || "").join(""); },
    set textContent(value) { text = String(value ?? ""); this.children = []; },
  };
  return element;
}

function makeWorkspace(options = {}) {
  const elements = {
    "workspace-switcher": makeElement("workspace-switcher"),
    "workspace-news": makeElement("workspace-news"),
    "workspace-discovery": makeElement("workspace-discovery"),
    "workspace-manage": makeElement("workspace-manage"),
    "news-workspace": makeElement("news-workspace", true),
    "skills-workspace": makeElement("skills-workspace"),
    "discovery-workspace": makeElement("discovery-workspace", true),
    "manage-workspace": makeElement("manage-workspace"),
    "return-article-control": makeElement("return-article-control", true),
    "news-list": makeElement("news-list"),
    "discovery-list": makeElement("discovery-list"),
    "news-cards": makeElement("news-cards"),
    "discovery-cards": makeElement("discovery-cards"),
    "news-detail": makeElement("news-detail"),
    "discovery-detail": makeElement("discovery-detail"),
    "news-update-hint": makeElement("news-update-hint", true),
    "discovery-update-hint": makeElement("discovery-update-hint", true),
    "news-meta-notices": makeElement("news-meta-notices"),
    "discovery-meta-notices": makeElement("discovery-meta-notices"),
    "news-status": makeElement("news-status"),
    "discovery-status": makeElement("discovery-status"),
  };
  elements["news-workspace"].append(elements["news-status"]);
  elements["discovery-workspace"].append(elements["discovery-status"]);
  const skillViews = [
    Object.assign(elements["workspace-discovery"], { dataset: { skillsView: "discovery" } }),
    Object.assign(elements["workspace-manage"], { dataset: { skillsView: "manage" } }),
  ];
  elements["workspace-switcher"].navButtons = skillViews;
  elements["workspace-switcher"].append(elements["workspace-news"], ...skillViews, elements["return-article-control"]);
  const listeners = { document: {}, window: {} };
  const intervals = [];
  const document = {
    hidden: false,
    addEventListener(name, handler) { listeners.document[name] = handler; },
    getElementById: (id) => elements[id],
    createElement(tag) {
      const element = Object.assign(makeElement(tag), { tagName: tag.toUpperCase() });
      element.focus = () => { document.activeElement = element; };
      return element;
    },
    createTextNode: (value) => Object.assign(makeElement("#text"), { textContent: String(value) }),
    querySelectorAll(selector) {
      if (selector === ".content-status") return [elements["news-status"], elements["discovery-status"]];
      if (selector === "[data-skills-view]") return skillViews;
      if (selector === "[data-entry-id]") {
        return [elements["news-cards"], elements["discovery-cards"]].flatMap((root) => root.children.filter((child) => child.dataset.entryId));
      }
      return [];
    },
  };
  const window = {
    addEventListener(name, handler) { listeners.window[name] = handler; },
    requestAnimationFrame(callback) { callback(); },
    setInterval(callback, delay) { intervals.push({ callback, delay }); return intervals.length; },
  };
  const storage = new Map(Object.entries(options.storage || {}));
  const localStorage = {
    getItem(key) { return storage.has(key) ? storage.get(key) : null; },
    setItem(key, value) { storage.set(key, String(value)); },
  };
  const requests = [];
  window.SkillManagerAPI = {
    async request(method, url, body, requestOptions = {}) {
      const call = { method, url, body, options: requestOptions };
      requests.push(call);
      if (options.request) return options.request(call);
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      return { status: 200, data: { items: [], meta: { stale: false } }, etag: "" };
    },
  };
  class FakeDate extends Date {
    static now() { return options.now ? options.now() : 1_000_000; }
  }
  const context = {
    document,
    window,
    localStorage,
    Date: FakeDate,
    URL,
    URLSearchParams,
    encodeURIComponent,
  };
  vm.runInNewContext(contentSource, context);
  vm.runInNewContext(contentUISource, context);
  return { elements, window, document, listeners, intervals, storage, requests, skillViews };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

async function settle() {
  await new Promise((resolve) => setImmediate(resolve));
  await Promise.resolve();
}

function feed(items, stale = false, nextCursor = "") {
  return { items, nextCursor, meta: { stale, sources: [], coverage: {}, notices: [] } };
}

function entry(id, title) {
  return {
    id,
    kind: id.startsWith("project_") ? "project" : "news",
    title,
    summary: "Summary for " + title,
    source: { name: "Publisher" },
    paragraphs: [{ heading: "Details", text: "Body for " + title }],
    project: null,
  };
}

function httpError(status) {
  return Object.assign(new Error("HTTP " + status), { status });
}

test("the top navigation has three direct destinations and one active state", async () => {
  const match = indexSource.match(/<nav id="workspace-switcher"[\s\S]*?<\/nav>/);
  assert.ok(match, "the shared navigation row should exist");
  const nav = match[0];
  for (const [id, label] of [
    ["workspace-news", "AI 资讯"],
    ["workspace-discovery", "发现 Skills"],
    ["workspace-manage", "我的技能"],
  ]) {
    assert.match(nav, new RegExp(`id="${id}"[^>]*>[\\s\\S]*?${label}`));
  }
  assert.match(nav, /id="return-article-control"[^>]*hidden/);
  assert.doesNotMatch(indexSource, /id="workspace-skills"|id="skills-subnav"/, "the redundant parent and second row should be removed");

  const { elements, window, skillViews } = makeWorkspace();
  window.SkillManagerContent.onMainStatus({ firstRun: false });
  const currentRoutes = () => [elements["workspace-news"], ...skillViews]
    .filter((button) => button.attributes["aria-current"] === "page");
  assert.equal(currentRoutes().length, 1, "exactly one global route should be active at startup");

  const discovery = skillViews.find((button) => button.dataset.skillsView === "discovery");
  discovery.click();
  assert.equal(window.SkillManagerContent.session.surface, "skills");
  assert.equal(window.SkillManagerContent.session.skillsView, "discovery");
  assert.deepEqual(currentRoutes().map((button) => button.id), ["workspace-discovery"]);

  elements["workspace-news"].click();
  assert.deepEqual(currentRoutes().map((button) => button.id), ["workspace-news"]);
  assert.equal(elements["workspace-news"].attributes["aria-current"], "page");
  assert.equal(elements["return-article-control"].hidden, true, "the return action should hide once News is active");

  window.SkillManagerContent.captureReturnArticle("article_1", () => {});
  assert.equal(elements["return-article-control"].hidden, true, "the return action has no use while News itself is active");
  skillViews.find((button) => button.dataset.skillsView === "manage").click();
  assert.equal(window.SkillManagerContent.session.surface, "skills");
  assert.equal(window.SkillManagerContent.session.skillsView, "manage");
  assert.deepEqual(currentRoutes().map((button) => button.id), ["workspace-manage"]);
  assert.equal(elements["return-article-control"].hidden, false, "the return action appears while browsing related Skills");
});

function response(data) {
  return {
    status: 200,
    ok: true,
    statusText: "OK",
    headers: { get: () => null },
    text: async () => JSON.stringify(data),
  };
}

test("market search uses the visible manager and existing online search", async () => {
  const { elements, window } = makeWorkspace();
  elements["workspace-news"].listeners.click();
  assert.equal(elements["news-workspace"].hidden, false, "news should open before searching from an article");

  window.SkillManagerContent.showMarketSearch();
  assert.equal(elements["news-workspace"].hidden, true);
  assert.equal(elements["skills-workspace"].hidden, false);
  assert.equal(elements["manage-workspace"].hidden, false, "market search results render in the existing manager mount");
  assert.equal(elements["discovery-workspace"].hidden, true, "the empty discovery mount must stay hidden for this bridge");
  assert.equal(elements["workspace-manage"].attributes["aria-current"], "page");

  const apiFunction = sourceBetween(appSource, "async function api(", "\n\n// openExternal");
  const onlineSearch = sourceBetween(appSource, "async function runOnlineSearch()", "\n\n// renderOnlineSection");
  const bridge = sourceBetween(appSource, "window.SkillManagerAPI = Object.freeze({", "\n});") + "\n});";
  const calls = [];
  let renderCount = 0;
  const input = { value: "" };
  const state = {
    search: "",
    skillsShOnline: { gen: 0, results: [], error: "", loading: false, available: true },
  };
  const context = {
    TOKEN: "test-token",
    Headers,
    state,
    window,
    openExternal: async () => {},
    $(selector) { assert.equal(selector, "#search"); return input; },
    renderInventory() { renderCount += 1; },
    fetch: async (url, options) => {
      calls.push({ url, options });
      return response({ results: [], available: true });
    },
  };
  vm.runInNewContext(`${apiFunction}\n${onlineSearch}\n${bridge}`, context);

  await window.SkillManagerAPI.searchSkills("  agent tooling  ");
  assert.equal(state.search, "agent tooling");
  assert.equal(input.value, "agent tooling");
  assert.deepEqual(calls.map((call) => call.url).sort(), [
    "/api/skillhub/find?q=agent%20tooling",
    "/api/skillsmp/find?q=agent%20tooling",
    "/api/skillssh/find?q=agent%20tooling",
  ]);
  assert.ok(renderCount >= 2, "searchSkills should dispatch through the existing runOnlineSearch renderer");
});

test("online skillsmp results remain searchable when Node-backed registries are unavailable", async () => {
  const onlineSearch = sourceBetween(appSource, "async function runOnlineSearch()", "\n\n// Keep the content workspace");
  const state = {
    search: "audit",
    skillsShOnline: { term: "", loading: false, available: true, results: [], error: "", gen: 0 },
  };
  const context = {
    state,
    encodeURIComponent,
    renderInventory() {},
    async api(method, url) {
      assert.equal(method, "GET");
      if (url.startsWith("/api/skillssh/find")) return { available: false, results: [] };
      if (url.startsWith("/api/skillhub/find")) return { available: false, results: [] };
      return { available: true, results: [{ skill: "safe-audit", repoUrl: "https://github.com/acme/tools", stars: 12 }] };
    },
  };
  vm.runInNewContext(onlineSearch, context);
  await vm.runInNewContext("runOnlineSearch()", context);

  assert.equal(state.skillsShOnline.available, true, "HTTP-only marketplace search should keep the online section usable");
  assert.deepEqual(JSON.parse(JSON.stringify(state.skillsShOnline.results.map((result) => result.origin))), ["skillsmp"]);
});

test("a skills.sh update click opens Node guidance before sending an update request", async () => {
  const updateFunction = sourceBetween(appSource, "async function updateSkillsShAll(btn)", "\n\n// openRepoSkills shows a modal");
  let guardCalls = 0;
  let apiCalls = 0;
  const button = { textContent: "需要 Node.js", disabled: false };
  const context = {
    nodeDependencyUI: { requireNode(target) { assert.equal(target, button); guardCalls++; return false; } },
    async api() { apiCalls++; return { ok: true }; },
    toast() {},
    banner() {},
  };
  await vm.runInNewContext(`${updateFunction}\nupdateSkillsShAll(button)`, { ...context, button });

  assert.equal(guardCalls, 1);
  assert.equal(apiCalls, 0, "the update endpoint must not be called when the Node prompt is open");
  assert.equal(button.textContent, "需要 Node.js", "the update entry should remain available for retry after restarting");
});

test("onMainStatus initializes the saved or first-run workspace only once", async () => {
  const firstRun = makeWorkspace();
  firstRun.window.SkillManagerContent.onMainStatus({ firstRun: true });
  assert.equal(firstRun.elements["news-workspace"].hidden, false);
  assert.equal(firstRun.storage.get("skillmanager.workspace"), "news");

  firstRun.window.SkillManagerContent.onMainStatus({ firstRun: false });
  assert.equal(firstRun.elements["news-workspace"].hidden, false, "later status loads must not reset the chosen workspace");
  assert.equal(firstRun.storage.get("skillmanager.workspace"), "news");

  const existingUser = makeWorkspace();
  existingUser.window.SkillManagerContent.onMainStatus({ firstRun: false });
  assert.equal(existingUser.elements["skills-workspace"].hidden, false);
  assert.equal(existingUser.elements["manage-workspace"].hidden, false, "existing users start in 我的技能");
  assert.equal(existingUser.storage.get("skillmanager.workspace"), "skills");
  await settle();

  const savedChoice = makeWorkspace({ storage: { "skillmanager.workspace": "news" } });
  savedChoice.window.SkillManagerContent.onMainStatus({ firstRun: false });
  assert.equal(savedChoice.elements["news-workspace"].hidden, false, "a valid saved workspace takes precedence over firstRun");
  assert.deepEqual([...savedChoice.storage.keys()], ["skillmanager.workspace"], "only the top-level workspace is persisted");
  await settle();
});

test("feed query state is separate by surface and filters reset pagination", async () => {
  const workspace = makeWorkspace();
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: false });
  content.switchWorkspace("news");
  content.setFeedCursor("opaque/page/2");
  content.setFeedFilter("category", "tools");
  content.setFeedFilter("q", "  agent systems  ");

  let query = new URL(content.buildFeedURL(), "http://workspace.local").searchParams;
  assert.equal(query.get("kind"), "news");
  assert.equal(query.get("view"), "selected");
  assert.equal(query.get("window"), "7d");
  assert.equal(query.get("category"), "tools");
  assert.equal(query.get("q"), "agent systems");
  assert.equal(query.get("cursor"), null, "filter changes must clear the opaque page cursor");

  content.switchWorkspace("skills");
  content.setSkillsView("discovery");
  content.setFeedFilter("scene", "search");
  content.setFeedFilter("view", "new");
  query = new URL(content.buildFeedURL(), "http://workspace.local").searchParams;
  assert.equal(query.get("kind"), "project");
  assert.equal(query.get("view"), "new");
  assert.equal(query.get("scene"), "search");
  assert.equal(query.get("q"), null);

  content.switchWorkspace("news");
  query = new URL(content.buildFeedURL(), "http://workspace.local").searchParams;
  assert.equal(query.get("category"), "tools", "switching surfaces should preserve news filters");
  assert.equal(query.get("q"), "agent systems");
  await settle();
});

test("news and discovery renderers register independently with query context", async () => {
  const seen = [];
  const workspace = makeWorkspace();
  const content = workspace.window.SkillManagerContent;
  content.registerRenderer("news", {
    feed(page, stale, context) { seen.push({ name: "news", stale, context, count: page.items.length }); },
  });
  content.registerRenderer("discovery", {
    feed(page, stale, context) { seen.push({ name: "discovery", stale, context, count: page.items.length }); },
  });
  content.onMainStatus({ firstRun: true });
  await settle();
  content.switchWorkspace("skills");
  content.setSkillsView("discovery");
  await settle();

  assert.deepEqual(seen.map((call) => call.name), ["news", "discovery"]);
  assert.equal(seen[0].context.surface, "news");
  assert.equal(seen[0].context.query.kind, "news");
  assert.equal(seen[1].context.surface, "discovery");
  assert.equal(seen[1].context.query.view, "weekly");
  assert.equal(seen[1].context.skillsView, "discovery");
});

test("feed revalidation sends ETag and a 304 preserves cached stale data", async () => {
  let feedCalls = 0;
  const originalPage = feed([entry("news_one", "Cached title")], true);
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      feedCalls += 1;
      if (feedCalls === 1) return { status: 200, data: originalPage, etag: '"feed-v1"' };
      return { status: 304, data: null, etag: '"feed-v1"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  const key = "news:" + content.buildFeedURL();
  const cached = content.session.pages.get(key);
  assert.equal(cached.stale, true);

  await content.refreshFeed(true);
  assert.equal(workspace.requests.at(-1).options.headers["If-None-Match"], '"feed-v1"');
  assert.equal(content.session.pages.get(key).page, originalPage, "304 must retain the exact cached page");
  assert.equal(content.session.pages.get(key).stale, true, "304 must not clear cached stale metadata");
  assert.match(workspace.elements["news-cards"].textContent, /Cached title/);
});

test("a late feed response cannot replace a newer filtered result", async () => {
  const original = deferred();
  const filtered = deferred();
  const workspace = makeWorkspace({
    request: ({ url }) => {
      if (url === "/api/content/status") return Promise.resolve({ sources: [], coverage: {}, notices: [] });
      return url.includes("category=tools") ? filtered.promise : original.promise;
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  content.setFeedFilter("category", "tools");
  filtered.resolve({ status: 200, data: feed([entry("news_new", "Filtered result")]), etag: '"filtered"' });
  await settle();
  assert.match(workspace.elements["news-cards"].textContent, /Filtered result/);

  original.resolve({ status: 200, data: feed([entry("news_old", "Old result")]), etag: '"old"' });
  await settle();
  assert.match(workspace.elements["news-cards"].textContent, /Filtered result/);
  assert.doesNotMatch(workspace.elements["news-cards"].textContent, /Old result/);
});

test("returning to News restarts an invalidated selected detail request", async () => {
  const firstDetail = deferred();
  const resumedDetail = deferred();
  let detailCalls = 0;
  const workspace = makeWorkspace({
    request: ({ url }) => {
      if (url === "/api/content/status") return Promise.resolve({ sources: [], coverage: {}, notices: [] });
      if (url.includes("/api/content/feed")) return Promise.resolve({ status: 200, data: feed([entry("news_retained", "Retained news")]), etag: '"news-feed"' });
      detailCalls += 1;
      return detailCalls === 1 ? firstDetail.promise : resumedDetail.promise;
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  const firstRequest = content.selectEntry("news_retained");
  content.switchWorkspace("skills");
  firstDetail.resolve({ status: 200, data: { entry: entry("news_retained", "Late news"), meta: { stale: false } }, etag: '"detail-old"' });
  await firstRequest;
  assert.equal(content.session.news.entryId, "news_retained");
  assert.equal(content.session.details.has("news_retained"), false, "the invalidated result should not populate detail cache");

  content.switchWorkspace("news");
  assert.equal(detailCalls, 2, "returning to the cached feed should restart the retained detail request");
  resumedDetail.resolve({ status: 200, data: { entry: entry("news_retained", "Resumed news"), meta: { stale: false } }, etag: '"detail-new"' });
  await settle();
  assert.match(workspace.elements["news-detail"].textContent, /Resumed news/);
});

test("returning to Discovery from manage restarts an invalidated selected detail request", async () => {
  const firstDetail = deferred();
  const resumedDetail = deferred();
  let detailCalls = 0;
  const workspace = makeWorkspace({
    request: ({ url }) => {
      if (url === "/api/content/status") return Promise.resolve({ sources: [], coverage: {}, notices: [] });
      if (url.includes("/api/content/feed")) return Promise.resolve({ status: 200, data: feed([entry("project_retained", "Retained project")]), etag: '"project-feed"' });
      detailCalls += 1;
      return detailCalls === 1 ? firstDetail.promise : resumedDetail.promise;
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: false });
  content.setSkillsView("discovery");
  await settle();
  const firstRequest = content.selectEntry("project_retained");
  content.setSkillsView("manage");
  firstDetail.resolve({ status: 200, data: { entry: entry("project_retained", "Late project"), meta: { stale: false } }, etag: '"detail-old"' });
  await firstRequest;
  assert.equal(content.session.project.entryId, "project_retained");
  assert.equal(content.session.details.has("project_retained"), false, "the invalidated result should not populate detail cache");

  content.setSkillsView("discovery");
  assert.equal(detailCalls, 2, "returning to discovery should restart the retained detail request");
  resumedDetail.resolve({ status: 200, data: { entry: entry("project_retained", "Resumed project"), meta: { stale: false } }, etag: '"detail-new"' });
  await settle();
  assert.match(workspace.elements["discovery-detail"].textContent, /Resumed project/);
});

test("late 410 evicts the entry from every cached page without changing the newer selection", async () => {
  const older = deferred();
  const newer = deferred();
  const page = feed([entry("news_one", "Withdrawn"), entry("news_two", "Current")]);
  const workspace = makeWorkspace({
    request: ({ url }) => {
      if (url === "/api/content/status") return Promise.resolve({ sources: [], coverage: {}, notices: [] });
      if (url === "/api/content/feed?kind=news&limit=20&view=selected&window=7d") {
        return Promise.resolve({ status: 200, data: page, etag: '"feed-v1"' });
      }
      if (url.endsWith("/news_one")) return older.promise;
      if (url.endsWith("/news_two")) return newer.promise;
      throw new Error("unexpected request " + url);
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  const feedKey = "news:" + content.buildFeedURL();
  const cached = content.session.pages.get(feedKey);
  cached.pending = { page: feed([entry("news_one", "Pending withdrawn"), entry("news_two", "Current")]), etag: "pending", checkedAt: 1, stale: false };
  content.session.pages.set("other-query", { page: feed([entry("news_one", "Other page")]), checkedAt: 1, stale: false });
  content.session.details.set("news_one", { entry: entry("news_one", "Cached withdrawn"), meta: {}, checkedAt: 1, stale: true });

  const oldSelection = content.selectEntry("news_one");
  const newSelection = content.selectEntry("news_two");
  older.reject(httpError(410));
  await oldSelection;

  assert.equal(content.session.news.entryId, "news_two", "the newer selection must stay selected");
  assert.equal(content.session.details.has("news_one"), false);
  for (const record of content.session.pages.values()) {
    assert.equal(record.page.items.some((item) => item.id === "news_one"), false);
    assert.equal(record.pending?.page.items.some((item) => item.id === "news_one") || false, false);
  }
  assert.doesNotMatch(workspace.elements["news-detail"].textContent, /已撤回/);
  newer.resolve({ status: 200, data: { entry: entry("news_two", "Current"), meta: { stale: false } }, etag: '"detail-v2"' });
  await newSelection;
});

test("404 reports temporary unavailability without labeling an entry withdrawn", async () => {
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("/api/content/feed")) return { status: 200, data: feed([]), etag: "" };
      throw httpError(404);
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  await content.selectEntry("news_missing");
  assert.equal(content.session.news.entryId, "news_missing");
  assert.match(workspace.elements["news-detail"].textContent, /当前不可用/);
  assert.doesNotMatch(workspace.elements["news-detail"].textContent, /撤回/);
});

test("a missing selected card revalidates detail and a detail 304 keeps its stale metadata", async () => {
  let feedCalls = 0;
  let detailCalls = 0;
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("/api/content/feed")) {
        feedCalls += 1;
        return { status: 200, data: feed(feedCalls === 1 ? [entry("news_keep", "Still available")] : []), etag: '"feed-' + feedCalls + '"' };
      }
      detailCalls += 1;
      if (detailCalls === 1) return { status: 200, data: { entry: entry("news_keep", "Still available"), meta: { stale: true } }, etag: '"detail-v1"' };
      return { status: 304, data: null, etag: '"detail-v1"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  await content.selectEntry("news_keep");
  await content.refreshFeed(true);
  await settle();

  assert.equal(detailCalls, 2, "absence from one refreshed page should revalidate the selected detail");
  const detailCall = workspace.requests.filter((call) => call.url.endsWith("/news_keep")).at(-1);
  assert.equal(detailCall.options.headers["If-None-Match"], '"detail-v1"');
  assert.equal(content.session.news.entryId, "news_keep");
  assert.equal(content.session.details.get("news_keep").stale, true);
  assert.match(workspace.elements["news-detail"].textContent, /详情来自缓存/);
  assert.doesNotMatch(workspace.elements["news-detail"].textContent, /撤回/);
});

test("failed detail revalidation exposes effective stale metadata to registered and fallback renderers", async () => {
  const entryStates = [];
  let newsCalls = 0;
  let projectCalls = 0;
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("/api/content/feed")) {
        const item = url.includes("kind=project") ? entry("project_stale", "Project detail") : entry("news_stale", "News detail");
        return { status: 200, data: feed([item]), etag: '"feed"' };
      }
      if (url.endsWith("/news_stale")) {
        newsCalls += 1;
        if (newsCalls === 1) return { status: 200, data: { entry: entry("news_stale", "News detail"), meta: { stale: false } }, etag: '"news-detail"' };
        if (newsCalls === 2) throw httpError(503);
        return { status: 304, data: null, etag: '"news-detail"' };
      }
      projectCalls += 1;
      if (projectCalls === 1) return { status: 200, data: { entry: entry("project_stale", "Project detail"), meta: { stale: false } }, etag: '"project-detail"' };
      if (projectCalls === 2) throw httpError(503);
      return { status: 304, data: null, etag: '"project-detail"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.registerRenderer("news", { entry(item, meta) { entryStates.push({ id: item.id, stale: meta.stale }); } });
  content.onMainStatus({ firstRun: true });
  await settle();

  await content.selectEntry("news_stale");
  await content.selectEntry("news_stale", true);
  assert.deepEqual(entryStates.slice(-2), [
    { id: "news_stale", stale: false },
    { id: "news_stale", stale: true },
  ]);
  const newsCache = content.session.details.get("news_stale");
  assert.equal(newsCache.meta.stale, false, "the cached server metadata should remain unchanged");
  assert.equal(newsCache.stale, true, "transport failure should mark the record effectively stale");
  await content.selectEntry("news_stale", true);
  assert.equal(entryStates.at(-1).stale, true, "a later 304 should retain effective stale metadata");

  content.switchWorkspace("skills");
  content.setSkillsView("discovery");
  await settle();
  await content.selectEntry("project_stale");
  await content.selectEntry("project_stale", true);
  const projectCache = content.session.details.get("project_stale");
  assert.equal(projectCache.meta.stale, false, "fallback rendering must not mutate original metadata");
  assert.equal(projectCache.stale, true);
  assert.match(workspace.elements["discovery-detail"].textContent, /详情来自缓存/);
  await content.selectEntry("project_stale", true);
  assert.match(workspace.elements["discovery-detail"].textContent, /详情来自缓存/, "a 304 after a failure must keep the stale hint");
});

test("same-ID summary changes wait for explicit new-content acceptance", async () => {
  let feedCalls = 0;
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      feedCalls += 1;
      const title = feedCalls === 1 ? "Old title" : "Updated title";
      return { status: 200, data: feed([entry("news_same", title)]), etag: '"feed-' + feedCalls + '"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();

  await content.refreshFeed(true, true);
  assert.match(workspace.elements["news-cards"].textContent, /Old title/, "poll results should not reorder the visible feed");
  assert.doesNotMatch(workspace.elements["news-cards"].textContent, /Updated title/);
  assert.equal(workspace.elements["news-update-hint"].hidden, false);
  workspace.elements["news-update-hint"].click();
  assert.match(workspace.elements["news-cards"].textContent, /Updated title/);
  assert.equal(workspace.elements["news-update-hint"].hidden, true);
});

test("an idle stale startup list adopts the fresh feed without manual acceptance", async () => {
  let ready = false;
  const workspace = makeWorkspace({
    request: async ({ url }) => url === "/api/content/status"
      ? { sources: [], coverage: {}, notices: [] }
      : { status: 200, data: feed([entry("news_same", ready ? "Today's news" : "Old cached news")], !ready), etag: ready ? '"fresh"' : '"stale"' },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  ready = true;

  await content.refreshFeed(true, true);

  assert.match(workspace.elements["news-cards"].textContent, /Today's news/);
  assert.equal(workspace.elements["news-update-hint"].hidden, true);
  assert.equal(content.session.pages.get("news:" + content.buildFeedURL()).stale, false);
});

test("stale startup polling retries promptly and returns to the normal fresh cache interval", async () => {
  let now = 1000;
  let calls = 0;
  const workspace = makeWorkspace({
    now: () => now,
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      calls += 1;
      return { status: 200, data: feed([entry("news_same", calls === 1 ? "Old cached news" : "Today's news")], calls === 1), etag: '"feed-' + calls + '"' };
    },
  });
  workspace.window.SkillManagerContent.onMainStatus({ firstRun: true });
  await settle();
  assert.equal(workspace.intervals[0].delay, 15000);

  now += 15000;
  workspace.intervals[0].callback();
  await settle();
  assert.equal(calls, 2);
  assert.match(workspace.elements["news-cards"].textContent, /Today's news/);

  now += 15000;
  workspace.intervals[0].callback();
  await settle();
  assert.equal(calls, 2, "a fresh feed should not be requested on every short retry tick");
  now += 300000;
  workspace.intervals[0].callback();
  await settle();
  assert.equal(calls, 3);
});

for (const reader of ["selected article", "scrolled list"]) test("stale startup recovery preserves the " + reader + " until accepted", async () => {
  let ready = false;
  const workspace = makeWorkspace({
    request: async ({ url }) => url === "/api/content/status"
      ? { sources: [], coverage: {}, notices: [] }
      : { status: 200, data: feed([entry("news_same", ready ? "Updated news" : "Old cached news")], !ready), etag: ready ? '"fresh"' : '"stale"' },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  if (reader === "selected article") content.session.news.entryId = "news_same";
  else workspace.elements["news-cards"].scrollTop = 80;
  ready = true;

  await content.refreshFeed(true, true);

  assert.match(workspace.elements["news-cards"].textContent, /Old cached news/);
  assert.equal(workspace.elements["news-update-hint"].hidden, false);
  workspace.elements["news-update-hint"].click();
  assert.match(workspace.elements["news-cards"].textContent, /Updated news/);
});

test("status renders human labels and status errors leave management usable", async () => {
  const workspace = makeWorkspace({
    request: async () => ({
      sources: [
        { name: "aihot:news:selected:7d", state: "stale", lastSuccessAt: "2026-10-27T10:00:00Z" },
        { name: "github", state: "fresh", lastSuccessAt: "2026-10-28T10:00:00Z" },
      ],
      coverage: { updated: 5, candidates: 8, completeGrowth: 3 },
      notices: [{ message: "Using cached source data", code: "rate_limited" }, { code: "opaque-machine-code" }],
      stale: true,
    }),
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: false });
  await content.refreshContentStatus();
  const text = workspace.elements["news-status"].textContent;
  assert.match(text, /AI 资讯/);
  assert.match(text, /缓存内容/);
  assert.match(text, /Using cached source data/);
  assert.doesNotMatch(text, /GitHub|覆盖：|2026-10-27T|2026-10-28T/,
    "news status should omit discovery-only coverage and timestamps must be localized");
  assert.doesNotMatch(text, /aihot:news|opaque-machine-code|\[object Object\]/);
  const newsDisclosure = workspace.elements["news-status"].children[0];
  assert.equal(newsDisclosure.tagName, "DETAILS");
  assert.equal(newsDisclosure.open, false, "the compact publisher status begins collapsed");
  assert.equal(newsDisclosure.children[0].tagName, "SUMMARY", "native summary is the disclosure control");
  const newsRegion = newsDisclosure.children[1];
  assert.equal(newsRegion.attributes.role, "region");
  assert.equal(newsRegion.attributes.tabindex, "0", "expanded details can receive keyboard focus for bounded scrolling");

  const discovery = workspace.elements["discovery-status"].textContent;
  assert.match(discovery, /GitHub/);
  assert.match(discovery, /8 个候选/);
  assert.match(discovery, /5 个已更新/);
  assert.match(discovery, /3 个有完整周数据/);
  assert.doesNotMatch(discovery, /AI 资讯/);
  assert.equal(workspace.elements["manage-workspace"].hidden, false);

  const failing = makeWorkspace({ request: async () => { throw new Error("connection refused"); } });
  failing.window.SkillManagerContent.onMainStatus({ firstRun: false });
  await failing.window.SkillManagerContent.refreshContentStatus();
  assert.equal(failing.elements["manage-workspace"].hidden, false);
  assert.match(failing.elements["news-status"].textContent, /暂不可用/);
});

test("status omits impossible timestamps through the shared date validator", async () => {
  const workspace = makeWorkspace({ request: async () => ({
    sources: [{ name: "aihot:news:selected:7d", state: "fresh", lastSuccessAt: "2026-02-31T12:00:00Z" }],
    coverage: {}, notices: [],
  }) });
  await workspace.window.SkillManagerContent.refreshContentStatus();
  const disclosure = workspace.elements["news-status"].children[0];
  assert.equal(disclosure.children[0].children[0].textContent, "AIHOT · 已更新");
  assert.doesNotMatch(workspace.elements["news-status"].textContent, /2026-02-31|更新于/);
  assert.doesNotMatch(disclosure.children[1].textContent, /2026-02-31/);
});

test("status refresh preserves an expanded disclosure, scroll position, and keyboard focus", async () => {
  const workspace = makeWorkspace({ request: async () => ({
    sources: [{ name: "aihot:news:selected:7d", state: "fresh" }],
    coverage: {}, notices: [{ message: "A source notice" }],
  }) });
  const content = workspace.window.SkillManagerContent;
  await content.refreshContentStatus();
  const firstDisclosure = workspace.elements["news-status"].children[0];
  const firstRegion = firstDisclosure.children[1];
  firstDisclosure.open = true;
  firstRegion.scrollTop = 31;
  workspace.document.activeElement = firstRegion;

  await content.refreshContentStatus();
  const updated = workspace.elements["news-status"].children[0];
  assert.equal(updated.open, true);
  assert.equal(updated.children[1].scrollTop, 31);
  assert.equal(workspace.document.activeElement, updated.children[1]);
});

test("foreground polling installs once and refreshes only while content is visible", async () => {
  const workspace = makeWorkspace();
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: false });
  content.onMainStatus({ firstRun: false });
  assert.equal(workspace.intervals.length, 1);
  assert.equal(workspace.intervals[0].delay, 15000);
  assert.equal(workspace.requests.length, 0, "management initialization must not await or fetch content");

  workspace.document.hidden = true;
  workspace.intervals[0].callback();
  await settle();
  assert.equal(workspace.requests.length, 0, "hidden tabs must not poll content");

  content.setSkillsView("discovery");
  workspace.document.hidden = false;
  workspace.listeners.document.visibilitychange();
  await settle();
  assert.equal(workspace.requests.filter((call) => call.url.includes("/api/content/feed")).length, 1);
  assert.equal(workspace.requests.filter((call) => call.url === "/api/content/status").length, 1);

  workspace.listeners.window.focus();
  await settle();
  assert.equal(workspace.requests.filter((call) => call.url.includes("/api/content/feed")).length, 2, "focus should revalidate a visible content surface");
  assert.equal(workspace.requests.filter((call) => call.url === "/api/content/status").length, 2);
});

test("409 cursor expiry restarts the same filtered query without its cursor", async () => {
  let feedCalls = 0;
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      feedCalls += 1;
      if (url.includes("cursor=expired")) throw httpError(409);
      return { status: 200, data: feed([]), etag: '"feed-' + feedCalls + '"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  content.setFeedFilter("category", "tools");
  content.setFeedFilter("q", "agent systems");
  await settle();
  content.setFeedCursor("expired");
  await settle();

  assert.equal(content.session.news.cursor, "");
  assert.equal(content.session.news.cursorStack.length, 0);
  const calls = workspace.requests.filter((call) => call.url.includes("/api/content/feed"));
  assert.ok(calls.some((call) => call.url.includes("cursor=expired")));
  const restart = new URL(calls.at(-1).url, "http://workspace.local").searchParams;
  assert.equal(restart.get("cursor"), null);
  assert.equal(restart.get("category"), "tools");
  assert.equal(restart.get("q"), "agent systems");
  assert.equal(feedCalls, calls.length);
});

test("409 cursor expiry reports failed first-page recovery without claiming a reload", async () => {
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("cursor=expired")) throw httpError(409);
      if (url.includes("kind=news")) throw httpError(503);
      return { status: 200, data: feed([]), etag: '"initial-feed"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  content.setFeedCursor("expired");
  await settle();

  const hint = workspace.elements["news-update-hint"];
  assert.equal(content.session.news.cursor, "");
  assert.match(hint.textContent, /加载失败/);
  assert.doesNotMatch(hint.textContent, /重新加载/);
});

test("scroll restoration uses each content-cards element as the scroll owner", async () => {
  const workspace = makeWorkspace();
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  workspace.elements["news-cards"].scrollTop = 14;
  workspace.elements["news-list"].scrollTop = 900;
  content.switchWorkspace("skills");
  content.setSkillsView("discovery");
  workspace.elements["discovery-cards"].scrollTop = 27;
  workspace.elements["discovery-list"].scrollTop = 800;
  content.setSkillsView("manage");
  workspace.elements["discovery-cards"].scrollTop = 0;
  content.setSkillsView("discovery");
  assert.equal(workspace.elements["discovery-cards"].scrollTop, 27);
  assert.equal(workspace.elements["discovery-list"].scrollTop, 800, "the non-scrolling list wrapper should not be rewritten");
  workspace.elements["news-cards"].scrollTop = 0;
  content.switchWorkspace("news");
  assert.equal(workspace.elements["news-cards"].scrollTop, 14);
  assert.equal(workspace.elements["news-list"].scrollTop, 900, "the non-scrolling list wrapper should not be rewritten");
});

test("feed cache is reused before five minutes and revalidated at its expiry", async () => {
  let now = 1000;
  let feedCalls = 0;
  const workspace = makeWorkspace({
    now: () => now,
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      feedCalls += 1;
      return { status: 200, data: feed([entry("news_one", "Cached")]), etag: '"feed-' + feedCalls + '"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  content.switchWorkspace("skills");
  content.switchWorkspace("news");
  await settle();
  assert.equal(feedCalls, 1, "fresh page cache should be reused");

  now += 300000;
  content.switchWorkspace("skills");
  content.switchWorkspace("news");
  await settle();
  assert.equal(feedCalls, 2, "page cache should revalidate after five minutes");
});


test("status keeps every relevant checkpoint and notice in a collapsed keyboard-scroll disclosure", async () => {
  const sources = [{ name: "github", state: "fresh" }, { name: "aihot:news:selected:7d", state: "fresh" }, ...Array.from({ length: 40 }, (_, i) => ({ name: "aihot:detail:daily_" + i, state: "stale" }))];
  const notices = [...Array.from({ length: 25 }, (_, i) => ({ message: "Notice " + i })), { message: "Notice 0" }];
  const workspace = makeWorkspace({ request: async () => ({ sources, coverage: { candidates: 4, updated: 3, completeGrowth: 2 }, notices }) });
  await workspace.window.SkillManagerContent.refreshContentStatus();
  const root = workspace.elements["news-status"];
  const disclosure = root.children[0];
  assert.equal(root.children.length, 1, "the default status occupies one compact row");
  assert.equal(disclosure.tagName, "DETAILS");
  assert.equal(disclosure.open, false);
  const region = disclosure.children[1];
  assert.equal(region.attributes.role, "region");
  assert.equal(region.attributes.tabindex, "0");
  assert.match(root.textContent, /AI 资讯/);
  assert.match(root.textContent, /AIHOT 详情 · daily_0/);
  assert.match(root.textContent, /AIHOT 详情 · daily_39/);
  assert.match(root.textContent, /Notice 24/);
  assert.equal(region.children.filter((child) => child.textContent === "Notice 0").length, 1, "duplicate notices should be collapsed");
  assert.doesNotMatch(root.textContent, /GitHub|覆盖：|aihot:detail/);

  const discovery = workspace.elements["discovery-status"].textContent;
  assert.match(discovery, /GitHub 项目/);
  assert.match(discovery, /4 个候选/);
  assert.match(discovery, /3 个已更新/);
  assert.doesNotMatch(discovery, /AI 资讯|daily_/);
});

test("GitHub stale checkpoint without a successful timestamp is not labeled as cached data", async () => {
  const status = { sources: [{ name: "github", state: "stale" }], coverage: {}, notices: [] };
  const workspace = makeWorkspace({ request: async () => status });
  await workspace.window.SkillManagerContent.refreshContentStatus();
  assert.match(workspace.elements["discovery-status"].textContent, /尚无可用数据/);
  assert.doesNotMatch(workspace.elements["discovery-status"].textContent, /缓存内容/);

  status.sources[0].lastSuccessAt = "2026-10-28T09:00:00Z";
  await workspace.window.SkillManagerContent.refreshContentStatus();
  assert.match(workspace.elements["discovery-status"].textContent, /缓存内容/);

  status.sources[0].state = "unavailable";
  await workspace.window.SkillManagerContent.refreshContentStatus();
  assert.match(workspace.elements["discovery-status"].textContent, /暂不可用/);
});
