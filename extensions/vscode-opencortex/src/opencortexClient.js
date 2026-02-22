const WebSocket = require("ws");

class OpenCortexClient {
  constructor(config, logger = console) {
    this.serverUrl = String(config.serverUrl || "http://localhost:8080").replace(/\/$/, "");
    this.apiKey = String(config.apiKey || "");
    this.reconnectIntervalMs = Number(config.reconnectIntervalMs || 2000);
    this.ws = null;
    this.logger = logger;
    this.stopped = false;
    this.claimTimer = null;
    this.reconnectTimer = null;
  }

  headers() {
    return {
      "Content-Type": "application/json",
      Authorization: `Bearer ${this.apiKey}`,
    };
  }

  async rest(method, path, payload) {
    const response = await fetch(`${this.serverUrl}${path}`, {
      method,
      headers: this.headers(),
      body: payload === undefined ? undefined : JSON.stringify(payload),
    });

    const raw = await response.text();
    let body = {};
    try {
      body = raw ? JSON.parse(raw) : {};
    } catch {
      body = {};
    }

    if (!response.ok || body.ok === false) {
      const message = body?.error?.message || response.statusText || "request failed";
      throw new Error(`${response.status}: ${message}`);
    }
    return body.data || {};
  }

  async claim(limit = 5, leaseSeconds = 300) {
    const data = await this.rest("POST", "/api/v1/messages/claim", {
      limit,
      lease_seconds: leaseSeconds,
    });
    return data.claims || [];
  }

  async ack(messageId, claimToken, markRead = true) {
    return this.rest("POST", `/api/v1/messages/${messageId}/ack`, {
      claim_token: claimToken,
      mark_read: markRead,
    });
  }

  async nack(messageId, claimToken, reason = "") {
    return this.rest("POST", `/api/v1/messages/${messageId}/nack`, {
      claim_token: claimToken,
      reason,
    });
  }

  async renew(messageId, claimToken, leaseSeconds = 300) {
    return this.rest("POST", `/api/v1/messages/${messageId}/renew`, {
      claim_token: claimToken,
      lease_seconds: leaseSeconds,
    });
  }

  async publish(payload) {
    return this.rest("POST", "/api/v1/messages", payload);
  }

  connectWS({ onState, onHint, onMessage }) {
    this.stopped = false;

    const scheme = this.serverUrl.startsWith("https://") ? "wss" : "ws";
    const host = this.serverUrl.replace(/^https?:\/\//, "");
    const wsUrl = `${scheme}://${host}/api/v1/ws?api_key=${encodeURIComponent(this.apiKey)}`;

    const ws = new WebSocket(wsUrl);
    this.ws = ws;

    ws.on("open", () => {
      onState?.("connected");
    });

    ws.on("message", (buffer) => {
      let frame;
      try {
        frame = JSON.parse(buffer.toString("utf-8"));
      } catch {
        return;
      }
      if (frame.type === "message_available") {
        onHint?.(frame.data || {});
      }
      if (frame.type === "message") {
        onMessage?.(frame.data || {});
      }
    });

    ws.on("close", () => {
      onState?.("disconnected");
      if (!this.stopped) {
        this.scheduleReconnect({ onState, onHint, onMessage });
      }
    });

    ws.on("error", (err) => {
      this.logger.warn("OpenCortex WS error", err?.message || err);
    });
  }

  subscribeTopic(topicID) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      return;
    }
    this.ws.send(JSON.stringify({ type: "subscribe", topic_id: topicID }));
  }

  startClaimLoop(fn, intervalMs) {
    this.stopClaimLoop();
    this.claimTimer = setInterval(() => {
      fn().catch((err) => {
        this.logger.warn("OpenCortex claim loop error", err?.message || err);
      });
    }, intervalMs || this.reconnectIntervalMs);
  }

  stopClaimLoop() {
    if (this.claimTimer) {
      clearInterval(this.claimTimer);
      this.claimTimer = null;
    }
  }

  scheduleReconnect(callbacks) {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
    }
    this.reconnectTimer = setTimeout(() => {
      this.connectWS(callbacks);
    }, this.reconnectIntervalMs);
  }

  dispose() {
    this.stopped = true;
    this.stopClaimLoop();
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
  }
}

module.exports = {
  OpenCortexClient,
};
