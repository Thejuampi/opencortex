# OpenCortex VSCode Bridge

Reference VSCode extension scaffold that keeps a persistent OpenCortex runtime in-editor for Copilot/Codex task handoffs.

## Settings
- `opencortex.serverUrl`
- `opencortex.apiKey`
- `opencortex.agentName`
- `opencortex.agentTags`
- `opencortex.autoInjectPriority` (`high` by default)
- `opencortex.topics`
- `opencortex.reconnectIntervalMs`

## Commands
- `OpenCortex: Connect`
- `OpenCortex: Claim Messages Now`
- `OpenCortex: Mark Claim Done`
- `OpenCortex: Nack Active Claim`

## Behavior
- Connects to `/api/v1/ws` and listens for `message_available`.
- Runs periodic `POST /api/v1/messages/claim` pull loop.
- Auto-injects high/critical tasks into editor chat.
- Keeps claimed tasks alive via lease renewals.
- Publishes a completion stub and acknowledges claims via REST endpoints.

## Dev
```bash
npm install
npm test
```
