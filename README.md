# Opencortex

[![Status](https://img.shields.io/badge/status-bootstrap-0ea5e9)](https://github.com/Thejuampi/opencortex)
[![Go Version](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Node](https://img.shields.io/badge/Node-20%2B-339933?logo=nodedotjs&logoColor=white)](https://nodejs.org/)
[![Svelte](https://img.shields.io/badge/Svelte-5-FF3E00?logo=svelte&logoColor=white)](https://svelte.dev/)
[![Vite](https://img.shields.io/badge/Vite-5-646CFF?logo=vite&logoColor=white)](https://vitejs.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)

Self-hostable, local-first infrastructure for multi-agent communication, shared knowledge, and selective node-to-node sync.

## Why Opencortex
- Message broker for topic pub/sub + direct messaging
- Versioned knowledge base with SQLite + FTS5
- Sync engine for push/pull/diff/conflict workflows
- Embedded modern web UI + CLI + Go SDK
- Single binary deployment for local, server, and hybrid modes

## Features
- REST + WebSocket API (`/api/v1`)
- MCP server support over STDIO (`opencortex mcp-server`)
- MCP Streamable HTTP endpoint (`/mcp` by default)
- In-process message broker with SQLite durability
- Lease-based claim/ack/nack/renew message delivery
- Broadcast and group messaging (fanout + queue modes)
- Versioned knowledge base with FTS5 search
- Auto-generated knowledge abstracts (optional override via API/CLI)
- Topic subscriptions and invite-only topic members
- RBAC (roles, permissions, assignment APIs)
- Sync remotes, push/pull/diff/conflicts, sync logs
- Embedded Svelte UI served by the same binary
- CLI + Go SDK

## Quickstart
```bash
cp config.example.yaml config.yaml
go run ./cmd/opencortex init --config ./config.yaml
go run ./cmd/opencortex server --config ./config.yaml
```

Open `http://localhost:8080`.

## CLI
```bash
opencortex init
opencortex server --config ./config.yaml
opencortex mcp-server --config ./config.yaml --api-key <agent-key>

opencortex auth login --base-url http://localhost:8080 --api-key <key>
opencortex auth status
opencortex auth whoami
opencortex auth logout --base-url http://localhost:8080

opencortex agents list --api-key <admin-key>
opencortex agents create --name researcher --type ai --api-key <admin-key>
opencortex agents get <id> --api-key <admin-key>
opencortex agents rotate-key <id> --api-key <admin-key>

opencortex knowledge search "quantum computing" --api-key <key>
opencortex knowledge add --title "My Note" --file ./note.md --tags research,2024 --summary "Short abstract" --api-key <key>
opencortex knowledge get <id> --api-key <key>
opencortex knowledge export --collection <id> --format json --out ./export.json --api-key <key>
opencortex knowledge import --file ./export.json --api-key <key>

opencortex sync remote add origin https://hub.example.com --key amk_remote_xxx --api-key <sync-key>
opencortex sync remote list --api-key <sync-key>
opencortex sync push origin --key amk_remote_xxx --api-key <sync-key>
opencortex sync pull origin --key amk_remote_xxx --api-key <sync-key>
opencortex sync status --api-key <sync-key>
opencortex sync diff origin --api-key <sync-key>
opencortex sync conflicts --api-key <sync-key>
opencortex sync resolve <conflict-id> --strategy latest-wins --api-key <sync-key>

opencortex admin stats --api-key <admin-key>
opencortex admin backup --api-key <admin-key>
opencortex admin vacuum --api-key <admin-key>
opencortex admin rbac roles --api-key <admin-key>
opencortex admin rbac assign --agent <id> --role agent --api-key <admin-key>
opencortex admin rbac revoke --agent <id> --role agent --api-key <admin-key>
```

## MCP (Copilot / Codex)
OpenCortex exposes MCP tools as an agent-native interface over the same RBAC-backed REST logic.

### Transports
- STDIO: `opencortex mcp-server --config ./config.yaml --api-key <key>`
- Streamable HTTP: enabled in `server` mode at `mcp.http.path` (default `/mcp`)

### Example Codex MCP config
```toml
[mcp_servers.opencortex]
command = "opencortex"
args = ["mcp-server", "--config", "./config.yaml", "--api-key", "amk_live_xxx"]
```

### Tool naming
- One tool per REST operation with `<resource>_<action>` naming.
- Examples: `messages_publish`, `messages_claim`, `knowledge_list`, `sync_push`, `admin_stats`.

## Reliable Message Delivery (Claim/Ack)
New endpoints:
- `POST /api/v1/messages/claim`
- `POST /api/v1/messages/{id}/ack`
- `POST /api/v1/messages/{id}/nack`
- `POST /api/v1/messages/{id}/renew`

Lease semantics:
- `claim`: acquires a temporary lease (`claim_token`, `claim_expires_at`) on `pending` receipts.
- `ack`: validates token + unexpired lease, then marks delivered/read.
- `nack`: clears active lease and keeps receipt `pending` for redelivery.
- `renew`: extends active lease.

## Broadcast and Group Targeting
Targeting modes supported by `POST /api/v1/messages`:
- `to_agent_id`: direct one-to-one delivery
- `topic_id`: topic fanout to subscribers
- `to_group_id` with group mode `fanout`: one copy per member
- `to_group_id` with group mode `queue` (+ optional `queue_mode: true`): exactly one member claims and processes

System-wide broadcast endpoint:
- `POST /api/v1/messages/broadcast`

Groups API:
- `POST /api/v1/groups`
- `GET /api/v1/groups`
- `GET /api/v1/groups/{id}`
- `PATCH /api/v1/groups/{id}`
- `DELETE /api/v1/groups/{id}`
- `POST /api/v1/groups/{id}/members`
- `DELETE /api/v1/groups/{id}/members/{agent_id}`
- `GET /api/v1/groups/{id}/members`

Broadcast behavior:
- Uses reserved topic `system.broadcast`
- All agents are auto-subscribed on registration and startup reconciliation
- WebSocket clients auto-listen to broadcast on connect

## WebSocket Notes
- Existing frames remain: `subscribe`, `unsubscribe`, `send`, `message`.
- Direct mailbox delivery is now live on connect (no topic subscription required).
- `message_available` is emitted for topic and direct deliveries to trigger bridge claim loops.

## VSCode Bridge (Copilot/Codex)
A reference extension scaffold is included at `extensions/vscode-opencortex`.

Configuration keys:
- `opencortex.serverUrl`
- `opencortex.apiKey`
- `opencortex.agentName`
- `opencortex.agentTags`
- `opencortex.autoInjectPriority` (default `high`)
- `opencortex.topics`
- `opencortex.reconnectIntervalMs`

Behavior:
- Persistent local client identity via VSCode global state.
- WebSocket + reconnect + periodic claim loop.
- Auto-inject for `high` and `critical` tasks.
- Manual commands for ack/nack and immediate claim cycle.

Run extension tests:
```bash
cd extensions/vscode-opencortex
npm install
npm test
```

## API
All endpoints use:
- `Authorization: Bearer <api_key>`
- Envelope:
```json
{
  "ok": true,
  "data": {},
  "error": null,
  "pagination": { "page": 1, "per_page": 50, "total": 0 }
}
```

## Knowledge Document Format
`summary` is optional in API/CLI, but recommended.

If `summary` is omitted, Opencortex derives it deterministically using:
1. Front matter at document top: `summary:` or `abstract:`
2. First paragraph marker: `Abstract:` / `Summary:` / `~abstract:`
3. First meaningful paragraph fallback

Example:
```markdown
---
title: Broker Design
summary: How the in-process broker handles delivery and backpressure.
---

# Broker Design

Details...
```

## Development
```bash
go test ./...
go run ./cmd/opencortex server --config ./config.yaml
```

For MCP stdio:
```bash
go run ./cmd/opencortex mcp-server --config ./config.yaml --api-key <key>
```

### Web UI
```bash
cd web
npm install
npm run build
```

Copy `web/dist/*` into `internal/webui/dist/` before releasing.

## Security Notes
- API keys use `amk_live_`, `amk_test_`, `amk_remote_`
- Only SHA256 hashes are stored for agent keys
- RBAC enforced on route groups by resource/action
- Sync restricted to sync permission scope

## Troubleshooting
- `CLAIM_NOT_FOUND` on ack/nack/renew: lease token is invalid or expired; reclaim first.
- MCP HTTP 401: pass `Authorization: Bearer <api_key>` to the MCP client.
- No WS events: verify `api_key` in `/api/v1/ws` URL and check agent has `messages:read`.
- VSCode bridge idle: confirm `opencortex.apiKey` and `opencortex.serverUrl` settings.
