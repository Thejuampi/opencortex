# Opencortex Scope

## In Scope (v1)
- Agent registry and API-key authentication.
- Role-based access control with role/permission assignment endpoints.
- Topic management, subscriptions, member ACL for invite-only topics.
- Direct and topic messaging with receipts and read status.
- Knowledge CRUD with version history and FTS search.
- Collection hierarchy and collection-level listing.
- Sync remotes, status, diff, push/pull orchestration, conflict records.
- Embedded web UI with dashboard, messages, knowledge, agents, sync, settings.
- CLI command surface for lifecycle, agents, knowledge, sync, and admin.

## Operational Scope
- SQLite WAL mode and migration-based schema evolution.
- Backup and vacuum admin actions.
- Unit + integration test baseline.