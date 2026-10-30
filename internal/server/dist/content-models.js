"use strict";

(() => {
  function priceNumber(raw) {
    const match = /^\s*[¥￥]?\s*(\d+(?:\.\d+)?)\s*$/.exec(String(raw || ""));
    return match && Number.isFinite(Number(match[1])) ? Number(match[1]) : null;
  }
  function selectRows(rows, options = {}) {
    const query = String(options.q || "").trim().toLocaleLowerCase();
    const key = options.sort || "rank";
    const direction = options.direction === "desc" ? -1 : 1;
    function value(row) {
      if (["cachePrice", "inputPrice", "outputPrice"].includes(key)) return priceNumber(row[key]);
      if (["name", "releasedOn"].includes(key)) return row[key] || null;
      return Number.isFinite(row[key]) ? row[key] : null;
    }
    return (rows || []).filter((row) => row && (!options.provider || row.provider === options.provider) &&
      (!query || `${row.name || ""} ${row.provider || ""}`.toLocaleLowerCase().includes(query))).sort((a, b) => {
      const left = value(a), right = value(b);
      if (left == null && right != null) return 1;
      if (right == null && left != null) return -1;
      const comparison = left == null ? 0 : typeof left === "string" ? left.localeCompare(right, "zh-CN", { numeric: true }) : left - right;
      return comparison * direction || a.rank - b.rank;
    });
  }
  window.SkillManagerModels = Object.freeze({ priceNumber, selectRows });

  const byId = (id) => document.getElementById(id);
  const panel = byId("model-leaderboard");
  const trigger = byId("news-models-button");
  const workspace = byId("news-workspace");
  if (!panel || !trigger || !workspace) return;
  const ui = window.SkillManagerContentUI;
  const categories = { overall: "综合", coding: "编程", reasoning: "科研", professional: "专业办公", knowledge: "知识问答" };
  const providerIcons = new Map([
    ["Anthropic", "anthropic.svg"],
    ["Google", "google.svg"],
    ["Meta", "meta.svg"],
    ["OpenAI", "openai.svg"],
    ["Moonshot AI", "moonshot.svg"],
    ["DeepSeek", "deepseek.svg"],
    ["Xiaomi", "xiaomi-mimo.svg"],
    ["Z.ai", "z-ai.svg"],
    ["xAI", "xai.svg"],
    ["Alibaba", "qianwen.svg"],
    ["StepFun", "stepfun.svg"],
    ["MiniMax", "minimax.svg"]
  ]);
  const cache = new Map();
  const pending = new Map();
  const errors = new Set();
  let category = "overall";
  let requestVersion = 0;
  let sort = "rank", direction = "asc", provider = "";
  let savedScroll = { list: 0, detail: 0 };
  let optionsKey = "";
  let notesSnapshot = null;
  const sourceURL = () => category === "overall" ? "https://aihot.news/leaderboard" : "https://aihot.news/leaderboard/category/" + category;
  const visible = () => workspace.dataset.newsPanel === "models" && !workspace.hidden && !document.hidden;
  function text(parent, tag, value, className) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (value != null) element.textContent = String(value);
    parent.append(element);
    return element;
  }
  function external(parent, label, raw, className) {
    const url = ui.safeHTTPURL(raw);
    if (!url) return;
    const button = text(parent, "button", label, className || "model-link");
    button.type = "button";
    button.addEventListener("click", () => window.SkillManagerAPI.openExternal(url));
    return button;
  }
  function status(message, retry = false) {
    const element = byId("model-status");
    if (element.contains(document.activeElement)) panel.querySelector(`[data-model-category="${category}"]`).focus();
    element.replaceChildren();
    if (message) text(element, "span", message);
    element.hidden = !message;
    if (retry) {
      const button = text(element, "button", "重试", "ghost small");
      button.type = "button";
      button.addEventListener("click", () => load(true));
    }
  }
  function render() {
    const record = cache.get(category);
    const board = record && record.data;
    panel.querySelectorAll("[data-model-category]").forEach((button) => button.setAttribute("aria-pressed", String(button.dataset.modelCategory === category)));
    if (pending.has(category)) status(board ? "正在检查榜单更新…" : "正在加载模型榜…");
    else if (errors.has(category)) status(board ? "暂时无法更新，当前显示缓存。" : "模型榜暂时无法加载，请稍后重试。", true);
    else if (board && board.meta && board.meta.stale) status("当前显示缓存，来源正在等待更新。", true);
    else status("");
    byId("model-table-region").hidden = !board;
    byId("model-source-info").hidden = !board;
    byId("model-provider").disabled = !board;
    if (!board) {
      byId("model-summary").textContent = categories[category] + "榜";
      byId("model-updated").textContent = "";
      return;
    }

    const providers = Array.from(new Set(board.entries.map((row) => row.provider).filter(Boolean))).sort((a, b) => a.localeCompare(b));
    const nextKey = category + ":" + providers.join("|");
    if (optionsKey !== nextKey) {
      const select = byId("model-provider");
      select.replaceChildren();
      const all = text(select, "option", "全部厂商"); all.value = "";
      providers.forEach((name) => { text(select, "option", name).value = name; });
      if (!providers.includes(provider)) provider = "";
      select.value = provider;
      optionsKey = nextKey;
    }
    const rows = selectRows(board.entries, { q: byId("model-search").value, provider, sort, direction });
    byId("model-summary").textContent = `${categories[category]}榜 · 显示 ${rows.length} / 原榜 ${board.entries.length} 个模型`;
    const fetched = ui.parseDate(board.fetchedAt);
    byId("model-updated").textContent = [board.updatedLabel, fetched ? "采集于 " + new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false }).format(fetched) : ""].filter(Boolean).join(" · ");
    const body = byId("model-table-body");
    body.replaceChildren();
    rows.forEach((row) => {
      const tr = text(body, "tr"); tr.dataset.modelId = row.id;
      text(tr, "td", row.rank, "model-rank model-number");
      const identity = text(tr, "td", null, "model-identity");
      const providerLogo = providerIcons.get(row.provider);
      if (providerLogo) {
        const image = document.createElement("img");
        image.className = "model-provider-logo";
        image.src = "/icons/providers/" + providerLogo;
        image.alt = "";
        image.setAttribute("aria-hidden", "true");
        image.addEventListener("error", () => { image.hidden = true; });
        identity.append(image);
      }
      if (!external(identity, row.name, row.url, "model-name-link")) text(identity, "strong", row.name, "model-name-text");
      text(identity, "small", row.provider || "—", "model-provider");
      if (row.openWeightsURL) external(identity, "开源权重 ↗", row.openWeightsURL, "model-weights-link");
      text(tr, "td", row.releasedOn || "—", "model-date model-number");
      const coverage = text(tr, "td", null, "model-coverage model-number");
      text(coverage, "span", Number.isFinite(row.coveragePercent) ? row.coveragePercent + "%" : "—");
      if (Number.isFinite(row.benchmarkCount)) text(coverage, "small", row.benchmarkCount + " 项评测");
      ["cachePrice", "inputPrice", "outputPrice"].forEach((key) => {
        const td = text(tr, "td", row[key] || "—", "model-price model-number");
        if (priceNumber(row[key]) == null) td.classList.add("is-missing");
      });
      text(tr, "td", row.score.toLocaleString("zh-CN", { minimumFractionDigits: 1, maximumFractionDigits: 2 }), "model-score model-number");
    });
    if (!rows.length) {
      const td = text(text(body, "tr"), "td", board.entries.length ? "没有匹配的模型，试试清空关键词或厂商筛选。" : "当前原榜暂无可展示的模型。", "model-empty");
      td.colSpan = 8;
    }
    panel.querySelectorAll("[data-model-sort]").forEach((button) => {
      const active = button.dataset.modelSort === sort;
      button.parentElement.setAttribute("aria-sort", active ? direction === "asc" ? "ascending" : "descending" : "none");
      button.querySelector("span").textContent = active ? direction === "asc" ? " ↑" : " ↓" : "";
    });
    if (notesSnapshot !== board) {
      const notes = byId("model-source-notes");
      notes.replaceChildren();
      text(notes, "p", "数据来自 AIHOT 公开榜单。表格保留原榜名次；筛选和列排序只改变当前展示顺序。评分不是正确率，价格不参与原榜排名。");
      text(notes, "p", "价格单位：人民币 / 百万 Token。未核验或缺失的价格保留原站标记；条件价格请以原榜说明为准。");
      (Array.isArray(board.priceNotes) ? board.priceNotes : []).forEach((note) => text(notes, "p", note));
      notesSnapshot = board;
    }
  }
  async function load(force = false) {
    const requested = category;
    const record = cache.get(requested);
    if (pending.has(requested)) { render(); return; }
    if (!force && record && Date.now() - record.checkedAt < 300_000) { render(); return; }
    const version = ++requestVersion;
    pending.set(requested, version);
    errors.delete(requested);
    render();
    try {
      const result = await window.SkillManagerAPI.request("GET", "/api/content/model-leaderboard?category=" + requested, undefined, { withMeta: true, headers: record && record.etag ? { "If-None-Match": record.etag } : {} });
      if (pending.get(requested) !== version) return;
      if (result.status === 304) {
        if (!record) throw new Error("Missing cached leaderboard");
        record.checkedAt = Date.now();
        if (result.etag) record.etag = result.etag;
      } else {
        const data = result.data;
        if (!data || data.category !== requested || data.currency !== "CNY" || !Array.isArray(data.entries) || !data.entries.every((row) => row && typeof row.name === "string" && Number.isFinite(row.rank) && Number.isFinite(row.score))) throw new Error("Invalid leaderboard");
        cache.set(requested, { data, etag: result.etag || "", checkedAt: Date.now() });
      }
    } catch { if (pending.get(requested) === version) errors.add(requested); }
    finally {
      if (pending.get(requested) === version) pending.delete(requested);
      if (category === requested && visible()) render();
    }
  }
  trigger.addEventListener("click", () => {
    savedScroll = { list: byId("news-cards").scrollTop, detail: byId("news-detail").scrollTop };
    workspace.dataset.newsPanel = "models";
    panel.hidden = false;
    render();
    load();
    byId("model-back").focus({ preventScroll: true });
  });
  byId("model-back").addEventListener("click", () => {
    workspace.dataset.newsPanel = "articles";
    panel.hidden = true;
    // Keep the reading DOM intact; the existing content poll resumes in article mode.
    window.requestAnimationFrame(() => {
      byId("news-cards").scrollTop = savedScroll.list;
      byId("news-detail").scrollTop = savedScroll.detail;
      trigger.focus({ preventScroll: true });
    });
  });
  panel.querySelectorAll("[data-model-category]").forEach((button) => button.addEventListener("click", () => {
    category = button.dataset.modelCategory;
    provider = "";
    byId("model-provider").value = "";
    load();
  }));
  panel.querySelectorAll("[data-model-sort]").forEach((button) => button.addEventListener("click", () => {
    const next = button.dataset.modelSort;
    direction = sort === next ? direction === "asc" ? "desc" : "asc" : ["score", "coveragePercent", "releasedOn"].includes(next) ? "desc" : "asc";
    sort = next;
    render();
  }));
  byId("model-filter-form").addEventListener("submit", (event) => { event.preventDefault(); render(); });
  byId("model-search").addEventListener("input", render);
  byId("model-provider").addEventListener("change", () => { provider = byId("model-provider").value; render(); });
  byId("model-filter-reset").addEventListener("click", () => {
    byId("model-search").value = ""; provider = ""; byId("model-provider").value = ""; render();
  });
  byId("model-original").addEventListener("click", () => window.SkillManagerAPI.openExternal(sourceURL()));
  byId("workspace-news").addEventListener("click", () => { if (visible()) load(); });
  byId("return-article-control").addEventListener("click", () => {
    workspace.dataset.newsPanel = "articles";
    panel.hidden = true;
  }, { capture: true });
  const refreshVisible = () => { if (visible()) load(); };
  window.setInterval(refreshVisible, 300_000);
  window.addEventListener("focus", refreshVisible);
  document.addEventListener("visibilitychange", refreshVisible);
})();
