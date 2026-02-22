const PRIORITY_ORDER = {
  low: 0,
  normal: 1,
  high: 2,
  critical: 3,
};

function normalizePriority(value) {
  const key = String(value || "normal").toLowerCase();
  return PRIORITY_ORDER[key] !== undefined ? key : "normal";
}

function shouldAutoInject(priority, threshold) {
  const p = PRIORITY_ORDER[normalizePriority(priority)];
  const t = PRIORITY_ORDER[normalizePriority(threshold)];
  return p >= t;
}

module.exports = {
  PRIORITY_ORDER,
  normalizePriority,
  shouldAutoInject,
};
