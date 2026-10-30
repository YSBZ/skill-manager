"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");
const vm = require("node:vm");

const source = fs.readFileSync(path.join(__dirname, "node-dependency.js"), "utf8");
const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");

function makeElement(tagName = "div", onFocus = () => {}) {
  let text = "";
  const classes = new Set();
  const element = {
    tagName: tagName.toUpperCase(),
    hidden: false,
    attributes: {},
    listeners: {},
    children: [],
    className: "",
    classList: {
      add(name) { classes.add(name); },
      remove(name) { classes.delete(name); },
      contains(name) { return classes.has(name); },
    },
    setAttribute(name, value) { this.attributes[name] = String(value); },
    getAttribute(name) { return this.attributes[name] || null; },
    addEventListener(name, handler) { this.listeners[name] = handler; },
    append(...children) { this.children.push(...children); children.forEach((child) => { child.parent = this; }); },
    focus() { onFocus(this); this.focused = true; },
    querySelectorAll(selector) {
      const all = this.children.flatMap((child) => [child, ...child.querySelectorAll(selector)]);
      return selector === "button" ? all.filter((child) => child.tagName === "BUTTON") : all;
    },
    click() {
      if (this.listeners.click) this.listeners.click({ target: this, preventDefault() {} });
    },
    get textContent() { return text + this.children.map((child) => child.textContent || "").join(""); },
    set textContent(value) { text = String(value ?? ""); this.children = []; },
  };
  return element;
}

function setup() {
  const listeners = {};
  const document = {
    activeElement: null,
    body: null,
    createElement: (tag) => makeElement(tag, (element) => { document.activeElement = element; }),
    addEventListener(name, handler) { listeners[name] = handler; },
  };
  document.body = makeElement("body", (element) => { document.activeElement = element; });
  const opened = [];
  const window = {};
  vm.runInNewContext(source, { window });
  const controller = window.SkillManagerNodeDependency.createController(document, async (url) => opened.push(url));
  return { controller, document, listeners, opened };
}

function trigger(document) {
  return makeElement("button", (element) => { document.activeElement = element; });
}

test("opening an install action with missing Node shows guidance without nagging at startup", async () => {
  const { controller, document, listeners, opened } = setup();
  controller.setStatus({ npxAvailable: false, nodeDependency: { state: "missing", requiredVersion: "22.20.0" } });

  assert.equal(document.body.children.length, 0, "setting startup status should not open or append a visible prompt");
  const action = trigger(document);
  assert.equal(controller.requireNode(action), false, "an install action must stop before calling its API");
  const overlay = document.body.children[0];
  assert.ok(overlay, "the action should open a prompt");
  assert.match(overlay.textContent, /未检测到可用的 Node\.js/);
  assert.match(overlay.textContent, /22\.20\.0/);
  assert.match(overlay.textContent, /重新打开/);
  const dialog = overlay.children[0];
  assert.equal(dialog.getAttribute("role"), "dialog");
  assert.equal(dialog.getAttribute("aria-modal"), "true");
  assert.equal(document.activeElement.tagName, "BUTTON", "focus should enter the dialog");

  const buttons = overlay.querySelectorAll("button");
  const download = buttons.find((button) => button.textContent.includes("下载"));
  download.click();
  await Promise.resolve();
  assert.deepEqual(opened, ["https://nodejs.org/en/download"], "the download action must use the fixed official URL");

  const close = buttons.find((button) => button.textContent.includes("关闭"));
  let tabPrevented = false;
  close.focus();
  document.activeElement = close;
  listeners.keydown({ key: "Tab", shiftKey: false, preventDefault() { tabPrevented = true; } });
  assert.equal(tabPrevented, true, "Tab from the last control should stay inside the modal");
  assert.equal(document.activeElement, buttons[0], "Tab should wrap to the first control");
  let shiftTabPrevented = false;
  listeners.keydown({ key: "Tab", shiftKey: true, preventDefault() { shiftTabPrevented = true; } });
  assert.equal(shiftTabPrevented, true, "Shift+Tab from the first control should stay inside the modal");
  assert.equal(document.activeElement, close, "Shift+Tab should wrap to the last control");
  close.click();
  assert.equal(overlay.classList.contains("hidden"), true, "the close button should hide the prompt");
  assert.equal(document.activeElement, action, "closing should return focus to the action that opened the prompt");
});

test("an old Node version is described and Escape closes the prompt", () => {
  const { controller, document, listeners } = setup();
  controller.setStatus({ npxAvailable: false, nodeDependency: { state: "too_old", version: "20.11.1", requiredVersion: "22.20.0" } });
  const action = trigger(document);
  assert.equal(controller.requireNode(action), false);
  const overlay = document.body.children[0];
  assert.match(overlay.textContent, /20\.11\.1/);
  assert.match(overlay.textContent, /22\.20\.0/);

  let prevented = false;
  listeners.keydown({ key: "Escape", preventDefault() { prevented = true; } });
  assert.equal(prevented, true);
  assert.equal(overlay.classList.contains("hidden"), true);
  assert.equal(document.activeElement, action);
});

test("ready Node permits the action without showing a modal", () => {
  const { controller, document } = setup();
  controller.setStatus({ npxAvailable: true, nodeDependency: { state: "ready", version: "22.20.0", requiredVersion: "22.20.0" } });

  assert.equal(controller.requireNode(makeElement("button")), true);
  assert.equal(document.body.children.length, 0);
});

test("the manager loads the dependency guidance helper before app.js", () => {
  assert.match(html, /<script src="\/node-dependency\.js"><\/script>\s*<script src="\/app\.js"><\/script>/);
});
