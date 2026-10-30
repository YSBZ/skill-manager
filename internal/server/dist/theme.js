"use strict";

// Loaded synchronously in <head> so a saved preference applies before CSS paints.
(() => {
  const key = "sm.theme";
  const root = document.documentElement;
  let theme = "light";
  try {
    const stored = window.localStorage.getItem(key);
    if (stored === "light" || stored === "dark") theme = stored;
  } catch { /* A theme toggle still works when storage is disabled. */ }
  root.dataset.theme = theme;

  function bind() {
    const button = document.getElementById("theme-toggle");
    if (!button) return;
    let switchVersion = 0;
    function update() {
      const light = theme === "light";
      button.setAttribute("aria-label", light ? "切换为暗色主题" : "切换为亮色主题");
      button.setAttribute("title", light ? "当前亮色 · 切换为暗色" : "当前暗色 · 切换为亮色");
      const label = button.querySelector("#theme-toggle-label");
      if (label) label.textContent = light ? "亮色" : "暗色";
    }
    button.addEventListener("click", () => {
      const version = ++switchVersion;
      root.classList.add("theme-changing");
      theme = theme === "light" ? "dark" : "light";
      root.dataset.theme = theme;
      try { window.localStorage.setItem(key, theme); } catch { /* Session choice remains usable. */ }
      update();
      window.requestAnimationFrame(() => window.requestAnimationFrame(() => {
        if (version === switchVersion) root.classList.remove("theme-changing");
      }));
    });
    update();
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", bind, { once: true });
  else bind();
})();
