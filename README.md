# agent-mcp

Unified MCP server for external agent integration with EvalOps. One config line gives any MCP-capable coding agent — Claude Code, Codex, Cursor, Windsurf, Cline — the full EvalOps organizational stack: identity, governance, approvals, metering, and operating rules.

## Architecture

```
┌─────────────────────────────────────────────┐
│           agent-mcp                         │
│                                             │
│  MCP Server (StreamableHTTP at /mcp)        │
│    ├── evalops_register                     │
│    │     ├── Identity (session + JWT)       │
│    │     └── Registry (discovery + presence)│
│    ├── evalops_heartbeat                    │
│    │     ├── Identity (token rotation)      │
│    │     └── Registry (presence update)     │
│    ├── evalops_deregister                   │
│    │     ├── Identity (session revoke)      │
│    │     └── Registry (deregister)          │
│    ├── evalops_check_action                 │
│    │     ├── Governance (risk evaluation)   │
│    │     └── Approvals (if require_approval)│
│    ├── evalops_check_approval               │
│    │     └── Approvals (poll status)        │
│    └── evalops_report_usage                 │
│          └── Meter (token/cost tracking)    │
└─────────────────────────────────────────────┘
```

## Quick start

### Claude Code

Add to `.claude/settings.json`:

```json
{
  "mcp_servers": [
    {
      "name": "evalops",
      "type": "url",
      "url": "https://agent-mcp.evalops.example/mcp"
    }
  ]
}
```

### OpenAI Codex

Add to `.codex/config.toml`:

```toml
[mcp_servers.evalops]
url = "https://agent-mcp.evalops.example/mcp"
bearer_token_env_var = "EVALOPS_TOKEN"
```

## Tools

| Tool | Description |
|------|-------------|
| `evalops_register` | Register as an EvalOps agent — creates identity session and registry presence |
| `evalops_heartbeat` | Heartbeat — rotates identity token and updates registry presence |
| `evalops_deregister` | Deregister — revokes identity session and removes registry presence |
| `evalops_check_action` | Evaluate an action against governance policies |
| `evalops_check_approval` | Check or wait for an approval request to be resolved |
| `evalops_report_usage` | Report token usage and cost to the metering service |

## Configuration

All configuration is via environment variables:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `IDENTITY_BASE_URL` | Yes | — | Identity service base URL |
| `REGISTRY_BASE_URL` | No | — | Registry service base URL (enables discovery) |
| `GOVERNANCE_BASE_URL` | No | — | Governance service base URL (enables policy evaluation) |
| `APPROVALS_BASE_URL` | No | — | Approvals service base URL (enables approval workflows) |
| `METER_BASE_URL` | No | — | Meter service base URL (enables cost tracking) |
| `MEMORY_BASE_URL` | No | — | Memory service base URL (enables operating rules) |
| `SESSION_STORE` | No | `memory` | Session backend: `memory` or `redis` |
| `SESSION_REDIS_URL` | With `SESSION_STORE=redis` | — | Redis URL for shared session persistence |
| `SESSION_REAP_INTERVAL` | No | `30s` | Expiry sweep interval for the in-memory session backend |
| `BREAKER_FAILURE_THRESHOLD` | No | `5` | Consecutive downstream failures before a breaker opens |
| `BREAKER_RESET_TIMEOUT` | No | `30s` | Time before an open breaker allows a half-open probe |
| `ADDR` | No | `:8080` | Listen address |

Each downstream service is optional — if its URL is not configured, the corresponding tools degrade gracefully (governance returns allow, metering is a no-op, etc.).

All services support mTLS via `*_CA_FILE`, `*_CERT_FILE`, `*_KEY_FILE`, `*_SERVER_NAME` env vars.

The Helm chart exposes `session.store`, `session.redis.url`, and `session.redis.existingSecretName` so Redis-backed sessions can be enabled without rebuilding the service. Prefer a secret-backed Redis URL for production credentials.

## Running locally

```bash
export IDENTITY_BASE_URL=http://localhost:8081
go run ./cmd/agent-mcp
```

## Building

```bash
make build          # compile
make test           # run tests
make run            # run locally
docker build -t agent-mcp .
```
