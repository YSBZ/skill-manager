"use strict";

const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

function helpers() {
  const context = { window: {}, document: { getElementById() { return null; } } };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, "content-models.js"), "utf8"), context);
  return context.window.SkillManagerModels;
}
const rows = [
  { id: "a", rank: 1, name: "Alpha Pro", provider: "One", score: 75, coveragePercent: 80, inputPrice: "待核验" },
  { id: "b", rank: 2, name: "Beta", provider: "Two", score: 74, coveragePercent: null, inputPrice: "¥10.00" },
  { id: "c", rank: 3, name: "Alpha Lite", provider: "One", score: 73, coveragePercent: 100, inputPrice: "¥2.00" },
];

test("unknown prices are never treated as zero", () => {
  const { priceNumber } = helpers();
  assert.equal(priceNumber("待核验"), null);
  assert.equal(priceNumber("—"), null);
  assert.equal(priceNumber(""), null);
  assert.equal(priceNumber("¥0.00"), 0);
  assert.equal(priceNumber("￥2.50"), 2.5);
  assert.equal(priceNumber("¥1.0 / ¥2.0"), null);
});

test("price sorting is numeric, keeps unknown last in either direction", () => {
  const { selectRows } = helpers();
  assert.equal(selectRows(rows, { sort: "inputPrice", direction: "asc" }).map((x) => x.id).join(), "c,b,a");
  assert.equal(selectRows(rows, { sort: "inputPrice", direction: "desc" }).map((x) => x.id).join(), "b,c,a");
});

test("filtering keeps original ranks and never mutates source rows", () => {
  const result = helpers().selectRows(rows, { q: "ALPHA", provider: "One", sort: "score", direction: "asc" });
  assert.equal(result.map((x) => x.rank).join(), "3,1");
  assert.equal(rows.map((x) => x.id).join(), "a,b,c");
});

test("null coverage stays after real zero coverage", () => {
  const result = helpers().selectRows([...rows, { id: "z", rank: 4, name: "Zero", coveragePercent: 0 }], { sort: "coveragePercent", direction: "asc" });
  assert.equal(result[0].id, "z");
  assert.equal(result.at(-1).id, "b");
});

test("search matches provider and empty filters return an empty list", () => {
  assert.equal(helpers().selectRows(rows, { q: "two" })[0].id, "b");
  assert.equal(helpers().selectRows(rows, { q: "nonexistent" }).length, 0);
});
