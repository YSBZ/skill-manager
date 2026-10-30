"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");
const vm = require("node:vm");

const root = __dirname;
const lifecycleSource = fs.readFileSync(path.join(root, "content-workspace.js"), "utf8");
const contentUIPath = path.join(root, "content-ui.js");
const contentUISource = fs.existsSync(contentUIPath) ? fs.readFileSync(contentUIPath, "utf8") : "";
const newsPath = path.join(root, "content-news.js");
const newsSource = fs.existsSync(newsPath) ? fs.readFileSync(newsPath, "utf8") : null;
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
    scrollTop: 0,
    addEventListener(name, handler) { this.listeners[name] = handler; },
    setAttribute(name, value) { this.attributes[name] = String(value); },
    append(...nodes) { this.children.push(...nodes); nodes.forEach((node) => { node.parent = this; }); },
    replaceChildren(...nodes) { ownText = ""; this.children = []; this.append(...nodes); },
    contains(target) { return this.navButtons ? this.navButtons.includes(target) : this.children.includes(target); },
    remove() { if (this.parent) this.parent.children = this.parent.children.filter((child) => child !== this); },
    click() {
      const event = {
        target: this,
        currentTarget: this,
        bubbles: true,
        propagationStopped: false,
        preventDefault() {},
        stopPropagation() { this.propagationStopped = true; },
      };
      if (typeof this.onclick === "function") this.onclick(event);
      let current = this;
      while (current) {
        event.currentTarget = current;
        if (current.listeners.click) current.listeners.click(event);
        if (event.propagationStopped) break;
        current = current.parent;
      }
    },
    get textContent() { return ownText + this.children.map((child) => child.textContent || "").join(""); },
    set textContent(value) { ownText = String(value ?? ""); this.children = []; },
  };
}

function makeWorkspace(options = {}) {
  assert.ok(newsSource, "the dedicated AI news renderer should be present");
  const ids = [
    "workspace-switcher", "workspace-news", "workspace-discovery", "workspace-manage", "news-workspace", "skills-workspace", "discovery-workspace", "manage-workspace",
    "return-article-control", "news-list", "news-cards", "news-detail", "news-update-hint",
    "news-meta-notices", "news-status", "discovery-list", "discovery-cards", "discovery-detail", "discovery-update-hint",
    "discovery-meta-notices", "discovery-status", "news-view-nav", "news-window-nav", "news-filter-form",
    "news-search-query", "news-category-filter", "news-window-filter", "news-query-submit", "news-category-options",
  ];
  const elements = Object.fromEntries(ids.map((id) => [id, makeElement(id, ["news-workspace", "discovery-workspace", "news-update-hint", "discovery-update-hint", "return-article-control"].includes(id))]));
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
      if (selector === "[data-entry-id]") return [elements["news-cards"], elements["discovery-cards"]].flatMap((parent) => parent.children.filter((child) => child.dataset.entryId));
      return [];
    },
  };
  const window = {
    addEventListener(name, handler) { listeners.window[name] = handler; },
    requestAnimationFrame(callback) { callback(); },
    setInterval(callback, delay) { intervals.push({ callback, delay }); return intervals.length; },
    SkillManagerAPI: {
      async request(method, url, body, requestOptions = {}) {
        const call = { method, url, body, options: requestOptions };
        requests.push(call);
        if (options.request) return options.request(call);
        if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
        return { status: 200, data: { items: [], nextCursor: "", meta: { stale: false, notices: [] } }, etag: "" };
      },
      openExternal(url) { externalURLs.push(url); },
      async searchSkills(term) { searchTerms.push(term); window.SkillManagerContent.showMarketSearch(); },
    },
  };
  const requests = [];
  const externalURLs = [];
  const searchTerms = [];
  const localStorage = { getItem() { return null; }, setItem() {} };
  class FakeDate extends Date { static now() { return options.now ? options.now() : 1_000_000; } }
  const context = { document, window, localStorage, Date: FakeDate, URL, URLSearchParams, encodeURIComponent };
  vm.runInNewContext(lifecycleSource, context);
  vm.runInNewContext(contentUISource, context);
  vm.runInNewContext(newsSource, context);
  return { elements, window, document, listeners, intervals, requests, externalURLs, searchTerms, newsViews, newsWindows, skillViews };
}

