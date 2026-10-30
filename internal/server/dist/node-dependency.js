"use strict";

(() => {
  const DOWNLOAD_URL = "https://nodejs.org/en/download";
  const FALLBACK_REQUIRED_VERSION = "22.20.0";

  function createController(document, openExternal) {
    let status = null;
    let modal = null;
    let returnFocus = null;
    let visible = false;

    function nodeState() {
      const dependency = status && status.nodeDependency;
      if (dependency && dependency.state === "ready" && status.npxAvailable) return { ready: true };
      if (!dependency && status && status.npxAvailable) return { ready: true };
      return {
        ready: false,
        state: dependency && dependency.state || "missing",
        version: dependency && dependency.version || "",
        requiredVersion: dependency && dependency.requiredVersion || FALLBACK_REQUIRED_VERSION,
      };
    }

    function describeNode(state) {
      if (state.state === "too_old") {
        return "当前检测到 Node.js " + (state.version || "版本过旧") + "，此操作需要 Node.js " + state.requiredVersion + " 或更高版本。";
      }
      if (state.state === "invalid") {
        return "无法确认当前 Node.js 版本。请安装 Node.js " + state.requiredVersion + " 或更高版本。";
      }
      return "未检测到可用的 Node.js。此操作需要 Node.js " + state.requiredVersion + " 或更高版本。";
    }

    function ensureModal() {
      if (modal) return modal;
      const overlay = document.createElement("div");
      overlay.className = "modal hidden node-dependency-overlay";
      overlay.setAttribute("data-node-dependency-modal", "true");

      const dialog = document.createElement("section");
      dialog.className = "modal-card node-dependency-card";
      dialog.setAttribute("role", "dialog");
      dialog.setAttribute("aria-modal", "true");
      dialog.setAttribute("aria-labelledby", "node-dependency-title");
      dialog.setAttribute("aria-describedby", "node-dependency-description");

      const header = document.createElement("div");
      header.className = "modal-head";
      const title = document.createElement("h3");
      title.id = "node-dependency-title";
      title.textContent = "需要 Node.js 才能安装或更新";
      const closeIcon = document.createElement("button");
      closeIcon.type = "button";
      closeIcon.className = "modal-x";
      closeIcon.setAttribute("aria-label", "关闭");
      closeIcon.title = "关闭";
      closeIcon.textContent = "×";
      header.append(title, closeIcon);

      const body = document.createElement("div");
      body.className = "node-dependency-body";
      const description = document.createElement("p");
      description.id = "node-dependency-description";
      description.className = "node-dependency-state";
      const restart = document.createElement("p");
      restart.className = "node-dependency-restart";
      restart.textContent = "从官方页面安装或升级 Node.js 后，请完全退出并重新打开 SkillManager，再重试此操作。";
      body.append(description, restart);

      const actions = document.createElement("div");
      actions.className = "node-dependency-actions";
      const download = document.createElement("button");
      download.type = "button";
      download.className = "small";
      download.textContent = "从 Node.js 官方网站下载";
      const close = document.createElement("button");
      close.type = "button";
      close.className = "ghost small";
      close.textContent = "关闭";
      actions.append(download, close);

      dialog.append(header, body, actions);
      overlay.append(dialog);
      document.body.append(overlay);

      const hide = () => closeModal();
      closeIcon.addEventListener("click", hide);
      close.addEventListener("click", hide);
      download.addEventListener("click", () => {
        if (typeof openExternal === "function") openExternal(DOWNLOAD_URL);
      });
      overlay.addEventListener("click", (event) => {
        if (event.target === overlay) hide();
      });
      modal = { overlay, dialog, description, closeIcon, close, download };
      return modal;
    }

    function closeModal() {
      if (!modal || !visible) return;
      visible = false;
      modal.overlay.classList.add("hidden");
      modal.overlay.hidden = true;
      const target = returnFocus;
      returnFocus = null;
      if (target && typeof target.focus === "function") target.focus();
    }

    function show(trigger) {
      const current = ensureModal();
      const state = nodeState();
      current.description.textContent = describeNode(state);
      if (!visible) returnFocus = trigger || document.activeElement || null;
      visible = true;
      current.overlay.hidden = false;
      current.overlay.classList.remove("hidden");
      current.closeIcon.focus();
    }

    document.addEventListener("keydown", (event) => {
      if (!visible) return;
      if (event.key === "Escape") {
        event.preventDefault();
        closeModal();
        return;
      }
      if (event.key !== "Tab" || !modal) return;
      const buttons = [modal.closeIcon, modal.download, modal.close];
      const first = buttons[0], last = buttons[buttons.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    });

    return Object.freeze({
      setStatus(nextStatus) { status = nextStatus && typeof nextStatus === "object" ? nextStatus : null; },
      requireNode(trigger) {
        const state = nodeState();
        if (state.ready) return true;
        show(trigger);
        return false;
      },
      open(trigger) { show(trigger); },
      close: closeModal,
    });
  }

  window.SkillManagerNodeDependency = Object.freeze({ createController, DOWNLOAD_URL });
})();
