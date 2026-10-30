"use strict";

(() => {
  const content = window.SkillManagerContent;
  const contentUI = window.SkillManagerContentUI;
  const byId = (id) => document.getElementById(id);
  const entryIDPattern = /^[A-Za-z0-9_-]{1,160}$/;
  const skillNamePattern = /^[A-Za-z0-9_][A-Za-z0-9._-]*$/;
  const safeHTTPURL = contentUI.safeHTTPURL;
  const scenes = [
    { value: "development", label: "开发" },
    { value: "design", label: "设计" },
    { value: "testing", label: "测试" },
    { value: "documentation", label: "文档" },
    { value: "agent-workflow", label: "Agent 工作流" },
  ];
  const typeLabels = { skill: "Skill 项目", agent: "Agent 项目", tool: "工具项目" };
  const sceneLabels = Object.fromEntries(scenes.map((scene) => [scene.value, scene.label]));

  function text(parent, tag, value, className) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    node.textContent = String(value == null ? "" : value);
    parent.append(node);
    return node;
  }

  function externalButton(parent, label, rawURL) {
    const url = safeHTTPURL(rawURL);
    if (!url || !window.SkillManagerAPI || typeof window.SkillManagerAPI.openExternal !== "function") return null;
    const button = document.createElement("button");
    button.type = "button";
    button.className = "content-external-action";
    button.textContent = label;
    button.addEventListener("click", () => window.SkillManagerAPI.openExternal(url));
    parent.append(button);
    return button;
  }

  function parsedDate(raw) {
    return contentUI.parseDate(raw);
  }

  function validDate(raw) {
    const date = parsedDate(raw);
    return date ? date.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" }) : "";
  }

  function localizedDateTime(raw) {
    const date = parsedDate(raw);
    return date ? date.toLocaleString(undefined, {
      year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: "UTC",
    }) + " UTC" : "";
  }

  function growthWindowDate(raw) {
    return localizedDateTime(raw);
  }

  function typeLabel(type) {
    return typeLabels[String(type || "").toLowerCase()] || "其他项目";
  }

  function sourceStateLabel(source) {
    const state = source && source.state;
    if (source && source.name === "github" && state === "stale" && !parsedDate(source.lastSuccessAt)) return "尚无可用数据";
    return ({ fresh: "已更新", stale: "缓存内容", rate_limited: "等待更新", unavailable: "暂不可用" })[state] || "状态未知";
  }

  function githubSource(meta) {
    const sources = meta && Array.isArray(meta.sources) ? meta.sources : [];
    return sources.find((source) => String(source && source.name || "").toLowerCase() === "github") || null;
  }

  function renderGitHubFreshness(parent, meta) {
    const github = githubSource(meta);
    const parts = ["GitHub 数据状态：" + (github ? sourceStateLabel(github) : "状态未知")];
    const updated = localizedDateTime(github && github.lastSuccessAt);
    if (updated) parts.push("最近成功更新 " + updated);
    text(parent, "small", parts.join("；"), "discovery-github-state");
  }

  function coverageLabel(coverage) {
    if (!coverage || typeof coverage !== "object") return "";
    const candidates = hasNumber(coverage.candidates) ? coverage.candidates : NaN;
    const complete = hasNumber(coverage.completeGrowth) ? coverage.completeGrowth : NaN;
    if (!Number.isFinite(candidates) && !Number.isFinite(complete)) return "";
    const facts = [];
    if (Number.isFinite(candidates) && candidates >= 0) facts.push(candidates + " 个候选项目");
    if (Number.isFinite(complete) && complete >= 0) facts.push(complete + " 个有完整周数据");
    return facts.length ? "已收录范围内 " + facts.join("，") + "。" : "";
  }

  function renderCoverage(parent, coverage) {
    const label = coverageLabel(coverage);
    if (label) text(parent, "small", label, "discovery-coverage");
  }

  function signedValue(value) {
    if (!hasNumber(value)) return "";
    const number = Number(value);
    if (number > 0) return "+" + number;
    if (number < 0) return "−" + Math.abs(number);
    return "0";
  }

  function hasNumber(value) {
    return typeof value === "number" && Number.isFinite(value);
  }

  function growthLabel(growth, includeDetails = true) {
    if (!growth || typeof growth !== "object") return "暂无有效增长数据。";
    const hasDays = Number.isInteger(growth.observedDays) && growth.observedDays >= 0;
    const days = hasDays ? growth.observedDays : 0;
    const range = includeDetails ? [growthWindowDate(growth.windowStart), growthWindowDate(growth.windowEnd)].filter(Boolean).join(" 至 ") : "";
    const value = signedValue(growth.value);
    if (growth.metric === "stars_added") {
      if (!value) return "暂无有效增长数据。";
      const label = growth.complete === true && hasDays && days === 7
        ? "近 7 日新增 Star"
        : !hasDays ? "观察时长未记录的新增 Star" : days === 0 ? "观察不足 1 天新增 Star" : "观察 " + days + " 天新增 Star";
      return (includeDetails ? "GitHub 历史：" : "") + label + " " + value + (range ? "（" + range + "）" : "");
    }
    if (growth.metric === "stars_net") {
      if (!value) return "暂无有效增长数据。";
      const label = !hasDays ? "观察时长未记录的净增" : days === 0 ? "观察不足 1 天净增" : "观察 " + days + " 天净增";
      return (includeDetails ? "本地快照：" : "") + label + " " + value + (range ? "（" + range + "）" : "");
    }
    return "暂无有效增长数据。";
  }

  function renderGrowth(parent, growth, includeDetails = true) {
    text(parent, "small", growthLabel(growth, includeDetails), "discovery-growth");
  }

  function sceneLabel(scene) {
    return sceneLabels[String(scene || "")] || "";
  }

  function renderScenes(parent, values) {
    if (!Array.isArray(values)) return;
    const labels = values.map(sceneLabel).filter(Boolean);
    if (labels.length) text(parent, "small", "适用场景：" + labels.join("、"), "discovery-scenes");
  }

  function renderProjectCardFacts(parent, entry) {
    const project = entry && entry.project && typeof entry.project === "object" ? entry.project : {};
    text(parent, "small", typeLabel(project.type) + (project.repo ? " · " + project.repo : ""), "discovery-project-type");
    if (project.type === "skill" && Array.isArray(project.skills)) {
      text(parent, "small", "Skill 引用：" + project.skills.length, "discovery-skill-count");
    }
    renderGrowth(parent, project.growth, false);
  }

  function setDiscoveryLayout(hasSelection, hasItems) {
    const compact = !hasSelection && !hasItems;
    byId("discovery-columns").setAttribute("data-layout", compact ? "compact" : "reader");
    byId("discovery-detail").hidden = compact;
  }

  function renderPageControls(parent, page, context) {
    const hasPrevious = Array.isArray(context.query.cursorStack) && context.query.cursorStack.length > 0;
    if (!hasPrevious && !(page && page.nextCursor)) return;
    const controls = document.createElement("nav");
    controls.className = "content-pagination";
    controls.setAttribute("aria-label", "项目分页");
    const previous = document.createElement("button");
    previous.type = "button";
    previous.textContent = "上一页";
    previous.disabled = !hasPrevious;
    previous.addEventListener("click", () => {
      if (!previous.disabled) {
        byId("discovery-cards").scrollTop = 0;
        content.previousFeedPage();
      }
    });
    const next = document.createElement("button");
    next.type = "button";
    next.textContent = "下一页";
    next.disabled = !(page && page.nextCursor);
    next.addEventListener("click", () => {
      if (!next.disabled) {
        byId("discovery-cards").scrollTop = 0;
        content.nextFeedPage();
      }
    });
    controls.append(previous, next);
    parent.append(controls);
  }

  function updateControls(query) {
    document.querySelectorAll("[data-discovery-view]").forEach((button) => {
      button.setAttribute("aria-current", String(button.dataset.discoveryView === query.view ? "page" : "false"));
    });
    const search = byId("discovery-search-query");
    if (document.activeElement !== search) search.value = query.q || "";
    const scene = byId("discovery-scene-filter");
    if (document.activeElement !== scene) scene.value = query.scene || "";
  }

  function renderFeed(page, stale, context) {
    const cards = byId("discovery-cards");
    const notices = byId("discovery-meta-notices");
    const previousScroll = cards.scrollTop;
    const items = Array.isArray(page && page.items) ? page.items : [];
    const meta = page && page.meta && typeof page.meta === "object" ? page.meta : {};
    setDiscoveryLayout(!!context.selectedId, items.length > 0);
    cards.replaceChildren();
    notices.replaceChildren();
    notices.setAttribute("role", "region");
    notices.setAttribute("aria-label", "列表来源通知");
    notices.setAttribute("tabindex", "0");
    updateControls(context.query);
    if (stale || meta.stale) text(notices, "p", "当前列表来自缓存。", "content-notice");
    renderCoverage(notices, meta.coverage);
    const github = githubSource(meta);
    text(notices, "p", "GitHub 数据状态：" + (github ? sourceStateLabel(github) : "状态未知"), "content-notice");
    (Array.isArray(meta.notices) ? meta.notices : []).forEach((notice) => {
      const message = notice && typeof notice === "object" ? (notice.message || notice.text) : typeof notice === "string" ? notice : "";
      if (typeof message === "string" && message.trim()) text(notices, "p", message, "content-notice");
    });

    if (context.query.view === "weekly" && items.length && items.every((entry) => entry && entry.project && entry.project.growth && entry.project.growth.metric === "stars_net" && entry.project.growth.complete === true && entry.project.growth.observedDays === 7)) {
      text(cards, "h2", "近 7 日净增 Star 榜", "discovery-ranking-title");
      text(cards, "p", "基于同一完整 7 日窗口的本地 Star 快照；净增包含取消 Star。", "content-notice");
    }

    items.forEach((entry) => {
      if (!entry || !entryIDPattern.test(String(entry.id || ""))) return;
      const card = document.createElement("button");
      card.type = "button";
      card.className = "content-card discovery-project-card";
      card.dataset.entryId = entry.id;
      card.setAttribute("aria-pressed", String(entry.id === context.selectedId));
      text(card, "strong", String(entry.title || "未命名项目"), "discovery-card-title");
      const capability = String(entry.summary || entry.reason || "暂无项目说明。");
      text(card, "p", capability, "content-card-summary");
      renderProjectCardFacts(card, entry);
      card.addEventListener("click", () => {
        Array.from(cards.children).forEach((candidate) => {
          if (candidate && candidate.dataset && candidate.dataset.entryId) candidate.setAttribute("aria-pressed", String(candidate.dataset.entryId === entry.id));
        });
        content.selectEntry(entry.id);
      });
      cards.append(card);
    });

    if (!items.length) renderEmptyState(cards, context, meta);
    renderPageControls(cards, page || {}, context);
    cards.scrollTop = previousScroll;
    if (!context.selectedId && items.length) renderMessage("选择一个项目，查看能力说明、来源与可安装 Skill。", context);
  }

  function renderEmptyState(parent, context, meta) {
    const noFilters = !String(context.query.q || "").trim() && !String(context.query.scene || "");
    const noFullWeeks = !!(meta && meta.coverage && hasNumber(meta.coverage.completeGrowth) && meta.coverage.completeGrowth === 0);
    const message = context.query.view === "weekly" && noFilters && noFullWeeks
      ? "正在积累周数据；目前没有可展示的完整 7 日增长记录。"
      : "当前条件下没有匹配的项目。";
    const empty = document.createElement("div");
    empty.className = "content-empty discovery-empty";
    text(empty, "p", message);
    if (context.query.view === "weekly") {
      const links = document.createElement("div");
      links.className = "discovery-empty-actions";
      [
        ["新项目发现", "new"],
        ["场景探索", "scenarios"],
      ].forEach(([label, view]) => {
        const button = document.createElement("button");
        button.type = "button";
        button.textContent = label;
        button.addEventListener("click", () => content.setFeedFilter("view", view));
        links.append(button);
      });
      empty.append(links);
    }
    parent.append(empty);
  }

  function renderEntry(entry, meta) {
    const root = byId("discovery-detail");
    const value = entry && typeof entry === "object" ? entry : {};
    const project = value.project && typeof value.project === "object" ? value.project : {};
    const metadata = meta && typeof meta === "object" ? meta : {};
    setDiscoveryLayout(true, true);
    root.replaceChildren();
    if (metadata.stale) text(root, "p", "详情来自缓存。", "content-notice");
    text(root, "h2", String(value.title || "未命名项目"));
    const isSkillProject = project.type === "skill";
    if (isSkillProject) externalButton(root, "查看项目说明", value.canonicalUrl);
    text(root, "p", String(value.summary || value.reason || "暂无项目说明。"));

    if (isSkillProject) {
      renderSkillRefs(root, value, project);
      const info = document.createElement("details");
      info.className = "discovery-project-info";
      text(info, "summary", "项目与增长信息");
      renderProjectInfo(info, value, project, metadata);
      root.append(info);
    } else {
      renderProjectInfo(root, value, project, metadata);
      externalButton(root, "查看项目说明", value.canonicalUrl);
    }

    (Array.isArray(value.paragraphs) ? value.paragraphs : []).forEach((paragraph) => {
      if (!paragraph || typeof paragraph !== "object") return;
      const section = document.createElement("section");
      if (paragraph.heading) text(section, "h3", paragraph.heading);
      if (typeof paragraph.text === "string" && paragraph.text) text(section, "p", paragraph.text);
      externalButton(section, "查看来源", paragraph.url || (paragraph.source && paragraph.source.url) || (paragraph.attribution && paragraph.attribution.url));
      root.append(section);
    });
    (Array.isArray(metadata.notices) ? metadata.notices : []).forEach((notice) => {
      const message = notice && typeof notice === "object" ? (notice.message || notice.text) : typeof notice === "string" ? notice : "";
      if (typeof message === "string" && message.trim()) text(root, "p", message, "content-notice");
    });
  }

  function renderProjectInfo(parent, entry, project, metadata) {
    text(parent, "p", "项目类型：" + typeLabel(project.type));
    text(parent, "p", "仓库：" + String(project.repo || "未知"));
    if (hasNumber(project.stars)) text(parent, "p", "总 Star：" + Number(project.stars) + "（仓库级）");
    const created = validDate(project.createdAt);
    const discovered = validDate(entry.discoveredAt);
    if (created) text(parent, "p", "项目创建：" + created);
    if (discovered) text(parent, "p", "首次收录：" + discovered);
    renderGrowth(parent, project.growth);
    renderScenes(parent, project.scenes);
    renderGitHubFreshness(parent, metadata);
    renderCoverage(parent, metadata.coverage);
  }

  function safeSkillPath(value) {
    if (typeof value !== "string" || !value || value.startsWith("/") || value.includes("\\")) return false;
    return value.split("/").every((segment) => segment && segment !== "." && segment !== "..");
  }

  function validSkillRef(entry, project, ref, duplicateCount) {
    return project.type === "skill" &&
      entryIDPattern.test(String(entry.id || "")) &&
      ref && ref.verified === true &&
      Number.isSafeInteger(project.repositoryId) && project.repositoryId > 0 &&
      ref.repositoryId === project.repositoryId &&
      typeof project.repo === "string" && typeof ref.repo === "string" && ref.repo.toLowerCase() === project.repo.toLowerCase() &&
      typeof ref.name === "string" && skillNamePattern.test(ref.name) && duplicateCount === 1 &&
      safeSkillPath(ref.path);
  }

  function renderSkillRefs(root, entry, project) {
    const refs = Array.isArray(project.skills) ? project.skills : [];
    const counts = new Map();
    refs.forEach((ref) => {
      if (!ref || !ref.name || !Number.isSafeInteger(ref.repositoryId)) return;
      const key = String(ref.repositoryId) + ":" + String(ref.name).toLowerCase();
      counts.set(key, (counts.get(key) || 0) + 1);
    });
    const list = document.createElement("section");
    list.className = "discovery-skill-list";
    text(list, "h3", "仓库内的 Skill（" + refs.length + "）");
    if (!refs.length) {
      text(list, "p", "仓库暂未提供经过验证的 Skill。", "content-empty");
      root.append(list);
      return;
    }
    refs.forEach((ref) => {
      if (!ref || typeof ref !== "object") return;
      const row = document.createElement("section");
      row.className = "discovery-skill-ref";
      const identity = document.createElement("div");
      identity.className = "discovery-skill-identity";
      text(identity, "strong", String(ref.name || "未命名 Skill"));
      text(identity, "small", String(ref.path || "路径缺失"), "discovery-skill-path");
      row.append(identity);
      const key = String(ref.repositoryId) + ":" + String(ref.name || "").toLowerCase();
      const duplicateCount = counts.get(key) || 0;
      if (validSkillRef(entry, project, ref, duplicateCount)) {
        const install = document.createElement("button");
        install.type = "button";
        install.textContent = "安装 Skill";
        install.addEventListener("click", () => installSkill(install, row, entry, ref));
        row.append(install);
      } else {
        const reason = duplicateCount > 1 ? "同名路径不唯一，暂不提供安装。" : ref.verified === true ? "项目或路径身份不匹配，暂不提供安装。" : "尚未验证，暂不提供安装。";
        text(row, "small", reason, "discovery-install-reason");
      }
      list.append(row);
    });
    root.append(list);
  }

  function exactInstalledSkill(skills, entry, ref) {
    if (!Array.isArray(skills)) return null;
    return skills.find((skill) => {
      const identity = skill && skill.contentIdentity;
      return skill && skill.identityStatus === "exact" && typeof skill.linkName === "string" && skill.linkName &&
        identity && typeof identity.canonicalDir === "string" && identity.canonicalDir &&
        identity.repositoryId === ref.repositoryId && identity.path === ref.path && identity.name === ref.name &&
        identity.repositoryId === entry.project.repositoryId && typeof identity.sourceRepo === "string" &&
        identity.sourceRepo.toLowerCase() === String(entry.project.repo || "").toLowerCase();
    }) || null;
  }

  async function refreshSkillsSh() {
    if (typeof window.SkillManagerAPI.refreshSkillsSh === "function") {
      return window.SkillManagerAPI.refreshSkillsSh();
    }
    return window.SkillManagerAPI.request("GET", "/api/skillssh");
  }

  async function isEnabled(linkName, target, installed) {
    const canonicalDir = installed && installed.contentIdentity && installed.contentIdentity.canonicalDir;
    return !!target && !!canonicalDir && typeof window.SkillManagerAPI.isSkillEnabled === "function" &&
      await window.SkillManagerAPI.isSkillEnabled(linkName, target, canonicalDir);
  }

  function applyFailureMessage(summary, linkName, target) {
    if (!summary || typeof summary !== "object") return "启用结果尚未确认。";
    const errors = Array.isArray(summary.errors) ? summary.errors.map((item) => String(item || "").trim()).filter(Boolean) : [];
    if (errors.length) return "启用失败：" + errors.join("；");
    const collisions = Array.isArray(summary.conflicts) ? summary.conflicts.filter((conflict) =>
      conflict && conflict.kind === "collision" && conflict.linkName === linkName &&
      Array.isArray(conflict.targets) && conflict.targets.includes(target)) : [];
    if (collisions.length) return "目标存在同名冲突，启用未完成。";
    return "";
  }

  function installStatusMessage(identityStatus) {
    if (identityStatus === "source_same_pending") return "来源相同，安装路径待核对；尚未启用。";
    if (identityStatus === "conflict") return "检测到同名或来源冲突，安装身份尚未确认。";
    if (identityStatus === "not_match") return "安装内容与所选 Skill 不匹配，尚未启用。";
    return "Skill 身份尚未核验；未确认安装状态，也未启用。";
  }

  function enableAction(row, status, installed) {
    const api = window.SkillManagerAPI;
    const target = typeof api.currentTarget === "function" ? String(api.currentTarget() || "") : "";
    const enable = document.createElement("button");
    enable.type = "button";
    enable.textContent = target ? "启用到当前目录" : "先选择目标目录后启用";
    enable.disabled = !target;
    enable.addEventListener("click", async () => {
      const currentTarget = typeof api.currentTarget === "function" ? String(api.currentTarget() || "") : "";
      if (!currentTarget) {
        status.textContent = "已安装 · 未启用。请先选择目标目录。";
        return;
      }
      enable.disabled = true;
      enable.textContent = "正在启用…";
      try {
        await api.request("POST", "/api/enabled", {
          skill: "@agents/" + installed.linkName,
          target: currentTarget,
          mode: "snapshot",
        });
        const summary = await api.request("POST", "/api/apply");
        await refreshSkillsSh();
        const applyFailure = applyFailureMessage(summary, installed.linkName, currentTarget);
        if (applyFailure) {
          status.textContent = "已安装 · 未启用。" + applyFailure;
          enable.disabled = false;
          enable.textContent = "重试启用";
        } else if (await isEnabled(installed.linkName, currentTarget, installed)) {
          status.textContent = "已安装 · 已启用到当前目录。";
          enable.remove();
        } else {
          status.textContent = "已安装 · 未启用。启用状态尚未确认。";
          enable.disabled = false;
          enable.textContent = "重试启用";
        }
      } catch (error) {
        status.textContent = "已安装 · 未启用。启用失败：" + String(error && error.message || error || "未知错误");
        enable.disabled = false;
        enable.textContent = "重试启用";
      }
    });
    row.append(enable);
  }

  async function installSkill(button, row, entry, ref) {
    if (!window.SkillManagerAPI || typeof window.SkillManagerAPI.request !== "function") return;
    if (window.SkillManagerNodeDependencyUI && !window.SkillManagerNodeDependencyUI.requireNode(button)) return;
    button.disabled = true;
    button.textContent = "正在核对…";
    const status = document.createElement("small");
    status.className = "discovery-install-status";
    try {
      const result = await window.SkillManagerAPI.request("POST", "/api/skillssh/add", {
        contentIdentity: { entryId: entry.id, repositoryId: ref.repositoryId, path: ref.path, name: ref.name },
      });
      if (!result || result.ok !== true) {
        button.disabled = false;
        button.textContent = result && result.identityStatus ? "身份待核验" : "重试安装";
        status.textContent = result && result.identityStatus
          ? installStatusMessage(result.identityStatus)
          : "安装未确认：" + String(result && result.error || "安装请求未完成");
      } else {
        const skills = await refreshSkillsSh();
        const installed = result.identityStatus === "exact" ? exactInstalledSkill(skills, entry, ref) : null;
        if (!installed) {
          button.disabled = false;
          button.textContent = "身份待核验";
          status.textContent = installStatusMessage(result.identityStatus);
          if (result.identityStatus === "exact") status.textContent = "安装响应已核实，但实时 skills.sh 列表未确认此身份；尚未启用。";
        } else {
          const target = typeof window.SkillManagerAPI.currentTarget === "function" ? String(window.SkillManagerAPI.currentTarget() || "") : "";
          button.disabled = true;
          button.textContent = "已安装";
          const enabled = await isEnabled(installed.linkName, target, installed);
          status.textContent = enabled ? "已安装 · 已启用到当前目录。" : "已安装 · 未启用。";
          if (!enabled) enableAction(row, status, installed);
        }
      }
      if (result && result.ok === true && result.identityStatus !== "exact") {
        button.disabled = false;
        button.textContent = result.identityStatus === "conflict" ? "身份冲突" : "身份待核验";
      }
    } catch (error) {
      button.disabled = false;
      button.textContent = "重试安装";
      status.textContent = "安装未确认：" + String(error && error.message || error || "未知错误");
    }
    Array.from(row.children).filter((child) => child.className === "discovery-install-status").forEach((child) => child.remove());
    row.append(status);
  }

  function renderMessage(message) {
    const root = byId("discovery-detail");
    root.replaceChildren();
    text(root, "p", message || "选择一个项目查看详情。", "content-empty");
  }

  function renderError(message, context) {
    const cards = byId("discovery-cards");
    const notices = byId("discovery-meta-notices");
    setDiscoveryLayout(!!(context && context.selectedId), false);
    cards.replaceChildren();
    notices.replaceChildren();
    text(cards, "p", "首次收录暂时不可用，当前没有可展示的项目列表。", "content-notice discovery-error-message");
    text(cards, "p", "请重试，或使用上方“搜索技能市场”查找现成 Skill。", "discovery-error-help");
    const retry = document.createElement("button");
    retry.type = "button";
    retry.className = "ghost small discovery-error-retry";
    retry.textContent = "重试";
    retry.addEventListener("click", () => content.refreshFeed(true));
    cards.append(retry);
  }

  document.querySelectorAll("[data-discovery-view]").forEach((button) => {
    button.addEventListener("click", () => content.setFeedFilter("view", button.dataset.discoveryView));
  });
  byId("discovery-search-form").addEventListener("submit", (event) => {
    event.preventDefault();
    content.setFeedFilter("q", byId("discovery-search-query").value);
  });
  byId("discovery-scene-filter").addEventListener("change", (event) => {
    content.setFeedFilter("scene", event.target.value);
  });
  byId("discovery-market-search").addEventListener("click", () => {
    const term = String(byId("discovery-search-query").value || "").trim().slice(0, 200);
    if (window.SkillManagerAPI && typeof window.SkillManagerAPI.searchSkills === "function") window.SkillManagerAPI.searchSkills(term);
    else content.showMarketSearch();
  });

  content.registerRenderer("discovery", {
    feed: renderFeed,
    entry: renderEntry,
    message: renderMessage,
    error: renderError,
  });
  updateControls(content.session.project);
})();
