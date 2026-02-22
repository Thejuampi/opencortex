# Opencortex Limitations

## Current Limitations
- Sync inbound materialization is scaffolded and can require follow-up hardening for full data-merge semantics.
- Browser E2E coverage is not included in the current test baseline.
- Initial admin bootstrap can create multiple admins if `init` is run repeatedly without policy checks.
- Conflict resolution strategies are present, but manual conflict UX can be expanded.
- Topic/member filtering and advanced message filtering can be deepened for production workloads.

## Security/Operations Notes
- API key is shown once at creation time and must be stored safely by operators.
- Workflow push via GitHub OAuth app requires `workflow` scope when pushing CI files.
- Production deployment should use TLS termination and locked-down CORS origins.

## Next Hardening Targets
- Full bidirectional sync payload apply + deterministic merge behavior.
- Expanded observability (metrics/tracing) and stronger audit surfacing.
- Additional integration scenarios for sync and websocket resilience.