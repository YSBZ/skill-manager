"use strict";

(() => {
  const byId = (id) => document.getElementById(id);
  const CACHE_MS = 300000;
  const SESSION_STORAGE_KEY = "skillmanager.workspace";
  const session = {
    initialized: false,
    surface: "skills",
    skillsView: "manage",
    news: { kind: "news", view: "selected", window: "7d", q: "", category: "", cursor: "", cursorStack: [], entryId: "" },
    project: { view: "weekly", q: "", scene: "", cursor: "", cursorStack: [], entryId: "" },
    returnArticle: null,
    scroll: { news: 0, discovery: 0 },
    pages: new Map(),
    details: new Map(),
    withdrawn: new Set(),
    contentStatus: null,
    listGeneration: 0,
    detailGeneration: 0,
    poll: 0,
  };

  const navigation = {
    newsButton: byId("workspace-news"),
    skillsButtons: document.querySelectorAll("[data-skills-view]"),
    newsWorkspace: byId("news-workspace"),
    skillsWorkspace: byId("skills-workspace"),
    discoveryWorkspace: byId("discovery-workspace"),
    manageWorkspace: byId("manage-workspace"),
    returnArticleControl: byId("return-article-control"),
  };

  let returnArticleHandler = null;
  let navigationBound = false;
  let refreshListenersInstalled = false;
  const surfaceRenderers = { news: {}, discovery: {} };

  function activeQuery(surface = session.surface) {
    return surface === "news" ? session.news : session.project;
  }

  function feedScope(surface = session.surface) {
    return surface === "news" ? "news:" : "discovery:";
  }

  function contentVisible() {
    return (session.surface === "news" && navigation.newsWorkspace.dataset.newsPanel !== "models") || (session.surface === "skills" && session.skillsView === "discovery");
  }

  function rendererContext(workspaceSurface = session.surface) {
    const surface = workspaceSurface === "news" ? "news" : "discovery";
    const query = activeQuery(workspaceSurface);
    return {
      surface,
      workspace: workspaceSurface === "news" ? "news" : "skills",
      skillsView: session.skillsView,
      query: { ...query, cursorStack: query.cursorStack.slice() },
      selectedId: query.entryId,
    };
  }

  function registerRenderer(surface, callbacks) {
    if (surface !== "news" && surface !== "discovery") return false;
    if (!callbacks || typeof callbacks !== "object") return false;
    const target = surfaceRenderers[surface];
    ["feed", "entry", "message", "error"].forEach((name) => {
      if (typeof callbacks[name] === "function") target[name] = callbacks[name];
    });
    return true;
  }

  function visibleContent() {
    return contentVisible() && !document.hidden;
  }

  function requestFrame(callback) {
    if (typeof window.requestAnimationFrame === "function") window.requestAnimationFrame(callback);
    else callback();
  }

  function currentScrollElement(surface = session.surface, skillsView = session.skillsView) {
    if (surface === "news") return byId("news-cards") || byId("news-list");
    if (surface === "skills" && skillsView === "discovery") return byId("discovery-cards") || byId("discovery-list");
    return null;
  }

  function saveCurrentScroll() {
    const element = currentScrollElement();
    if (!element) return;
    const key = session.surface === "news" ? "news" : "discovery";
    session.scroll[key] = element.scrollTop || 0;
  }

  function restoreCurrentScroll() {
    requestFrame(() => {
      const element = currentScrollElement();
      if (!element) return;
      const key = session.surface === "news" ? "news" : "discovery";
      element.scrollTop = session.scroll[key] || 0;
    });
  }

  function showWorkspace(name, persist = true) {
    switchWorkspace(name, persist);
  }

  function showSkillsView(view) {
    setSkillsView(view);
  }

  function activateSkillsView(view) {
    const nextView = view === "discovery" ? "discovery" : "manage";
    if (session.surface !== "skills") {
      setSkillsView(nextView, false);
      switchWorkspace("skills");
      return;
    }
    setSkillsView(nextView);
  }

  function syncWorkspaceNavigation() {
    const newsActive = session.surface === "news";
    navigation.newsButton.setAttribute("aria-current", newsActive ? "page" : "false");
    navigation.skillsButtons.forEach((button) => {
      const active = !newsActive && button.dataset.skillsView === session.skillsView;
      button.setAttribute("aria-current", active ? "page" : "false");
    });
  }

  function bindWorkspaceNavigation() {
    if (navigationBound) return;
    navigationBound = true;
    navigation.newsButton.addEventListener("click", () => switchWorkspace("news"));
    navigation.skillsButtons.forEach((button) => {
      button.addEventListener("click", () => activateSkillsView(button.dataset.skillsView));
    });
    navigation.returnArticleControl.addEventListener("click", () => returnToArticle());
  }

  function setSkillsView(view, refresh = true) {
    const nextView = view === "discovery" ? "discovery" : "manage";
    const activatingDiscovery = session.surface === "skills" && nextView === "discovery" && nextView !== session.skillsView;
    if (nextView !== session.skillsView) {
      saveCurrentScroll();
      session.listGeneration += 1;
      session.detailGeneration += 1;
    }
    session.skillsView = nextView;
    navigation.discoveryWorkspace.hidden = nextView !== "discovery";
    navigation.manageWorkspace.hidden = nextView !== "manage";
    document.querySelectorAll("[data-skills-view]").forEach((button) => {
      button.setAttribute("aria-current", button.dataset.skillsView === nextView ? "page" : "false");
    });
    syncWorkspaceNavigation();
    updateReturnArticleControl();
    if (refresh && contentVisible() && !document.hidden) {
      refreshFeed(false, false, activatingDiscovery);
      refreshContentStatus();
    }
    restoreCurrentScroll();
  }

  function switchWorkspace(name, persist = true) {
    const nextSurface = name === "news" ? "news" : "skills";
    const activatingContent = nextSurface !== session.surface && (nextSurface === "news" || session.skillsView === "discovery");
    if (nextSurface !== session.surface) {
      saveCurrentScroll();
      session.listGeneration += 1;
      session.detailGeneration += 1;
    }
    session.surface = nextSurface;
    const isNews = nextSurface === "news";
    syncWorkspaceNavigation();
    navigation.newsWorkspace.hidden = !isNews;
    navigation.skillsWorkspace.hidden = isNews;

    if (!isNews) {
      navigation.discoveryWorkspace.hidden = session.skillsView !== "discovery";
      navigation.manageWorkspace.hidden = session.skillsView !== "manage";
      document.querySelectorAll("[data-skills-view]").forEach((button) => {
        button.setAttribute("aria-current", button.dataset.skillsView === session.skillsView ? "page" : "false");
      });
    }
    updateReturnArticleControl();
    if (persist) {
      try { localStorage.setItem(SESSION_STORAGE_KEY, nextSurface); } catch { /* storage may be disabled */ }
    }
    if (contentVisible() && !document.hidden) {
      refreshFeed(false, false, activatingContent);
      refreshContentStatus();
    }
    restoreCurrentScroll();
  }

  function updateReturnArticleControl() {
    navigation.returnArticleControl.hidden = session.surface === "news" || (!returnArticleHandler && !session.returnArticle);
  }

  function returnToArticle() {
    if (returnArticleHandler) returnArticleHandler();
    else if (session.returnArticle && typeof session.returnArticle.onReturn === "function") session.returnArticle.onReturn();
  }

  function onMainStatus(status) {
    if (session.initialized) return;
    session.initialized = true;
    bindWorkspaceNavigation();
    installRefreshListeners();
    let saved = "";
    try { saved = localStorage.getItem(SESSION_STORAGE_KEY) || ""; } catch { /* storage may be disabled */ }
    const initial = saved === "news" || saved === "skills" ? saved : (status && status.firstRun ? "news" : "skills");
    switchWorkspace(initial, true);
  }

  function buildFeedURL(surface = session.surface) {
    const news = surface === "news";
    const query = news ? session.news : session.project;
    const values = news ? { kind: query.kind, limit: "20" } : { kind: "project", view: query.view, limit: "20" };
    if (news && query.kind === "news") {
      values.view = query.view;
      values.window = query.window;
    }
    const params = new URLSearchParams(values);
    const q = String(query.q || "").trim().slice(0, 200);
    if (q) params.set("q", q);
    if (news && query.category) params.set("category", query.category);
    if (!news && query.scene) params.set("scene", query.scene);
    if (query.cursor) params.set("cursor", query.cursor);
    return "/api/content/feed?" + params.toString();
  }

  function setFeedFilter(key, value) {
    return setFeedFilters({ [key]: value });
  }

  function setFeedFilters(filters) {
    const query = activeQuery();
    const allowed = session.surface === "news" ? ["kind", "view", "window", "q", "category"] : ["view", "q", "scene"];
    if (!filters || typeof filters !== "object") return false;
    let changed = false;
    Object.keys(filters).forEach((key) => {
      if (!allowed.includes(key)) return;
      const value = filters[key];
      const nextValue = key === "q" ? String(value || "").trim().slice(0, 200) : String(value ?? "");
      if (query[key] === nextValue) return;
      query[key] = nextValue;
      changed = true;
    });
    if (!changed) return false;
    query.cursor = "";
    query.cursorStack = [];
    query.entryId = "";
    session.listGeneration += 1;
    session.detailGeneration += 1;
    hideUpdateHint(session.surface);
    showDetailMessage("选择条目以查看详情。");
    if (contentVisible() && !document.hidden) refreshFeed(true);
    return true;
  }

  function captureReturnArticle(id, onReturn) {
    const value = String(id || "");
    if (session.surface !== "news" || !/^[A-Za-z0-9_-]{1,160}$/.test(value)) return null;
    session.news.entryId = value;
    const list = byId("news-cards");
    session.returnArticle = {
      id: value,
      query: { ...session.news, cursorStack: session.news.cursorStack.slice() },
      scrollTop: list ? list.scrollTop || 0 : 0,
    };
    returnArticleHandler = typeof onReturn === "function" ? onReturn : null;
    updateReturnArticleControl();
    return session.returnArticle;
  }

  function clearReturnArticle() {
    session.returnArticle = null;
    returnArticleHandler = null;
    updateReturnArticleControl();
  }

  function setFeedCursor(cursor) {
    const query = activeQuery();
    const nextCursor = String(cursor || "");
    if (query.cursor === nextCursor) return;
    if (nextCursor) query.cursorStack.push(query.cursor || "");
    query.cursor = nextCursor;
    session.listGeneration += 1;
    session.detailGeneration += 1;
    if (contentVisible() && !document.hidden) refreshFeed(true);
  }

  function previousFeedPage() {
    const query = activeQuery();
    if (!query.cursorStack.length) return;
    query.cursor = query.cursorStack.pop() || "";
    session.listGeneration += 1;
    session.detailGeneration += 1;
    if (contentVisible() && !document.hidden) refreshFeed(true);
  }

  function nextFeedPage() {
    const key = pageCacheKey(session.surface, buildFeedURL(session.surface));
    const cached = session.pages.get(key);
    if (cached && cached.page && cached.page.nextCursor) setFeedCursor(cached.page.nextCursor);
  }

  function pageCacheKey(surface, url) {
    return feedScope(surface) + url;
  }

  function isCurrentFeed(surface, url, generation) {
    return generation === session.listGeneration && surface === session.surface && url === buildFeedURL(surface);
  }

  async function refreshFeed(force = false, deferReorder = false, resumeSelection = false) {
    const surface = session.surface;
    if (surface !== "news" && !(surface === "skills" && session.skillsView === "discovery")) return;
    const url = buildFeedURL(surface);
    const key = pageCacheKey(surface, url);
    const cached = session.pages.get(key);
    if (!force && cached && Date.now() - cached.checkedAt < CACHE_MS) {
      renderFeed(cached.page, cached.stale, surface);
      if (cached.pending) showNewContentHint(surface, key);
      const selectedID = activeQuery(surface).entryId;
      if (selectedID && (resumeSelection || !(cached.page.items || []).some((item) => item.id === selectedID))) selectEntry(selectedID, true);
      return true;
    }

    const generation = ++session.listGeneration;
    const etagRecord = cached && cached.pending ? cached.pending : cached;
    try {
      const result = await window.SkillManagerAPI.request("GET", url, undefined, {
        withMeta: true,
        headers: etagRecord && etagRecord.etag ? { "If-None-Match": etagRecord.etag } : {},
      });
      if (!isCurrentFeed(surface, url, generation)) return;
      if (result.status === 304) {
        if (!cached && !etagRecord) throw Object.assign(new Error("304 without cached page"), { status: 502 });
        const checkedAt = Date.now();
        if (cached && cached.pending) {
          cached.pending.checkedAt = checkedAt;
          if (result.etag) cached.pending.etag = result.etag;
          renderFeed(cached.page, cached.stale, surface);
          showNewContentHint(surface, key);
        } else {
          cached.checkedAt = checkedAt;
          if (result.etag) cached.etag = result.etag;
          renderFeed(cached.page, cached.stale, surface);
        }
        const selectedID = activeQuery(surface).entryId;
        if (selectedID && (resumeSelection || !(cached.page.items || []).some((item) => item.id === selectedID))) selectEntry(selectedID, true);
        return true;
      }

      const page = result.data;
      session.withdrawn.forEach((id) => removeEntryFromPage(page, id));
      const record = { page, etag: result.etag || "", checkedAt: Date.now(), stale: !!(page.meta && page.meta.stale) };
      const selectedID = activeQuery(surface).entryId;
      if (deferReorder && cached && !sameFeedContent(cached.page, page)) {
        cached.pending = record;
        cached.checkedAt = record.checkedAt;
        showNewContentHint(surface, key);
        renderFeed(cached.page, cached.stale, surface);
        if (selectedID && (resumeSelection || !(page.items || []).some((item) => item.id === selectedID))) selectEntry(selectedID, true);
        return true;
      }
      if (cached && cached.pending && !deferReorder) {
        // An already-announced update still needs an explicit accept action.
        cached.pending = record;
        cached.checkedAt = record.checkedAt;
        renderFeed(cached.page, cached.stale, surface);
        showNewContentHint(surface, key);
        if (selectedID && (resumeSelection || !(page.items || []).some((item) => item.id === selectedID))) selectEntry(selectedID, true);
        return true;
      }
      session.pages.set(key, record);
      clearAcceptedUpdateHint(surface, key);
      renderFeed(page, record.stale, surface);
      if (selectedID && (resumeSelection || deferReorder || !(page.items || []).some((item) => item.id === selectedID))) selectEntry(selectedID, true);
      return true;
    } catch (error) {
      if (!isCurrentFeed(surface, url, generation)) return;
      const query = activeQuery(surface);
      if (error.status === 409 && query.cursor) {
        query.cursor = "";
        query.cursorStack = [];
        session.listGeneration += 1;
        const firstPageURL = buildFeedURL(surface);
        const recovered = await refreshFeed(true, deferReorder, resumeSelection);
        if (surface === session.surface && firstPageURL === buildFeedURL(surface)) {
          const hint = byId(surface === "news" ? "news-update-hint" : "discovery-update-hint");
          if (hint) {
            hint.hidden = false;
            hint.textContent = recovered ? "分页位置已失效，列表已从第一页重新加载。" : "分页位置已失效，第一页加载失败，请稍后重试。";
            hint.onclick = null;
          }
        }
        return recovered;
      }
      if (cached) {
        cached.stale = true;
        renderFeed(cached.page, true, surface);
        if (cached.pending) showNewContentHint(surface, key);
        const selectedID = activeQuery(surface).entryId;
        if (resumeSelection && selectedID) selectEntry(selectedID, true);
      } else {
        renderFeedError(surface);
      }
      return false;
    }
  }

  function sameFeedContent(left, right) {
    const normalize = (page) => JSON.stringify({ items: page.items || [], nextCursor: page.nextCursor || "" });
    return normalize(left || {}) === normalize(right || {});
  }

  function showNewContentHint(surface = session.surface, key = pageCacheKey(surface, buildFeedURL(surface))) {
    const button = byId(surface === "news" ? "news-update-hint" : "discovery-update-hint");
    button.hidden = false;
    button.textContent = "有新内容 · 点击更新";
    button.onclick = () => acceptPendingFeed(surface, key);
  }

  function hideUpdateHint(surface) {
    const button = byId(surface === "news" ? "news-update-hint" : "discovery-update-hint");
    button.hidden = true;
    button.textContent = "";
    button.onclick = null;
  }

  function clearAcceptedUpdateHint(surface, key) {
    if (surface === session.surface && key === pageCacheKey(session.surface, buildFeedURL(session.surface))) hideUpdateHint(surface);
  }

  function acceptPendingFeed(surface, key) {
    if (surface !== session.surface || key !== pageCacheKey(session.surface, buildFeedURL(session.surface))) return;
    const cached = session.pages.get(key);
    if (!cached || !cached.pending) return;
    const accepted = cached.pending;
    delete accepted.pending;
    session.pages.set(key, accepted);
    hideUpdateHint(surface);
    renderFeed(accepted.page, accepted.stale, surface);
    const selectedID = activeQuery(surface).entryId;
    if (selectedID && !(accepted.page.items || []).some((item) => item.id === selectedID)) selectEntry(selectedID, true);
  }

  function entryCardText(item) {
    const project = item.project;
    const details = [item.summary, item.source && item.source.name, item.category, project && project.repo].filter(Boolean);
    return [item.title || "无标题", ...details].join("\n");
  }

  function renderFeed(page, stale = false, surface = session.surface) {
    const context = rendererContext(surface);
    const renderer = surfaceRenderers[context.surface].feed;
    if (renderer) return renderer(page, stale, context);
    renderFeedFallback(page, stale, context);
  }

  function renderFeedFallback(page, stale, context) {
    const news = context.surface === "news";
    const cards = byId(news ? "news-cards" : "discovery-cards");
    const notices = byId(news ? "news-meta-notices" : "discovery-meta-notices");
    const selectedID = context.selectedId;
    cards.replaceChildren();
    notices.replaceChildren();
    (page.items || []).forEach((item) => {
      if (!item || !item.id || !/^[A-Za-z0-9_-]{1,160}$/.test(String(item.id))) return;
      const card = document.createElement("button");
      card.type = "button";
      card.className = "content-card";
      card.dataset.entryId = item.id;
      card.setAttribute("aria-current", String(item.id === selectedID));
      card.textContent = entryCardText(item);
      card.addEventListener("click", () => selectEntry(item.id));
      cards.append(card);
    });
    if (stale || (page.meta && page.meta.stale)) {
      const hint = document.createElement("p");
      hint.textContent = "当前使用缓存内容。";
      notices.append(hint);
    }
    humanNotices(page.meta && page.meta.notices).forEach((message) => {
      const notice = document.createElement("p");
      notice.textContent = message;
      notices.append(notice);
    });
  }

  function renderFeedError(surface) {
    const context = rendererContext(surface);
    const renderer = surfaceRenderers[context.surface].error;
    if (renderer) return renderer("内容暂时无法加载，请稍后重试。", context);
    const cards = byId(context.surface === "news" ? "news-cards" : "discovery-cards");
    cards.replaceChildren();
    const message = document.createElement("p");
    message.textContent = "内容暂时无法加载，请稍后重试。";
    cards.append(message);
  }

  function safeExternalURL(value) {
    try {
      const url = new URL(String(value || ""), window.location && window.location.href);
      return url.protocol === "http:" || url.protocol === "https:" ? url.href : "";
    } catch {
      return "";
    }
  }

  function renderSelectedEntry(entry, meta, surface = session.surface) {
    const context = rendererContext(surface);
    const renderer = surfaceRenderers[context.surface].entry;
    if (renderer) return renderer(entry, meta, context);
    renderEntryFallback(entry, meta, context);
  }

  function renderEntryFallback(entry, meta, context) {
    const root = byId(context.surface === "news" ? "news-detail" : "discovery-detail");
    root.replaceChildren();
    const title = document.createElement("h2");
    title.textContent = entry.title || "无标题";
    root.append(title);
    if (entry.summary) {
      const summary = document.createElement("p");
      summary.textContent = entry.summary;
      root.append(summary);
    }
    const project = entry.project;
    if (project && project.repo) {
      const repo = document.createElement("p");
      repo.textContent = "GitHub 项目 · " + project.repo;
      root.append(repo);
    }
    (entry.paragraphs || []).forEach((paragraph) => {
      const section = document.createElement("section");
      if (paragraph.heading) {
        const heading = document.createElement("h3");
        heading.textContent = paragraph.heading;
        section.append(heading);
      }
      const body = document.createElement("p");
      body.textContent = paragraph.text || "";
      section.append(body);
      const linkValue = paragraph.url || (paragraph.source && paragraph.source.url) || (paragraph.attribution && paragraph.attribution.url);
      const safeURL = safeExternalURL(linkValue);
      if (safeURL) {
        const link = document.createElement("a");
        link.href = safeURL;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        link.textContent = "查看来源";
        section.append(link);
      }
      root.append(section);
    });
    const sourceURL = safeExternalURL(entry.originalUrl || entry.canonicalUrl);
    if (sourceURL) {
      const link = document.createElement("a");
      link.href = sourceURL;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      link.textContent = "打开原文";
      root.append(link);
    }
    if (meta && meta.stale) {
      const hint = document.createElement("p");
      hint.textContent = "详情来自缓存。";
      root.append(hint);
    }
  }

  function renderSelectedForSurface(entry, meta, surface) {
    renderSelectedEntry(entry, meta, surface);
  }

  function effectiveDetailMeta(record) {
    const meta = record.meta || {};
    return record.stale && !meta.stale ? { ...meta, stale: true } : meta;
  }

  function showDetailMessage(message, surface = session.surface) {
    const context = rendererContext(surface);
    const renderer = surfaceRenderers[context.surface].message;
    if (renderer) return renderer(message, context);
    const root = byId(context.surface === "news" ? "news-detail" : "discovery-detail");
    root.replaceChildren();
    const line = document.createElement("p");
    line.textContent = message;
    root.append(line);
  }

  function renderCurrentFeed(surface = session.surface) {
    const key = pageCacheKey(surface, buildFeedURL(surface));
    const cached = session.pages.get(key);
    if (cached) renderFeed(cached.page, cached.stale, surface);
  }

  async function selectEntry(id, revalidate = false) {
    const value = String(id || "");
    if (!/^[A-Za-z0-9_-]{1,160}$/.test(value)) return;
    const surface = session.surface;
    if (surface !== "news" && !(surface === "skills" && session.skillsView === "discovery")) return;
    const selected = activeQuery(surface);
    selected.entryId = value;
    const generation = ++session.detailGeneration;
    if (session.withdrawn.has(value)) {
      showDetailMessage("该条目已撤回。", surface);
      return;
    }
    const cached = session.details.get(value);
    if (cached) renderSelectedForSurface(cached.entry, effectiveDetailMeta(cached), surface);
    else showDetailMessage("正在加载详情…", surface);
    if (!revalidate && cached && Date.now() - cached.checkedAt < CACHE_MS) return;
    try {
      const result = await window.SkillManagerAPI.request("GET", "/api/content/entries/" + encodeURIComponent(value), undefined, {
        withMeta: true,
        headers: cached && cached.etag ? { "If-None-Match": cached.etag } : {},
      });
      if (generation !== session.detailGeneration || selected.entryId !== value || session.surface !== surface) return;
      if (session.withdrawn.has(value)) return;
      if (result.status === 304) {
        if (!cached) throw Object.assign(new Error("304 without cached detail"), { status: 502 });
        cached.checkedAt = Date.now();
        if (result.etag) cached.etag = result.etag;
        renderSelectedForSurface(cached.entry, effectiveDetailMeta(cached), surface);
        return;
      }
      const record = {
        entry: result.data.entry,
        meta: result.data.meta || {},
        etag: result.etag || "",
        checkedAt: Date.now(),
        stale: !!(result.data.meta && result.data.meta.stale),
      };
      session.details.set(value, record);
      renderSelectedForSurface(record.entry, record.meta, surface);
    } catch (error) {
      if (error.status === 410) {
        evictEntryEverywhere(value, surface);
        return;
      }
      if (generation !== session.detailGeneration || selected.entryId !== value || session.surface !== surface) return;
      if (error.status === 404) {
        showDetailMessage("条目当前不可用。", surface);
        return;
      }
      if (cached) {
        cached.stale = true;
        renderSelectedForSurface(cached.entry, effectiveDetailMeta(cached), surface);
      } else {
        showDetailMessage("详情暂时不可用，请稍后重试。", surface);
      }
    }
  }

  function evictEntryEverywhere(id, requestSurface) {
    session.withdrawn.add(id);
    session.details.delete(id);
    session.pages.forEach((record) => {
      removeEntryFromPage(record.page, id);
      if (record.pending) removeEntryFromPage(record.pending.page, id);
    });
    document.querySelectorAll("[data-entry-id]").forEach((card) => {
      if (card.dataset.entryId === id) card.remove();
    });
    const activeSurface = session.surface;
    const activeSelection = activeQuery(activeSurface).entryId;
    if (session.news.entryId === id) session.news.entryId = "";
    if (session.project.entryId === id) session.project.entryId = "";
    if (activeSurface === requestSurface && activeSelection === id) {
      renderCurrentFeed(activeSurface);
      showDetailMessage("该条目已撤回。", activeSurface);
    }
  }

  function removeEntryFromPage(page, id) {
    if (!page || !Array.isArray(page.items)) return;
    page.items = page.items.filter((item) => item.id !== id);
  }

  function humanSourceName(name) {
    const value = String(name || "");
    if (value.startsWith("aihot:news")) return "AI 资讯" + (value.includes(":selected:") ? "精选" : "");
    if (value.startsWith("aihot:topics")) return "AI 主题";
    if (value.startsWith("aihot:dailies")) return "每日 AI 报告";
    if (value.startsWith("aihot:weekly")) return "每周 AI 报告";
    if (value.startsWith("aihot:monthly")) return "每月 AI 报告";
    if (value.startsWith("aihot:detail:")) return "AIHOT 详情 · " + value.slice("aihot:detail:".length);
    if (value.startsWith("aihot:detail")) return "AIHOT 详情";
    if (value === "github") return "GitHub 项目";
    return "内容来源";
  }

  function sourceStateLabel(source) {
    const state = source && source.state;
    if (source && source.name === "github" && state === "stale" && !contentDate(source.lastSuccessAt)) return "尚无可用数据";
    return ({ fresh: "已更新", stale: "缓存内容", rate_limited: "等待更新", unavailable: "暂不可用" })[state] || "状态未知";
  }

  function humanNotices(notices) {
    if (!Array.isArray(notices)) return [];
    return Array.from(new Set(notices.map((notice) => {
      if (typeof notice === "string") return notice;
      if (notice && typeof notice.message === "string") return notice.message;
      if (notice && typeof notice.text === "string") return notice.text;
      return "";
    }).map((message) => String(message).trim()).filter(Boolean)));
  }

  function contentDate(raw) {
    const parseDate = window.SkillManagerContentUI && window.SkillManagerContentUI.parseDate;
    return typeof parseDate === "function" ? parseDate(raw) : null;
  }

  function compactDate(raw) {
    const date = contentDate(raw);
    if (!date) return "";
    return date.toLocaleString(undefined, {
      month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hourCycle: "h23",
    });
  }

  function sourceGroupFor(root, sources) {
    const isDiscovery = root.id === "discovery-status";
    return sources.filter((source) => {
      const name = String(source && source.name || "");
      return isDiscovery ? name === "github" : name.startsWith("aihot:");
    });
  }

  function sourceGroupName(root) {
    return root.id === "discovery-status" ? "GitHub" : "AIHOT";
  }

  function statusSummaryState(sources, meta) {
    const severity = { fresh: 0, stale: 1, rate_limited: 2, unavailable: 3 };
    const worst = sources.reduce((state, source) => Math.max(state, severity[source.state] ?? 3), 0);
    if (worst === 3) return sources.length ? "暂不可用" : (meta.stale ? "缓存内容" : "状态未知");
    if (sources.some((source) => source.name === "github" && source.state === "stale" && !contentDate(source.lastSuccessAt))) return "尚无可用数据";
    if (worst === 2) return "等待更新";
    if (worst === 1 || meta.stale) return "缓存内容";
    return sources.length ? "已更新" : (meta.stale ? "缓存内容" : "状态未知");
  }

  function latestSuccess(sources, meta) {
    const timestamps = [meta.sourceUpdatedAt, ...sources.map((source) => source.lastSuccessAt)]
      .filter((value) => typeof value === "string")
      .map((value) => ({ value, date: contentDate(value) }))
      .filter((value) => value.date)
      .map((value) => ({ value: value.value, time: value.date.getTime() }))
      .sort((left, right) => right.time - left.time);
    return timestamps.length ? compactDate(timestamps[0].value) : "";
  }

  function coverageLabel(coverage) {
    if (!coverage || typeof coverage !== "object") return "";
    const parts = [];
    if (typeof coverage.candidates === "number" && Number.isFinite(coverage.candidates)) parts.push(coverage.candidates + " 个候选");
    if (typeof coverage.updated === "number" && Number.isFinite(coverage.updated)) parts.push(coverage.updated + " 个已更新");
    if (typeof coverage.completeGrowth === "number" && Number.isFinite(coverage.completeGrowth)) parts.push(coverage.completeGrowth + " 个有完整周数据");
    return parts.length ? "GitHub 覆盖：" + parts.join(" / ") : "";
  }

  function renderContentStatus(meta) {
    const value = meta && typeof meta === "object" ? meta : {};
    document.querySelectorAll(".content-status").forEach((root) => {
      const previous = Array.from(root.children).find((child) => child.className === "content-status-details");
      const previousSummary = previous && previous.children[0];
      const previousRegion = previous && previous.children[1];
      const focusedSummary = !!previousSummary && document.activeElement === previousSummary;
      const focusedRegion = !!previousRegion && document.activeElement === previousRegion;
      const wasOpen = !!(previous && previous.open);
      const scrollTop = previousRegion ? previousRegion.scrollTop : 0;
      root.replaceChildren();
      const allSources = Array.isArray(value.sources) ? value.sources.filter((source) => source && typeof source === "object") : [];
      const sources = sourceGroupFor(root, allSources);
      const notices = humanNotices(value.notices);
      const coverage = root.id === "discovery-status" ? coverageLabel(value.coverage) : "";
      const hasStale = !!value.stale;
      if (!sources.length && !notices.length && !coverage && !hasStale) return;

      const disclosure = document.createElement("details");
      disclosure.className = "content-status-details";
      disclosure.open = wasOpen;
      const summary = document.createElement("summary");
      const status = statusSummaryState(sources, value);
      const timestamp = latestSuccess(sources, value);
      const summaryParts = [sourceGroupName(root), status];
      if (timestamp) summaryParts.push("更新于 " + timestamp);
      if (notices.length) summaryParts.push(notices.length + " 条通知");
      const label = document.createElement("span");
      label.className = "content-status-label";
      label.textContent = summaryParts.join(" · ");
      summary.append(label);
      summary.setAttribute("aria-label", label.textContent + "；展开查看来源检查点、覆盖与通知");

      const region = document.createElement("div");
      region.className = "content-status-details-content";
      region.setAttribute("role", "region");
      region.setAttribute("aria-label", sourceGroupName(root) + " 来源检查点、覆盖与通知");
      region.setAttribute("tabindex", "0");
      sources.forEach((source) => {
        const facts = [humanSourceName(source.name), sourceStateLabel(source)];
        const updated = compactDate(source.lastSuccessAt);
        const retry = compactDate(source.retryAt);
        if (updated) facts.push("上次成功 " + updated);
        if (retry) facts.push("下次重试 " + retry);
        const line = document.createElement("p");
        line.textContent = facts.join(" · ");
        region.append(line);
      });
      if (coverage) {
        const line = document.createElement("p");
        line.textContent = coverage;
        region.append(line);
      }
      if (hasStale && status !== "缓存内容") {
        const line = document.createElement("p");
        line.textContent = "当前使用缓存内容。";
        region.append(line);
      }
      notices.forEach((message) => {
        const notice = document.createElement("p");
        notice.textContent = message;
        region.append(notice);
      });
      disclosure.append(summary, region);
      root.append(disclosure);
      if (focusedSummary && typeof summary.focus === "function") summary.focus({ preventScroll: true });
      else if (focusedRegion && typeof region.focus === "function") region.focus({ preventScroll: true });
      region.scrollTop = scrollTop;
    });
  }

  async function refreshContentStatus() {
    try {
      const meta = await window.SkillManagerAPI.request("GET", "/api/content/status");
      session.contentStatus = meta || {};
      renderContentStatus(session.contentStatus);
      return session.contentStatus;
    } catch {
      document.querySelectorAll(".content-status").forEach((root) => {
        root.replaceChildren();
        const error = document.createElement("p");
        error.className = "content-status-error";
        error.textContent = "内容服务暂不可用；本地技能管理仍可使用。";
        root.append(error);
      });
      return null;
    }
  }

  function refreshIfVisible() {
    if (!visibleContent()) return;
    refreshFeed(true, true);
    refreshContentStatus();
  }

  function installRefreshListeners() {
    if (refreshListenersInstalled) return;
    refreshListenersInstalled = true;
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden) refreshIfVisible();
    });
    window.addEventListener("focus", refreshIfVisible);
    session.poll = window.setInterval(refreshIfVisible, CACHE_MS);
  }

  bindWorkspaceNavigation();

  window.SkillManagerContent = {
    onMainStatus,
    showMarketSearch() {
      setSkillsView("manage", false);
      showWorkspace("skills");
    },
    setReturnArticleHandler(handler) {
      returnArticleHandler = typeof handler === "function" ? handler : null;
      updateReturnArticleControl();
    },
    switchWorkspace,
    setSkillsView,
    setFeedFilter,
    setFeedFilters,
    setFeedCursor,
    previousFeedPage,
    nextFeedPage,
    selectEntry,
    refreshFeed,
    refreshContentStatus,
    buildFeedURL,
    registerRenderer,
    captureReturnArticle,
    clearReturnArticle,
    renderFeed,
    renderSelectedEntry,
    renderFeedError,
    showDetailMessage,
    session,
  };
})();
