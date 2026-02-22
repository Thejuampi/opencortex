# Opencortex

[![CI](https://github.com/Thejuampi/opencortex/actions/workflows/ci.yml/badge.svg)](https://github.com/Thejuampi/opencortex/actions/workflows/ci.yml)
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
- In-process message broker with SQLite durability
- Versioned knowledge base with FTS5 search
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

opencortex agents list --api-key <admin-key>
opencortex agents create --name researcher --type ai --api-key <admin-key>
opencortex agents get <id> --api-key <admin-key>
opencortex agents rotate-key <id> --api-key <admin-key>

opencortex knowledge search "quantum computing" --api-key <key>
opencortex knowledge add --title "My Note" --file ./note.md --tags research,2024 --api-key <key>
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

## Development
```bash
go test ./...
go run ./cmd/opencortex server --config ./config.yaml
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