function feed(items, nextCursor = "") {
  return { items, nextCursor, meta: { stale: false, notices: [], sources: [], coverage: {} } };
}

function item(id, title, extra = {}) {
  return {
    id, kind: id.startsWith("daily_") || id.startsWith("weekly_") || id.startsWith("monthly_") ? id.split("_")[0] : id.startsWith("topic_") ? "topic" : "news",
    title, summary: "Summary for " + title, category: "agents", source: { name: "Publisher" }, attribution: { name: "AIHOT" }, paragraphs: [], ...extra,
  };
}

async function settle() {
  await new Promise((resolve) => setImmediate(resolve));
  await Promise.resolve();
}

function buttons(node) {
  return descendants(node).filter((child) => child.tagName === "BUTTON");
}

function descendants(node) {
  return (node.children || []).flatMap((child) => [child, ...descendants(child)]);
}

test("index wires the real news controls and loads the renderer before the manager", () => {
  for (const id of ["news-rail", "news-view-nav", "news-window-nav", "news-filter-form", "news-search-query", "news-category-filter", "news-detail"]) {
    assert.match(indexSource, new RegExp(`id="${id}"`), `${id} should be present in the real workspace`);
  }
  const lifecycle = indexSource.indexOf('src="/content-workspace.js');
  const renderer = indexSource.indexOf('src="/content-news.js');
  const app = indexSource.indexOf('src="/app.js');
  assert.ok(lifecycle >= 0 && renderer > lifecycle && app > renderer, "lifecycle, news renderer, then manager script order is required");

  const listStart = indexSource.indexOf('<section id="news-list"');
  const railStart = indexSource.indexOf('<aside id="news-rail"');
  const headerStart = indexSource.indexOf('class="content-pane-header news-pane-header"');
  const listEnd = indexSource.indexOf("</section>", listStart);
  assert.ok(listStart >= 0 && railStart > listStart && railStart < headerStart && headerStart < listEnd,
    "the compact news category navigation should live at the top of the directory, before its title and filters");
  assert.match(indexSource, /<div id="news-status" class="content-status" role="status" aria-live="polite"><\/div>/,
    "the publisher status summary should retain its live status semantics");
  assert.match(fs.readFileSync(path.join(root, "content-workspace.css"), "utf8"),
    /\.news-columns\s*\{\s*grid-template-columns:\s*minmax\(300px,\s*340px\)\s+minmax\(0,\s*1fr\)/,
    "desktop news should use a compact directory and a flexible reader column");
});

test("shared content UI helper validates dates and external HTTP links for renderers", () => {
  const helperWindow = {};
  vm.runInNewContext(contentUISource, { window: helperWindow, URL });
  assert.equal(typeof helperWindow.SkillManagerContentUI?.parseDate, "function");
  assert.equal(typeof helperWindow.SkillManagerContentUI?.safeHTTPURL, "function");
  assert.equal(helperWindow.SkillManagerContentUI.parseDate("2026-02-31T12:00:00Z"), null);
  assert.equal(helperWindow.SkillManagerContentUI.parseDate("2026-10-28T12:00:00Z").toISOString(), "2026-10-28T12:00:00.000Z");
  assert.equal(helperWindow.SkillManagerContentUI.safeHTTPURL("javascript:alert(1)"), "");
  assert.equal(helperWindow.SkillManagerContentUI.safeHTTPURL("https://user:pass@example.test/"), "");
  assert.equal(helperWindow.SkillManagerContentUI.safeHTTPURL("https://example.test/story"), "https://example.test/story");
});

test("news sections map to the content contract and search/category/window filters reset pagination", async () => {
  const workspace = makeWorkspace({
    request: async ({ url }) => url === "/api/content/status" ? { sources: [], coverage: {}, notices: [] } : { status: 200, data: feed([item("news_one", "News")], "cursor-two"), etag: '"feed"' },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  let query = new URL(workspace.requests.filter((call) => call.url.includes("/api/content/feed")).at(-1).url, "http://local").searchParams;
  assert.equal(query.get("kind"), "news");
  assert.equal(query.get("view"), "selected");
  assert.equal(query.get("window"), "7d");

  workspace.elements["news-search-query"].value = "agent runtime";
  workspace.elements["news-category-filter"].value = "agents";
  workspace.elements["news-filter-form"].listeners.submit({ preventDefault() {} });
  await settle();
  workspace.newsWindows.find((button) => button.dataset.newsWindow === "24h").click();
  await settle();
  query = new URL(workspace.requests.filter((call) => call.url.includes("/api/content/feed")).at(-1).url, "http://local").searchParams;
  assert.equal(query.get("q"), "agent runtime");
  assert.equal(query.get("category"), "agents");
  assert.equal(query.get("window"), "24h");

  workspace.newsViews.find((button) => button.dataset.newsView === "all").click();
  await settle();
  query = new URL(workspace.requests.filter((call) => call.url.includes("/api/content/feed")).at(-1).url, "http://local").searchParams;
  assert.equal(query.get("view"), "all");
  assert.equal(query.get("window"), "24h");

  for (const kind of ["topic", "daily", "weekly", "monthly"]) {
    workspace.newsViews.find((button) => button.dataset.newsView === kind).click();
    await settle();
    query = new URL(workspace.requests.filter((call) => call.url.includes("/api/content/feed")).at(-1).url, "http://local").searchParams;
    assert.equal(query.get("kind"), kind);
    assert.equal(query.has("view"), false, `${kind} does not accept a news view`);
    assert.equal(query.has("window"), false, `${kind} does not accept a news window`);
  }
  workspace.newsViews.find((button) => button.dataset.newsView === "selected").click();
  await settle();
  assert.equal(content.session.news.cursor, "");
  assert.deepEqual(Array.from(content.session.news.cursorStack), []);
  assert.equal(content.session.news.entryId, "");
});

test("news cards and structured details render safe text, valid metadata, and available source actions", async () => {
  const story = item("news_story", "<img src=x onerror=alert(1)>", {
    selected: true, reason: "Recommended for its release notes", publishedAt: "2026-10-28T11:30:00Z", discoveredAt: "invalid-date",
    canonicalUrl: "https://aihot.news/read/story", originalUrl: "javascript:alert(1)",
    source: { name: "Publisher", url: "https://publisher.example" }, attribution: { name: "AIHOT", url: "https://aihot.news" },
    paragraphs: [{
      heading: "Event summary", text: "Plain upstream text <b>stays text</b>", url: "https://publisher.example/story",
      source: { name: "Desk", url: "javascript:alert(2)" }, attribution: { name: "AIHOT", url: "https://aihot.news" },
    }, { heading: "No source link", text: "No external action should be invented.", source: { name: "Unlinked publisher" } }],
  });
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("/api/content/feed")) return { status: 200, data: feed([story]), etag: '"feed"' };
      return { status: 200, data: { entry: story, meta: { stale: false, notices: [{ message: "Source updated with a delay" }, { code: "opaque-machine-code", credential: "hidden" }] } }, etag: '"entry"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  const card = buttons(workspace.elements["news-cards"])[0];
  assert.equal(card.dataset.entryId, "news_story");
  assert.equal(card.attributes["aria-pressed"], "false");
  assert.match(card.textContent, /<img src=x onerror=alert\(1\)>/);
  assert.doesNotMatch(card.textContent, /Summary for|Recommended for its release notes/, "directory rows should not repeat article prose");
  assert.doesNotMatch(card.textContent, /2026-10-28T/, "directory dates should be localized, never raw ISO text");
  assert.equal(card.children.some((child) => child.tagName === "IMG"), false, "upstream markup must stay text");
  card.click();
  assert.equal(card.attributes["aria-pressed"], "true", "the selected card should reflect the selected detail");
  await settle();

  const detail = workspace.elements["news-detail"];
  assert.match(detail.textContent, /AIHOT 精选/);
  assert.match(detail.textContent, /推荐理由：Recommended for its release notes/);
  assert.match(detail.textContent, /来源：Publisher/);
  assert.match(detail.textContent, /归属：AIHOT/);
  assert.match(detail.textContent, /发布时间：/);
  assert.doesNotMatch(detail.textContent, /收录时间：/);
  assert.doesNotMatch(detail.textContent, /报刊期号/);
  assert.match(detail.textContent, /Event summary/);
  assert.match(detail.textContent, /Plain upstream text <b>stays text/);
  assert.match(detail.textContent, /Source updated with a delay/);
  assert.doesNotMatch(detail.textContent, /opaque-machine-code|hidden/);
  const sourceDisclosure = descendants(detail).find((child) => child.tagName === "DETAILS");
  assert.ok(sourceDisclosure, "secondary source facts should use native disclosure semantics");
  assert.equal(sourceDisclosure.open, false, "source facts should start collapsed");
  assert.equal(sourceDisclosure.children[0].tagName, "SUMMARY", "the first child must be a native, keyboard-operable summary");
  const sourceRegion = sourceDisclosure.children[1];
  assert.equal(sourceRegion.attributes.tabindex, "0", "expanded long source facts should have a keyboard scroll target");
  assert.equal(sourceRegion.attributes.role, "region");
  const firstBody = descendants(detail).find((child) => child.className === "news-paragraph");
  assert.ok(firstBody && detail.children.indexOf(firstBody) < detail.children.indexOf(sourceDisclosure),
    "structured reading should begin before secondary source facts");
  assert.deepEqual(Array.from(content.session.metaNotices).map((notice) => notice.code || notice.message), ["Source updated with a delay", "opaque-machine-code"]);
  const actionButtons = buttons(detail);
  assert.ok(actionButtons.some((button) => button.textContent === "AIHOT 阅读页"));
  const primaryActions = detail.children.find((child) => child.className === "news-detail-actions");
  assert.ok(primaryActions && actionButtons.filter((button) => button.textContent === "AIHOT 阅读页" || button.textContent === "原文阅读全文")
    .some((button) => button.parent === primaryActions), "primary reading links should remain available above the body");
  assert.equal(actionButtons.filter((button) => button.textContent === "打开段落来源").length, 1, "missing or unsafe paragraph URLs are omitted");
  const secondaryActions = sourceRegion.children.find((child) => child.className === "news-detail-actions");
  assert.ok(actionButtons.some((button) => button.textContent === "来源主页" && button.parent === secondaryActions));
  assert.ok(actionButtons.some((button) => button.textContent === "归属来源" && button.parent === secondaryActions));
  assert.equal(actionButtons.some((button) => button.textContent === "原文阅读全文"), false, "unsafe URLs are omitted");
  actionButtons.find((button) => button.textContent === "AIHOT 阅读页").click();
  assert.deepEqual(workspace.externalURLs, ["https://aihot.news/read/story"]);
});

test("report details keep the period and render sections while omitting impossible dates", async () => {
  const report = item("monthly_2026-10", "October report", {
    period: "2026-10", publishedAt: "2026-02-31T12:00:00Z", discoveredAt: "2026-10-28T09:15:00-07:00",
    canonicalUrl: "https://aihot.news/read/monthly-2026-10",
    paragraphs: [{ heading: "Model releases", text: "Release summary", source: { name: "Research desk" }, attribution: { name: "AIHOT Digest" } }],
  });
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("/api/content/feed")) return { status: 200, data: feed([report]), etag: '"report"' };
      return { status: 200, data: { entry: report, meta: { stale: false, notices: [] } }, etag: '"report-detail"' };
    },
  });
  workspace.window.SkillManagerContent.onMainStatus({ firstRun: true });
  await settle();
  workspace.newsViews.find((button) => button.dataset.newsView === "monthly").click();
  await settle();
  assert.equal(workspace.elements["news-window-nav"].hidden, true, "report views do not expose a news time window");
  const reportCard = buttons(workspace.elements["news-cards"]).find((button) => button.dataset.entryId === report.id);
  assert.match(reportCard.textContent, /2026-10/, "report directory rows should show their period");
  assert.doesNotMatch(reportCard.textContent, /2026-02-31|2026-10-28T/, "invalid report dates must stay omitted");
  assert.doesNotMatch(reportCard.textContent, /Summary for/, "report summaries remain in the reader, not the directory");
  reportCard.click();
  await settle();
  assert.match(workspace.elements["news-detail"].textContent, /报刊期号：2026-10/);
  assert.match(workspace.elements["news-detail"].textContent, /收录时间：/);
  assert.doesNotMatch(workspace.elements["news-detail"].textContent, /发布时间：/);
  assert.match(workspace.elements["news-detail"].textContent, /Model releases/);
  assert.match(workspace.elements["news-detail"].textContent, /Release summary/);
});

