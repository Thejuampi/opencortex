const test = require("node:test");
const assert = require("node:assert/strict");

const { normalizePriority, shouldAutoInject } = require("../src/policy");

test("normalizePriority uses safe defaults", () => {
  assert.equal(normalizePriority("HIGH"), "high");
  assert.equal(normalizePriority("unknown"), "normal");
  assert.equal(normalizePriority(""), "normal");
});

test("shouldAutoInject follows threshold ordering", () => {
  assert.equal(shouldAutoInject("critical", "high"), true);
  assert.equal(shouldAutoInject("high", "high"), true);
  assert.equal(shouldAutoInject("normal", "high"), false);
});
