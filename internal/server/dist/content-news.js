"use strict";

(() => {
  const content = window.SkillManagerContent;
  const contentUI = window.SkillManagerContentUI;
  const byId = (id) => document.getElementById(id);
  const entryIDPattern = /^[A-Za-z0-9_-]{1,160}$/;
  const safeHTTPURL = contentUI.safeHTTPURL;
  const validDate = contentUI.parseDate;

  function text(parent, tag, value, className) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    node.textContent = String(value == null ? "" : value);
    parent.append(node);
    return node;
  }

  function sourceIcon(source) {
    const icon = document.createElement("span");
    icon.className = "content-icon content-icon-" + contentUI.sourceIconName(source);
    icon.setAttribute("aria-hidden", "true");
    return icon;
  }

  function externalButton(parent, label, rawURL) {
    const url = safeHTTPURL(rawURL);
    if (!url || !window.SkillManagerAPI || typeof window.SkillManagerAPI.openExternal !== "function") return null;
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = label;
    button.addEventListener("click", () => window.SkillManagerAPI.openExternal(url));
    parent.append(button);
    return button;
  }

  function textValue(value) {
    return typeof value === "string" ? value.trim() : "";
  }

  function dateLabel(date, includeTime = false) {
    if (!date) return "";
    return includeTime
      ? date.toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })
      : date.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
  }

  function showNotices(parent, notices) {
    if (!Array.isArray(notices)) return;
    notices.forEach((notice) => {
      if (notice && typeof notice === "object" && typeof notice.message === "string" && notice.message.trim()) {
        text(parent, "p", notice.message, "content-notice");
      }
    });
  }

  function updateControls(query) {
    const selectedView = query.kind === "news" ? (query.view || "selected") : query.kind;
    document.querySelectorAll("[data-news-view]").forEach((button) => {
      button.setAttribute("aria-pressed", String(button.dataset.newsView === selectedView));
    });
    document.querySelectorAll("[data-news-window]").forEach((button) => {
      button.setAttribute("aria-pressed", String(query.kind === "news" && button.dataset.newsWindow === query.window));
    });
    byId("news-window-nav").hidden = query.kind !== "news";
    const search = byId("news-search-query");
    const category = byId("news-category-filter");
    if (document.activeElement !== search) search.value = query.q || "";
    if (document.activeElement !== category) category.value = query.category || "";
  }

  function updateCategories(items, selected) {
    const categories = new Set();
    (items || []).forEach((item) => {
      const category = textValue(item && item.category);
      if (category) categories.add(category);
    });
    if (selected) categories.add(selected);
    const select = byId("news-category-filter");
    const all = document.createElement("option");
    all.value = "";
    all.textContent = "全部分类";
    const options = [all, ...Array.from(categories).sort((left, right) => left.localeCompare(right)).map((category) => {
      const option = document.createElement("option");
      option.value = category;
      option.textContent = category;
      return option;
    })];
    select.replaceChildren(...options);
    select.value = selected || "";
  }

  function renderPageControls(parent, page, context) {
    const controls = document.createElement("nav");
    controls.className = "content-pagination";
    controls.setAttribute("aria-label", "资讯分页");
    const previous = document.createElement("button");
    previous.type = "button";
    previous.textContent = "上一页";
    previous.disabled = !context.query.cursorStack.length;
    previous.addEventListener("click", () => {
      if (previous.disabled) return;
      byId("news-cards").scrollTop = 0;
      content.previousFeedPage();
    });
    const next = document.createElement("button");
    next.type = "button";
    next.textContent = "下一页";
    next.disabled = !page.nextCursor;
    next.addEventListener("click", () => {
      if (next.disabled) return;
      byId("news-cards").scrollTop = 0;
      content.nextFeedPage();
    });
    controls.append(previous, next);
    parent.append(controls);
  }

  function renderFeed(page, stale, context) {
    const cards = byId("news-cards");
    const notices = byId("news-meta-notices");
    const scrollTop = cards.scrollTop;
    const items = Array.isArray(page && page.items) ? page.items : [];
    const meta = page && page.meta ? page.meta : {};
    content.session.pageNotices = Array.isArray(meta.notices) ? meta.notices : [];
    cards.replaceChildren();
    notices.replaceChildren();
    notices.setAttribute("role", "region");
    notices.setAttribute("aria-label", "列表来源通知");
    notices.setAttribute("tabindex", "0");
    updateControls(context.query);
    if (document.activeElement !== byId("news-category-filter")) updateCategories(items, context.query.category);
    if (stale || meta.stale) text(notices, "p", "当前使用缓存内容。", "content-notice");
    showNotices(notices, content.session.pageNotices);

    items.forEach((item) => {
      if (!item || !entryIDPattern.test(String(item.id || ""))) return;
      const card = document.createElement("button");
      card.type = "button";
      card.className = "content-card";
      card.dataset.entryId = item.id;
      card.setAttribute("aria-pressed", String(item.id === context.selectedId));
      if (item.selected) text(card, "small", "AIHOT 精选", "content-card-selected");
      text(card, "strong", textValue(item.title) || "无标题");
      const detail = [item.source && textValue(item.source.name)];
      const period = textValue(item.period);
      if (period) detail.push("期号 " + period);
      const publishedAt = validDate(item.publishedAt);
      const discoveredAt = validDate(item.discoveredAt);
      const date = publishedAt || discoveredAt;
      if (date) detail.push((publishedAt ? "发布于 " : "收录于 ") + dateLabel(date));
      const byline = detail.filter(Boolean);
      if (byline.length) {
        const bylineNode = document.createElement("small");
        bylineNode.className = "content-card-meta";
        if (item.source && (textValue(item.source.name) || textValue(item.source.url))) bylineNode.append(sourceIcon(item.source));
        text(bylineNode, "span", byline.join(" · "));
        card.append(bylineNode);
      }
      card.addEventListener("click", () => {
        Array.from(cards.children).forEach((candidate) => {
          if (candidate && candidate.dataset && candidate.dataset.entryId) candidate.setAttribute("aria-pressed", String(candidate.dataset.entryId === item.id));
        });
        content.selectEntry(item.id);
      });
      cards.append(card);
    });
    if (!items.length) text(cards, "p", "当前没有匹配的 AI 资讯或报刊。", "content-empty");
    renderPageControls(cards, page || {}, context);
    cards.scrollTop = scrollTop;
    if (!context.selectedId) renderMessage("选择一条内容，查看摘要、来源与结构化正文。", context);
  }

  function renderEntry(entry, meta, context) {
    const root = byId("news-detail");
    const value = entry || {};
    const metadata = meta || {};
    content.session.metaNotices = Array.isArray(metadata.notices) ? metadata.notices : [];
    root.replaceChildren();
    if (metadata.stale) text(root, "p", "详情来自缓存。", "content-notice news-entry-stale");
    if (value.selected) text(root, "small", "AIHOT 精选", "news-entry-badge");
    text(root, "h2", textValue(value.title) || "无标题");

    const publishedAt = validDate(value.publishedAt);
    const discoveredAt = validDate(value.discoveredAt);
    const byline = [value.source && textValue(value.source.name)];
    if (value.period) byline.push("期号 " + textValue(value.period));
    const date = publishedAt || discoveredAt;
    if (date) byline.push((publishedAt ? "发布于 " : "收录于 ") + dateLabel(date));
    if (byline.some(Boolean)) {
      const bylineNode = document.createElement("p");
      bylineNode.className = "news-entry-byline";
      if (value.source && (textValue(value.source.name) || textValue(value.source.url))) bylineNode.append(sourceIcon(value.source));
      text(bylineNode, "span", byline.filter(Boolean).join(" · "));
      root.append(bylineNode);
    }
    if (value.summary) text(root, "p", value.summary, "news-entry-summary");

    const facts = document.createElement("section");
    facts.className = "news-entry-meta";
    if (value.category) text(facts, "p", "分类：" + value.category);
    if (value.source && value.source.name) text(facts, "p", "来源：" + value.source.name);
    if (value.attribution && value.attribution.name) text(facts, "p", "归属：" + value.attribution.name);
    if (publishedAt) text(facts, "p", "发布时间：" + publishedAt.toLocaleString());
    if (discoveredAt) text(facts, "p", "收录时间：" + discoveredAt.toLocaleString());
    if (value.period) text(facts, "p", "报刊期号：" + value.period);

    const primaryActions = document.createElement("div");
    primaryActions.className = "news-detail-actions";
    externalButton(primaryActions, "AIHOT 阅读页", value.canonicalUrl);
    externalButton(primaryActions, "原文阅读全文", value.originalUrl);
    if (primaryActions.children.length) root.append(primaryActions);

    if (Array.isArray(value.paragraphs)) value.paragraphs.forEach((paragraph) => {
      if (!paragraph || typeof paragraph !== "object") return;
      const section = document.createElement("section");
      section.className = "news-paragraph";
      if (paragraph.heading) text(section, "h3", paragraph.heading);
      if (typeof paragraph.text === "string" && paragraph.text) text(section, "p", paragraph.text);
      const details = [];
      if (paragraph.source && paragraph.source.name) details.push("来源：" + paragraph.source.name);
      if (paragraph.attribution && paragraph.attribution.name) details.push("归属：" + paragraph.attribution.name);
      if (details.length) text(section, "small", details.join(" · "), "news-paragraph-meta");
      const sourceURL = paragraph.url || (paragraph.source && paragraph.source.url) || (paragraph.attribution && paragraph.attribution.url);
      const sourceAction = document.createElement("div");
      sourceAction.className = "news-detail-actions";
      externalButton(sourceAction, "打开段落来源", sourceURL);
      if (sourceAction.children.length) section.append(sourceAction);
      root.append(section);
    });

    if (value.reason) text(root, "p", "推荐理由：" + value.reason, "news-entry-reason");

    const sourceActions = document.createElement("div");
    sourceActions.className = "news-detail-actions";
    externalButton(sourceActions, "来源主页", value.source && value.source.url);
    externalButton(sourceActions, "归属来源", value.attribution && value.attribution.url);
    const detailNotices = content.session.metaNotices;
    const hasDetailNotices = Array.isArray(detailNotices) && detailNotices.some((notice) => notice && typeof notice.message === "string" && notice.message.trim());
    if (facts.children.length || sourceActions.children.length || hasDetailNotices) {
      const sourceDisclosure = document.createElement("details");
      sourceDisclosure.className = "news-entry-sources";
      sourceDisclosure.open = false;
      const summary = document.createElement("summary");
      summary.textContent = "来源与时间";
      const sourceContent = document.createElement("div");
      sourceContent.className = "news-entry-source-content";
      sourceContent.setAttribute("role", "region");
      sourceContent.setAttribute("aria-label", "来源事实与次级链接");
      sourceContent.setAttribute("tabindex", "0");
      if (facts.children.length) sourceContent.append(facts);
      if (sourceActions.children.length) sourceContent.append(sourceActions);
      showNotices(sourceContent, detailNotices);
      sourceDisclosure.append(summary, sourceContent);
      root.append(sourceDisclosure);
    }

    const related = document.createElement("div");
    related.className = "news-related-search";
    const label = document.createElement("label");
    label.textContent = "相关 Skill 搜索词";
    const input = document.createElement("input");
    input.type = "search";
    input.maxLength = 200;
    input.setAttribute("aria-label", "相关技能搜索词");
    input.value = [textValue(value.category), textValue(value.title)].filter(Boolean).join(" ").slice(0, 200);
    label.append(input);
    const search = document.createElement("button");
    search.type = "button";
    search.textContent = "查找相关 Skill";
    search.addEventListener("click", () => beginRelatedSearch(value, input.value));
    related.append(label, search);
    root.append(related);
  }

  function renderMessage(message) {
    const root = byId("news-detail");
    root.replaceChildren();
    text(root, "p", message || "选择一条内容查看详情。", "content-empty");
  }

  function renderError(message) {
    const cards = byId("news-cards");
    cards.replaceChildren();
    text(cards, "p", message || "资讯暂时不可用。", "content-notice");
    const retry = document.createElement("button");
    retry.type = "button";
    retry.textContent = "重试";
    retry.addEventListener("click", () => content.refreshFeed(true));
    cards.append(retry);
  }

  function beginRelatedSearch(entry, term) {
    const snapshot = content.captureReturnArticle(entry && entry.id, returnToArticle);
    if (!snapshot) return;
    if (window.SkillManagerAPI && typeof window.SkillManagerAPI.searchSkills === "function") {
      window.SkillManagerAPI.searchSkills(String(term || "").trim().slice(0, 200));
    } else {
      content.showMarketSearch();
    }
  }

  function returnToArticle() {
    const saved = content.session.returnArticle;
    if (!saved || !saved.query) return;
    content.session.news = { ...saved.query, cursorStack: saved.query.cursorStack.slice(), entryId: saved.id };
    content.session.scroll.news = saved.scrollTop || 0;
    content.clearReturnArticle();
    content.switchWorkspace("news");
    window.requestAnimationFrame(() => {
      byId("news-cards").scrollTop = saved.scrollTop || 0;
    });
  }

  function applyFilters() {
    content.setFeedFilters({
      q: byId("news-search-query").value,
      category: byId("news-category-filter").value,
    });
  }

  document.querySelectorAll("[data-news-view]").forEach((button) => {
    button.addEventListener("click", () => {
      const view = button.dataset.newsView;
      const filters = view === "selected" || view === "all"
        ? { kind: "news", view }
        : { kind: view };
      content.setFeedFilters(filters);
      updateControls(content.session.news);
    });
  });

  document.querySelectorAll("[data-news-window]").forEach((button) => {
    button.addEventListener("click", () => {
      content.setFeedFilters({ window: button.dataset.newsWindow });
      updateControls(content.session.news);
    });
  });

  byId("news-filter-form").addEventListener("submit", (event) => {
    event.preventDefault();
    applyFilters();
  });
  byId("news-category-filter").addEventListener("change", applyFilters);

  content.registerRenderer("news", {
    feed: renderFeed,
    entry: renderEntry,
    message: renderMessage,
    error: renderError,
  });
  content.setReturnArticleHandler(null);
  updateControls(content.session.news);
})();
