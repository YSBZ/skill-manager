"use strict";

(() => {
  const ui = window.SkillManagerContentUI;
  const zone = "Asia/Shanghai";
  const dateFormat = new Intl.DateTimeFormat("en-CA", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit" });
  function dateKey(raw) {
    const date = raw instanceof Date ? raw : ui.parseDate(raw);
    if (!date || !Number.isFinite(date.getTime())) return "";
    const parts = Object.fromEntries(dateFormat.formatToParts(date).map((part) => [part.type, part.value]));
    return `${parts.year}-${parts.month}-${parts.day}`;
  }
  function eventDay(event) {
    if (event.status === "confirmed") {
      if (/^\d{4}-\d\d-\d\d$/.test(event.occurredOn || "") && dateKey(event.occurredOn + "T00:00:00+08:00")) {
        return { date: event.occurredOn, basis: "已核实发生日期" };
      }
      return { date: dateKey(event.confirmedAt), basis: "确认帖日期" };
    }
    const scheduled = dateKey(event.schedule && event.schedule.from);
    if (scheduled) return { date: scheduled, basis: "公告时间窗口" };
    return { date: dateKey(event.createdAt), basis: "公告发布日" };
  }
  function shiftDay(day, amount) {
    const date = new Date(day + "T12:00:00Z");
    date.setUTCDate(date.getUTCDate() + amount);
    return date.toISOString().slice(0, 10);
  }
  function eventStatus(event, now = new Date()) {
    if (event.status === "confirmed") return "已确认";
    const schedule = event.schedule || {};
    const end = ui.parseDate(schedule.through);
    if (end && end < now) return "窗口已过 · 未确认";
    const start = ui.parseDate(schedule.from);
    if (start && start > now) return "已宣布 · 待生效";
    return "已宣布 · 待确认";
  }
  function monthCells(day) {
    const first = day.slice(0, 7) + "-01";
    const offset = (new Date(first + "T12:00:00Z").getUTCDay() + 6) % 7;
    return Array.from({ length: 42 }, (_, index) => shiftDay(first, index - offset));
  }
  window.SkillManagerResetCalendar = Object.freeze({ dateKey, eventDay, eventStatus, monthCells });

  const trigger = document.getElementById("reset-calendar-button");
  const panel = document.getElementById("reset-calendar-popover");
  if (!trigger || !panel) return;
  let selected = dateKey(new Date());
  let month = selected.slice(0, 7);
  let snapshot = null;
  let etag = "";
  let fetchedAt = 0;
  let loading = false;
  let failed = false;
  const node = (parent, tag, value, className) => {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (value != null) element.textContent = String(value);
    parent.append(element);
    return element;
  };
  function button(parent, label, action, key, className = "ghost small") {
    const element = node(parent, "button", label, className);
    element.type = "button";
    element.dataset.calendarFocus = key;
    element.addEventListener("click", action);
    return element;
  }
  function close(restoreFocus) {
    panel.hidden = true;
    trigger.setAttribute("aria-expanded", "false");
    if (restoreFocus) trigger.focus();
  }
  function focusDay() {
    const day = panel.querySelector(`[data-date="${selected}"]`);
    if (day) day.focus({ preventScroll: true });
  }
  function choose(day, focus = true) {
    selected = day;
    month = day.slice(0, 7);
    render();
    if (focus) focusDay();
  }
  function changeMonth(amount) {
    const date = new Date(month + "-01T12:00:00Z");
    date.setUTCMonth(date.getUTCMonth() + amount);
    month = date.toISOString().slice(0, 7);
    selected = month + "-01";
    render();
  }
  function renderEvent(parent, event) {
    const card = node(parent, "article", null, "reset-event");
    const label = event.displayLabel || (event.type === "reset_credit" ? "重置卡" : "额度重置");
    const status = eventStatus(event);
    node(card, "h4", `${label} · ${status}`);
    const placement = eventDay(event);
    node(card, "p", placement.basis + (event.scope ? " · " + event.scope : ""), "reset-note");
    if (event.title && event.title !== label) node(card, "p", event.title);
    // The original estimate is retained for context, never treated as execution time.
    if (event.schedule && event.schedule.label) node(card, "p", "原公告：" + event.schedule.label, "reset-note");
    const posts = Array.isArray(event.posts) ? event.posts : [];
    posts.slice(0, 3).forEach((post) => {
      const line = node(card, "div", null, "reset-post");
      if (post.text) node(line, "p", post.text);
      const url = ui.safeHTTPURL(post.url);
      if (url) {
        const link = button(line, post.stage ? post.stage + " · 原帖 ↗" : "查看原帖 ↗", () => window.SkillManagerAPI.openExternal(url), "post-" + event.id + "-" + post.id, "reset-source-link");
        link.title = url;
      }
    });
  }
  function render() {
    const focusKey = panel.contains(document.activeElement) && document.activeElement.dataset.calendarFocus;
    panel.replaceChildren();
    const head = node(panel, "div", null, "reset-calendar-head");
    const title = node(head, "h2", "重置日历");
    title.id = "reset-calendar-title";
    node(head, "span", "Codex · 北京时间", "reset-note");
    const x = button(head, "×", () => close(true), "close", "reset-close ghost");
    x.setAttribute("aria-label", "关闭重置日历");

    const status = node(panel, "div", null, "reset-calendar-status reset-note");
    status.setAttribute("role", "status");
    if (loading) status.textContent = snapshot ? "正在检查更新…" : "正在加载重置记录…";
    else if (failed || snapshot && snapshot.meta && snapshot.meta.stale) status.textContent = snapshot ? "当前显示缓存，暂未取得最新记录。" : "暂时无法加载重置记录。";
    else if (snapshot) {
      const checked = ui.parseDate(snapshot.checkedAt);
      status.textContent = checked ? "AIHOT · 核验于 " + new Intl.DateTimeFormat("zh-CN", { timeZone: zone, month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false }).format(checked) : "数据来自 AIHOT";
    }
    if (!loading && (failed || snapshot && snapshot.meta && snapshot.meta.stale)) button(status, "重试", () => load(true), "retry", "reset-source-link");

    const toolbar = node(panel, "div", null, "reset-calendar-toolbar");
    const previous = button(toolbar, "‹", () => changeMonth(-1), "previous");
    previous.setAttribute("aria-label", "上个月");
    node(toolbar, "h3", `${month.slice(0, 4)} 年 ${Number(month.slice(5))} 月`);
    button(toolbar, "今天", () => choose(dateKey(new Date())), "today");
    const next = button(toolbar, "›", () => changeMonth(1), "next");
    next.setAttribute("aria-label", "下个月");
    const week = node(panel, "div", null, "reset-calendar-week");
    week.setAttribute("aria-hidden", "true");
    ["一", "二", "三", "四", "五", "六", "日"].forEach((day) => node(week, "span", day));
    const events = snapshot && Array.isArray(snapshot.events) ? snapshot.events : [];
    const byDay = new Map();
    events.forEach((event) => {
      const date = eventDay(event).date;
      if (!byDay.has(date)) byDay.set(date, []);
      byDay.get(date).push(event);
    });
    const today = dateKey(new Date());
    const grid = node(panel, "div", null, "reset-calendar-grid");
    grid.setAttribute("role", "group");
    grid.setAttribute("aria-label", "选择日期，方向键移动，PageUp 和 PageDown 切月");
    monthCells(month + "-01").forEach((date) => {
      const rows = byDay.get(date) || [];
      const cell = button(grid, null, () => choose(date), date, "reset-day");
      cell.dataset.date = date;
      cell.tabIndex = date === selected ? 0 : -1;
      cell.setAttribute("aria-pressed", String(date === selected));
      cell.setAttribute("aria-label", `${date}${date === today ? "，今天" : ""}，${snapshot ? rows.length + " 条记录" : "记录尚未加载"}`);
      if (date === today) cell.setAttribute("aria-current", "date");
      if (!date.startsWith(month)) cell.classList.add("is-outside");
      node(cell, "span", Number(date.slice(8)), "reset-day-number");
      if (rows.length) {
        const pending = rows.some((event) => event.status !== "confirmed");
        cell.classList.add(pending ? "has-pending" : "has-reset");
        const kinds = new Set(rows.map((event) => event.type));
        if (kinds.size === 1 && kinds.has("reset_credit")) cell.classList.add("has-credit");
        if (kinds.size > 1) cell.classList.add("has-mixed");
        const label = kinds.size > 1 ? "重置 · 卡" : kinds.has("reset_credit") ? "重置卡" : "额度重置";
        node(cell, "span", pending ? (rows.some((event) => eventStatus(event).endsWith("待生效")) ? "待生效" : "待确认") : label, "reset-day-marker");
      }
      cell.addEventListener("keydown", (event) => {
        const moves = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 };
        if (event.key in moves) { event.preventDefault(); choose(shiftDay(date, moves[event.key])); }
        else if (event.key === "PageUp" || event.key === "PageDown") { event.preventDefault(); changeMonth(event.key === "PageUp" ? -1 : 1); focusDay(); }
        else if (event.key === "Home" || event.key === "End") {
          event.preventDefault();
          const offset = (new Date(date + "T12:00:00Z").getUTCDay() + 6) % 7;
          choose(shiftDay(date, event.key === "Home" ? -offset : 6 - offset));
        }
      });
    });
    const legend = node(panel, "div", null, "reset-calendar-legend reset-note");
    node(legend, "span", "● 额度重置", "reset-confirmed");
    node(legend, "span", "● 重置卡", "reset-credit");
    node(legend, "span", "○ 公告待确认", "reset-pending");
    const detail = node(panel, "section", null, "reset-calendar-detail");
    detail.setAttribute("aria-live", "polite");
    node(detail, "h3", `${Number(selected.slice(5, 7))} 月 ${Number(selected.slice(8))} 日${selected === today ? " · 今天" : ""}`);
    if (snapshot) {
      const rows = byDay.get(selected) || [];
      if (!rows.length) node(detail, "p", events.some((event) => eventDay(event).date.startsWith(month)) ? "这一天没有公开记录。" : "本月暂无公开记录。", "reset-empty reset-note");
      rows.forEach((event) => renderEvent(detail, event));
      const undated = byDay.get("") || [];
      if (undated.length) {
        const disclosure = node(detail, "details", null, "reset-undated");
        node(disclosure, "summary", `日期待确认 · ${undated.length} 条`);
        undated.forEach((event) => renderEvent(disclosure, event));
      }
    }
    const history = snapshot && dateKey(snapshot.historyFrom);
    node(panel, "p", (history ? `公开记录始于 ${history}。` : "") + "按已核实日期或原帖日期归档；确认帖时间不代表实际到账时间。", "reset-calendar-foot reset-note");
    if (focusKey) {
      const target = Array.from(panel.querySelectorAll("[data-calendar-focus]")).find((element) => element.dataset.calendarFocus === focusKey);
      if (target) target.focus({ preventScroll: true });
    }
  }
  async function load(force = false) {
    if (loading || !force && snapshot && Date.now() - fetchedAt < 60_000) return;
    loading = true;
    failed = false;
    render();
    try {
      const response = await window.SkillManagerAPI.request("GET", "/api/content/reset-calendar", undefined, { withMeta: true, headers: etag ? { "If-None-Match": etag } : {} });
      if (response.status === 304) {
        if (!snapshot) throw new Error("Missing cached calendar");
      } else {
        if (!response.data || response.data.timezone !== zone || !Array.isArray(response.data.events)) throw new Error("Invalid calendar");
        snapshot = response.data;
      }
      etag = response.etag || etag;
      fetchedAt = Date.now();
    } catch { failed = true; }
    finally { loading = false; render(); }
  }
  trigger.addEventListener("click", () => {
    if (!panel.hidden) { close(true); return; }
    panel.hidden = false;
    panel.style.top = Math.round(document.querySelector("header").getBoundingClientRect().bottom + 8) + "px";
    trigger.setAttribute("aria-expanded", "true");
    render();
    load();
    focusDay();
  });
  document.addEventListener("click", (event) => {
    // Rendering can replace the clicked date before this event reaches document.
    const path = event.composedPath();
    if (!panel.hidden && !path.includes(panel) && !path.includes(trigger)) close(false);
  });
  document.addEventListener("keydown", (event) => {
    if (!panel.hidden && event.key === "Escape") { event.preventDefault(); event.stopPropagation(); close(true); }
  });
  document.addEventListener("focusin", (event) => {
    if (!panel.hidden && !panel.contains(event.target) && !trigger.contains(event.target)) close(false);
  });
  window.addEventListener("resize", () => {
    if (!panel.hidden) panel.style.top = Math.round(document.querySelector("header").getBoundingClientRect().bottom + 8) + "px";
  });
})();
