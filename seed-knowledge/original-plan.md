# Opencortex Original Plan

## Vision
Build a self-hostable, local-first system for multi-agent communication and shared knowledge.

## Core Components
- REST API + WebSocket gateway for agents and UI.
- In-process broker for topic pub/sub and direct messages.
- SQLite-backed storage for agents, topics, messages, knowledge, sync, and RBAC.
- Embedded web UI (Svelte) shipped inside the Go binary.
- Sync engine for remote push/pull/diff/conflict workflows.

## Delivery Shape
Single binary executable with three operating modes:
- local
- server
- hybrid

## Architectural Priorities
- No external infra dependency for MVP operation.
- Durable writes before message dispatch where possible.
- Predictable interfaces for CLI, UI, and Go SDK.