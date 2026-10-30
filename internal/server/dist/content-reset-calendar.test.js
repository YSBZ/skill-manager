"use strict";

const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

function calendar() {
  const context = { window: {}, document: { getElementById() { return null; } }, Date, Intl, URL };
  vm.createContext(context);
  for (const file of ["content-ui.js", "content-reset-calendar.js"]) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, file), "utf8"), context);
  }
  return context.window.SkillManagerResetCalendar;
}

test("calendar dates follow Beijing across the UTC day boundary", () => {
  assert.equal(calendar().dateKey("2026-10-28T17:30:00Z"), "2026-10-29");
  assert.equal(calendar().dateKey("2026-10-28T23:30:00-07:00"), "2026-10-29");
});

test("verified occurrence takes precedence over confirmation and original estimate", () => {
  const day = calendar().eventDay({ status: "confirmed", occurredOn: "2026-10-27", confirmedAt: "2026-10-28T10:00:00Z", schedule: { from: "2026-10-29T00:00:00+08:00" } });
  assert.equal(day.date, "2026-10-27");
  assert.equal(day.basis, "已核实发生日期");
});

test("unknown occurrence is labelled as a confirmation post, never execution time", () => {
  const day = calendar().eventDay({ status: "confirmed", occurredOn: null, confirmedAt: "2026-10-28T17:00:00Z", schedule: { from: "2026-10-30T00:00:00+08:00" } });
  assert.equal(day.date, "2026-10-29");
  assert.equal(day.basis, "确认帖日期");
});

test("announcements use explicit source windows but ignore inferred estimates", () => {
  const api = calendar();
  assert.equal(api.eventDay({ status: "announced", schedule: { from: "2026-11-01T00:00:00+08:00" }, createdAt: "2026-10-29T08:00:00+08:00" }).basis, "公告时间窗口");
  const day = api.eventDay({ status: "announced", createdAt: "2026-10-29T08:00:00+08:00", estimate: { from: "2026-11-01T00:00:00+08:00" } });
  assert.equal(day.date, "2026-10-29");
  assert.equal(day.basis, "公告发布日");
});

test("missing and invalid dates stay undated rather than fabricate a reset", () => {
  assert.equal(calendar().eventDay({ status: "confirmed", occurredOn: "2026-02-30", confirmedAt: "invalid" }).date, "");
  assert.equal(calendar().dateKey("2026-02-30T00:00:00Z"), "");
});

test("past unconfirmed announcements never remain labelled as awaiting a reset", () => {
  const api = calendar();
  const now = new Date("2026-10-29T08:00:00Z");
  assert.equal(api.eventStatus({ status: "announced", schedule: { through: "2026-10-26T23:59:59+08:00" }, presentation: { status: "likely_completed" } }, now), "窗口已过 · 未确认");
  assert.equal(api.eventStatus({ status: "announced", presentation: { status: "likely_completed" } }, now), "已宣布 · 待确认");
  assert.equal(api.eventStatus({ status: "announced", schedule: { from: "2026-10-30T08:00:00+08:00" } }, now), "已宣布 · 待生效");
  assert.equal(api.eventStatus({ status: "confirmed" }, now), "已确认");
});

test("February and year boundaries use a Monday-first complete six-week grid", () => {
  const api = calendar();
  const leap = api.monthCells("2024-02-29");
  assert.equal(leap.length, 42);
  assert.ok(leap.includes("2024-02-29"));
  assert.equal(leap[0], "2024-01-29");
  assert.equal(api.monthCells("2026-01-01")[0], "2025-12-29");
  assert.equal(api.monthCells("2025-02-01").filter((day) => day.startsWith("2025-02")).length, 28);
});
