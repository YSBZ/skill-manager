"use strict";

const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

function loadTheme(options = {}) {
  const saved = new Map(options.saved ? [["sm.theme", options.saved]] : []);
  const attrs = {};
  const events = {};
  const label = { textContent: "" };
  const classes = new Set();
  const frames = [];
  const root = { dataset: {}, classList: { add(name) { classes.add(name); }, remove(name) { classes.delete(name); } } };
  const button = {
    setAttribute(key, value) { attrs[key] = value; },
    addEventListener(name, handler) { events[name] = handler; },
    querySelector() { return label; },
  };
  const storage = {
    getItem(key) { if (options.blocked) throw new Error("Storage disabled"); return saved.get(key); },
    setItem(key, value) { if (options.blocked) throw new Error("Storage disabled"); saved.set(key, value); },
  };
  const document = {
    documentElement: root,
    readyState: "loading",
    getElementById() { return button; },
    addEventListener(name, handler) { events[name] = handler; },
  };
  const context = { document, window: { localStorage: storage, requestAnimationFrame(handler) { frames.push(handler); }, addEventListener(name, handler) { events[name] = handler; } } };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, "theme.js"), "utf8"), context);
  return { root, attrs, saved, label, events, classes, frames };
}

test("new profiles show white theme before the DOM is ready", () => {
  const ui = loadTheme();
  assert.equal(ui.root.dataset.theme, "light");
  assert.equal(ui.label.textContent, "");
  ui.events.DOMContentLoaded();
  assert.equal(ui.attrs["aria-label"], "切换为暗色主题");
  assert.equal(ui.label.textContent, "亮色");
});

test("saved dark choice is applied before paint and can switch back", () => {
  const ui = loadTheme({ saved: "dark" });
  assert.equal(ui.root.dataset.theme, "dark");
  ui.events.DOMContentLoaded();
  ui.events.click();
  assert.equal(ui.root.dataset.theme, "light");
  assert.equal(ui.saved.get("sm.theme"), "light");
  assert.equal(ui.attrs["aria-label"], "切换为暗色主题");
  ui.events.click();
  assert.equal(ui.root.dataset.theme, "dark");
  assert.equal(ui.saved.get("sm.theme"), "dark");
});

test("theme stays usable when local storage is unavailable", () => {
  const ui = loadTheme({ blocked: true });
  ui.events.DOMContentLoaded();
  assert.doesNotThrow(() => ui.events.click());
  assert.equal(ui.root.dataset.theme, "dark");
});

test("unknown persisted values safely use light theme", () => {
  assert.equal(loadTheme({ saved: "system-or-invalid" }).root.dataset.theme, "light");
});

test("switching suppresses color transitions through the next paint", () => {
  const ui = loadTheme();
  ui.events.DOMContentLoaded();
  ui.events.click();
  assert.ok(ui.classes.has("theme-changing"));
  ui.frames.shift()();
  assert.ok(ui.classes.has("theme-changing"));
  ui.frames.shift()();
  assert.equal(ui.classes.has("theme-changing"), false);
});