test("news detail keeps 404 unavailable separate from a 410 withdrawal", async () => {
  const makeFailureWorkspace = (status) => makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("/api/content/feed")) return { status: 200, data: feed([]), etag: '"empty"' };
      throw Object.assign(new Error("HTTP " + status), { status });
    },
  });
  const missing = makeFailureWorkspace(404);
  missing.window.SkillManagerContent.onMainStatus({ firstRun: true });
  await settle();
  await missing.window.SkillManagerContent.selectEntry("news_missing");
  assert.match(missing.elements["news-detail"].textContent, /当前不可用/);
  assert.doesNotMatch(missing.elements["news-detail"].textContent, /撤回/);

  const withdrawn = makeFailureWorkspace(410);
  withdrawn.window.SkillManagerContent.onMainStatus({ firstRun: true });
  await settle();
  await withdrawn.window.SkillManagerContent.selectEntry("news_withdrawn");
  assert.match(withdrawn.elements["news-detail"].textContent, /已撤回/);
});

test("failed detail revalidation keeps the cached article visible with a stale label", async () => {
  const story = item("news_stale", "Cached story");
  let detailCalls = 0;
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("/api/content/feed")) return { status: 200, data: feed([story]), etag: '"feed"' };
      detailCalls += 1;
      if (detailCalls > 1) throw Object.assign(new Error("temporary upstream error"), { status: 503 });
      return { status: 200, data: { entry: story, meta: { stale: false, notices: [] } }, etag: '"detail"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  await content.selectEntry(story.id);
  await content.selectEntry(story.id, true);
  assert.match(workspace.elements["news-detail"].textContent, /详情来自缓存/);
  assert.match(workspace.elements["news-detail"].textContent, /Cached story/);
  assert.equal(content.session.details.get(story.id).meta.stale, false);
  assert.equal(content.session.details.get(story.id).stale, true);
});

