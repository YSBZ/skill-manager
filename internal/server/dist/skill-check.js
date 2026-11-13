"use strict";
(() => {
  const NOTE = "仅检查本地文件与明确依赖，未运行验证；不代表实际执行成功。";
  function createRequestScope() {
    let version = 0;
    return { begin: () => ++version, isCurrent: value => value === version, cancel: () => { version++; } };
  }
  function fingerprint(report) {
    if (!report) return "";
    return JSON.stringify({ state: report.state, path: report.path || {}, dependencies: report.dependencies || [],
      compatibility: report.compatibility || "", allowedTools: report.allowedTools || [], findings: report.findings || [] });
  }
  function badge(document, report) {
    const el = document.createElement("span");
    const state = report?.state || "unknown";
    const findings = report?.findings || [];
    const count = findings.filter(f => state === "issues" ? f.level === "error" : f.level === "warning").length;
    el.className = "skill-check-badge skill-check-" + state;
    el.textContent = state === "checked" ? "本地检查通过" : state === "issues" ? "待处理 " + count : state === "review" ? "需确认 " + count : "尚未检查";
    el.title = NOTE;
    return el;
  }
  function render(document, host, options = {}) {
    host.replaceChildren();
    const node = (tag, text, className) => {
      const el = document.createElement(tag);
      if (text !== undefined) el.textContent = text;
      if (className) el.className = className;
      return el;
    };
    const header = node("div", undefined, "skill-check-head");
    header.append(node("h4", "本地检查"));
    if (options.onRefresh) {
      const refresh = node("button", "重新检查", "ghost small");
      refresh.type = "button";
      refresh.disabled = !!options.loading;
      refresh.onclick = options.onRefresh;
      header.append(refresh);
    }
    host.append(header, node("p", NOTE, "skill-check-note"));
    if (options.notice) { host.append(node("p", options.notice, "muted")); return; }
    if (options.loading) { host.append(node("p", "检查中…", "muted")); return; }
    if (options.error) { host.append(node("p", "检查失败：" + options.error, "skill-check-error")); return; }
    const report = options.report;
    if (!report) { host.append(node("p", "暂无检查数据。可重新检查。", "muted")); return; }
    host.append(badge(document, report));
    const fields = node("dl", undefined, "skill-check-paths");
    const field = (label, value) => { fields.append(node("dt", label), node("dd", value || "—")); };
    const p = report.path || {};
    field("入口", p.entry); field("链接目标", p.linkTarget); field("真实路径", p.resolved);
    const states = { ok: "可读取", broken: "链接已断开", unreadable: "无法读取", missing_skill: "缺少 SKILL.md" };
    field("路径状态", states[p.state] || p.state);
    host.append(fields);
    if (p.state === "broken") host.append(node("p", "软链来源已不可用。请恢复来源目录，或使用详情操作移除链接（只删除软链）。", "skill-check-guidance"));
    else if (p.state === "missing_skill") host.append(node("p", "目标没有 SKILL.md。请恢复来源中的 SKILL.md，或使用详情操作移除链接。", "skill-check-guidance"));
    host.append(node("h5", "明确依赖"));
    const dependencies = report.dependencies || [];
    if (!dependencies.length) host.append(node("p", "未识别到明确的 Skill 依赖。", "muted"));
    dependencies.forEach(dep => {
      const row = node("div", undefined, "skill-check-dependency");
      row.append(node("strong", dep.name), node("span", dep.state === "present" ? "可发现" : dep.state === "missing" ? "缺失" : "需确认", "skill-check-dependency-state"));
      if (dep.source) row.append(node("p", "来源：" + dep.source, "skill-check-evidence"));
      if (dep.evidence) row.append(node("pre", dep.evidence, "skill-check-evidence"));
      if (dep.state === "missing" && options.onSearch) {
        const search = node("button", "查找依赖", "ghost small");
        search.type = "button"; search.onclick = () => options.onSearch(dep.name); row.append(search);
      }
      host.append(row);
    });
    if ((report.findings || []).length) {
      host.append(node("h5", "检查发现"));
      report.findings.forEach(f => {
        const row = node("div", undefined, "skill-check-finding skill-check-level-" + f.level);
        row.append(node("p", f.message));
        if (f.evidence) row.append(node("pre", f.evidence, "skill-check-evidence"));
        host.append(row);
      });
    }
    host.append(node("h5", "compatibility 原文"), node("pre", report.compatibility || "未声明", "skill-check-evidence"));
    const tools = Array.isArray(report.allowedTools) ? report.allowedTools.join("\n") : report.allowedTools;
    host.append(node("h5", "allowed-tools 原文（预批准工具，非必需依赖）"), node("pre", tools || "未声明", "skill-check-evidence"));
  }
  function createController(document, host, options) {
    const scope = createRequestScope();
    let current = null;
    async function run(refresh) {
      const params = current;
      if (!params) return;
      const token = scope.begin();
      const paint = data => render(document, host, { ...data, onSearch: options.onSearch, onRefresh: () => run(true) });
      if (!params.target) { paint({ notice: "选择同步目录后可检查依赖与客户端。" }); return; }
      paint({ loading: true });
      try {
        if (refresh && options.onRefresh) await options.onRefresh(params);
        if (!scope.isCurrent(token)) return;
        const report = await options.request(new URLSearchParams(params).toString());
        if (scope.isCurrent(token)) paint({ report });
      } catch (error) {
        if (scope.isCurrent(token)) paint({ error: error.message || String(error) });
      }
    }
    return { open(params) { current = { ...params }; return run(false); }, cancel() { current = null; scope.cancel(); } };
  }
  window.SkillManagerSkillCheck = Object.freeze({ badge, render, fingerprint, createController, createRequestScope });
})();
