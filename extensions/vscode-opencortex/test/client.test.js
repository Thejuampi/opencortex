const test = require("node:test");
const assert = require("node:assert/strict");

const { OpenCortexClient } = require("../src/opencortexClient");

function okResponse(data) {
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    text: async () => JSON.stringify({ ok: true, data }),
  };
}

test("claim/ack/renew uses REST contract", async () => {
  const calls = [];
  global.fetch = async (url, init) => {
    calls.push({ url, method: init.method, body: init.body });
    if (url.endsWith("/messages/claim")) {
      return okResponse({ claims: [{ message: { id: "m1" }, claim_token: "t1" }] });
    }
    if (url.includes("/messages/m1/renew")) {
      return okResponse({ claim_expires_at: "2026-01-01T00:00:00Z" });
    }
    return okResponse({});
  };

  const client = new OpenCortexClient({
    serverUrl: "http://localhost:8080",
    apiKey: "amk_live_test",
    reconnectIntervalMs: 100,
  });

  const claims = await client.claim(2, 300);
  assert.equal(claims.length, 1);

  await client.ack("m1", "t1", true);
  await client.renew("m1", "t1", 120);

  assert.equal(calls[0].url, "http://localhost:8080/api/v1/messages/claim");
  assert.equal(calls[1].url, "http://localhost:8080/api/v1/messages/m1/ack");
  assert.equal(calls[2].url, "http://localhost:8080/api/v1/messages/m1/renew");

  client.dispose();
});

test("scheduleReconnect and claim loop execute callbacks", async () => {
  const client = new OpenCortexClient({
    serverUrl: "http://localhost:8080",
    apiKey: "amk_live_test",
    reconnectIntervalMs: 10,
  });

  let reconnectCalled = false;
  client.connectWS = () => {
    reconnectCalled = true;
  };

  client.scheduleReconnect({});
  await new Promise((resolve) => setTimeout(resolve, 30));
  assert.equal(reconnectCalled, true);

  let ticks = 0;
  client.startClaimLoop(async () => {
    ticks += 1;
  }, 10);
  await new Promise((resolve) => setTimeout(resolve, 35));
  assert.ok(ticks >= 2, `expected multiple claim ticks, got ${ticks}`);

  client.dispose();
});