test("related Skill search returns through discovery and manage with exact article query and scroll", async () => {
  const article = item("news_return", "Return article", { canonicalUrl: "https://aihot.news/read/return" });
  const project = item("project_result", "Related project");
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("kind=project")) return { status: 200, data: feed([project]), etag: '"project"' };
      if (url.includes("/api/content/feed")) return { status: 200, data: feed([article], "article-next"), etag: '"article"' };
      const id = decodeURIComponent(url.split("/").at(-1));
      return { status: 200, data: { entry: id === article.id ? article : project, meta: { stale: false, notices: [] } }, etag: '"detail"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  content.setFeedFilters({ q: "release notes", category: "agents", window: "24h" });
  await settle();
  content.setFeedCursor("cursor-one");
  await settle();
  content.session.news.cursorStack.push("cursor-parent");
  const list = workspace.elements["news-cards"];
  list.scrollTop = 438;
  await content.selectEntry(article.id);
  await settle();
  const queryInput = buttons(workspace.elements["news-detail"]).find((button) => button.textContent === "查找相关 Skill");
  assert.ok(queryInput, "article detail should offer related Skill search");
  const keyword = descendants(workspace.elements["news-detail"]).find((child) => child.tagName === "INPUT");
  keyword.value = "editable ai agent tools";
  queryInput.click();
  await settle();

  assert.deepEqual(Array.from(workspace.searchTerms), ["editable ai agent tools"]);
  assert.equal(workspace.elements["skills-workspace"].hidden, false);
  assert.equal(workspace.elements["manage-workspace"].hidden, false);
  assert.equal(workspace.elements["return-article-control"].hidden, false);
  const saved = content.session.returnArticle;
  assert.equal(saved.id, article.id);
  assert.deepEqual(JSON.parse(JSON.stringify(saved.query)), {
    kind: "news", view: "selected", window: "24h", q: "release notes", category: "agents", cursor: "cursor-one", cursorStack: ["", "cursor-parent"], entryId: article.id,
  });
  assert.equal(saved.scrollTop, 438);
  assert.notEqual(saved.query.cursorStack, content.session.news.cursorStack);

  workspace.skillViews.find((button) => button.dataset.skillsView === "discovery").click();
  await settle();
  assert.equal(workspace.elements["discovery-workspace"].hidden, false, "the delegated Discovery transition should run");
  assert.equal(workspace.elements["manage-workspace"].hidden, true);
  assert.equal(content.session.returnArticle, saved, "Discovery must retain the original article snapshot");
  assert.equal(workspace.elements["return-article-control"].hidden, false, "the return control stays visible in Discovery");
  workspace.skillViews.find((button) => button.dataset.skillsView === "manage").click();
  await settle();
  assert.equal(workspace.elements["discovery-workspace"].hidden, true);
  assert.equal(workspace.elements["manage-workspace"].hidden, false, "the delegated manage transition should run before return");
  assert.equal(content.session.returnArticle, saved, "manage must retain the original article snapshot");
  assert.equal(workspace.elements["return-article-control"].hidden, false);
  workspace.elements["return-article-control"].click();
  await settle();
  assert.equal(content.session.returnArticle, null);
  assert.equal(workspace.elements["return-article-control"].hidden, true);
  assert.equal(workspace.elements["news-workspace"].hidden, false);
  assert.equal(content.session.news.entryId, article.id);
  assert.equal(content.session.news.q, "release notes");
  assert.equal(content.session.news.category, "agents");
  assert.equal(content.session.news.cursor, "cursor-one");
  assert.equal(workspace.elements["news-cards"].scrollTop, 438);
  assert.match(workspace.elements["news-detail"].textContent, /Return article/);
});

test("a new related search from another article replaces the retained return context", async () => {
  const first = item("news_first", "First article");
  const second = item("news_second", "Second article");
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("/api/content/feed")) return { status: 200, data: feed([first, second]), etag: '"two-articles"' };
      const id = decodeURIComponent(url.split("/").at(-1));
      return { status: 200, data: { entry: id === first.id ? first : second, meta: { stale: false, notices: [] } }, etag: '"detail"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  await content.selectEntry(first.id);
  await settle();
  buttons(workspace.elements["news-detail"]).find((button) => button.textContent === "查找相关 Skill").click();
  await settle();
  assert.equal(content.session.returnArticle.id, first.id);

  workspace.elements["workspace-news"].click();
  await settle();
  workspace.elements["news-cards"].scrollTop = 91;
  buttons(workspace.elements["news-cards"]).find((button) => button.dataset.entryId === second.id).click();
  await settle();
  workspace.elements["news-cards"].scrollTop = 127;
  buttons(workspace.elements["news-detail"]).find((button) => button.textContent === "查找相关 Skill").click();
  await settle();

  assert.equal(content.session.returnArticle.id, second.id);
  assert.equal(content.session.returnArticle.query.entryId, second.id);
  assert.equal(content.session.returnArticle.scrollTop, 127);
});

test("same-query refresh retains selected detail and scroll until pending content is applied", async () => {
  const oldItem = item("news_pending", "Published title");
  const refreshed = item("news_pending", "Revised title");
  let feedCalls = 0;
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      if (url.includes("/api/content/feed")) {
        feedCalls += 1;
        return { status: 200, data: feed([feedCalls === 1 ? oldItem : refreshed]), etag: '"feed-' + feedCalls + '"' };
      }
      return { status: 200, data: { entry: oldItem, meta: { stale: false, notices: [] } }, etag: '"detail"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  await content.selectEntry(oldItem.id);
  await settle();
  workspace.elements["news-cards"].scrollTop = 202;

  await content.refreshFeed(true, true);
  assert.equal(workspace.elements["news-update-hint"].hidden, false);
  assert.match(workspace.elements["news-cards"].textContent, /Published title/);
  assert.doesNotMatch(workspace.elements["news-cards"].textContent, /Revised title/);
  assert.equal(workspace.elements["news-cards"].scrollTop, 202);
  assert.equal(content.session.news.entryId, oldItem.id);
  assert.match(workspace.elements["news-detail"].textContent, /Published title/);

  workspace.elements["news-update-hint"].click();
  assert.match(workspace.elements["news-cards"].textContent, /Revised title/);
  assert.equal(workspace.elements["news-cards"].scrollTop, 202);
  assert.equal(buttons(workspace.elements["news-cards"]).find((button) => button.dataset.entryId === oldItem.id).attributes["aria-pressed"], "true");
  assert.match(workspace.elements["news-detail"].textContent, /Published title/);
});

test("a feed refresh does not overwrite a filter value while the user is editing it", async () => {
  const workspace = makeWorkspace({
    request: async ({ url }) => url === "/api/content/status"
      ? { sources: [], coverage: {}, notices: [] }
      : { status: 200, data: feed([item("news_editing", "Editing safe")]), etag: '"same"' },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  const search = workspace.elements["news-search-query"];
  workspace.document.activeElement = search;
  search.value = "unfinished query";
  await content.refreshFeed(true);
  assert.equal(search.value, "unfinished query");
});

test("pagination controls follow server cursors and reset only the list scroll on page changes", async () => {
  const workspace = makeWorkspace({
    request: async ({ url }) => {
      if (url === "/api/content/status") return { sources: [], coverage: {}, notices: [] };
      const secondPage = url.includes("cursor=cursor-two");
      return { status: 200, data: feed([item(secondPage ? "news_two" : "news_one", secondPage ? "Second page" : "First page")], secondPage ? "" : "cursor-two"), etag: '"' + (secondPage ? "two" : "one") + '"' };
    },
  });
  const content = workspace.window.SkillManagerContent;
  content.onMainStatus({ firstRun: true });
  await settle();
  workspace.elements["news-cards"].scrollTop = 255;
  buttons(workspace.elements["news-cards"]).find((button) => button.textContent === "下一页").click();
  await settle();
  assert.equal(content.session.news.cursor, "cursor-two");
  assert.equal(content.session.news.cursorStack.length, 1);
  assert.equal(workspace.elements["news-cards"].scrollTop, 0);
  assert.match(workspace.elements["news-cards"].textContent, /Second page/);
  buttons(workspace.elements["news-cards"]).find((button) => button.textContent === "上一页").click();
  await settle();
  assert.equal(content.session.news.cursor, "");
  assert.equal(content.session.news.cursorStack.length, 0);
  assert.equal(workspace.elements["news-cards"].scrollTop, 0);
});
