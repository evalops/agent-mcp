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

On the first unauthenticated tool call, MCP clients that support OAuth 2.1 will
receive a `401` challenge with protected resource metadata, discover the
Identity authorization server, open the browser sign-in flow, and retry
automatically with a bearer token bound to the MCP resource URL.

### OpenAI Codex

Add to `.codex/config.toml`:

```toml
[mcp_servers.evalops]
url = "https://agent-mcp.evalops.example/mcp"
bearer_token_env_var = "EVALOPS_TOKEN"
```

### Local sidecar federation

If you run `agent-mcp` in the same local environment as the agent, you can skip a separate EvalOps token entirely. Set `DEFAULT_WORKSPACE_ID` plus one of `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GITHUB_TOKEN`, or `GOOGLE_OAUTH_ACCESS_TOKEN`, and `evalops_register` will federate that existing provider credential into an EvalOps session automatically.

## Tools

| Tool | Description |
|------|-------------|
| `evalops_register` | Register as an EvalOps agent — creates identity session and registry presence |
| `evalops_heartbeat` | Heartbeat — rotates identity token and updates registry presence |
| `evalops_deregister` | Deregister — revokes identity session and removes registry presence |
| `evalops_check_action` | Evaluate an action against governance policies |
| `evalops_check_approval` | Check or wait for an approval request to be resolved |
| `evalops_report_usage` | Report token usage and cost to the metering service |
| `evalops_create_api_key` | Create a new headless API key for CI/CD and automation |
| `evalops_list_api_keys` | List your API keys with names, prefixes, scopes, and usage timestamps |

## Configuration

All configuration is via environment variables:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `IDENTITY_BASE_URL` | Yes | — | Identity service base URL |
| `DEFAULT_WORKSPACE_ID` | No | — | Default workspace or organization used when local provider-token federation is enabled |
| `IDENTITY_ISSUER_URL` | No | `IDENTITY_BASE_URL` | OAuth authorization server issuer advertised to MCP clients |
| `IDENTITY_INTROSPECT_URL` | No | `IDENTITY_BASE_URL + /v1/tokens/introspect` | Explicit token introspection endpoint override |
| `MCP_RESOURCE_URL` | No | inferred from request | Protected resource URL advertised in RFC 9728 metadata and bearer challenges |
| `REGISTRY_BASE_URL` | No | — | Registry service base URL (enables discovery) |
| `GOVERNANCE_BASE_URL` | No | — | Governance service base URL (enables policy evaluation) |
| `APPROVALS_BASE_URL` | No | — | Approvals service base URL (enables approval workflows) |
| `METER_BASE_URL` | No | — | Meter service base URL (enables cost tracking) |
| `MEMORY_BASE_URL` | No | — | Memory service base URL (enables operating rules) |
| `SESSION_STORE` | No | `memory` | Session backend: `memory` or `redis` |
| `SESSION_REDIS_URL` | With `SESSION_STORE=redis` | — | Redis URL for shared session persistence |
| `SESSION_REAP_INTERVAL` | No | `30s` | Expiry sweep interval for the in-memory session backend |
| `NATS_URL` | No | — | Shared NATS URL for durable lifecycle and governance event publishing |
| `NATS_STREAM` | No | `agent_mcp_events` | JetStream stream used for event envelopes |
| `NATS_SUBJECT_PREFIX` | No | `agent-mcp.events` | Subject prefix used when publishing event changes |
| `BREAKER_FAILURE_THRESHOLD` | No | `5` | Consecutive downstream failures before a breaker opens |
| `BREAKER_RESET_TIMEOUT` | No | `30s` | Time before an open breaker allows a half-open probe |
| `ADDR` | No | `:8080` | Listen address |

Each downstream service is optional — if its URL is not configured, the corresponding tools degrade gracefully (governance returns allow, metering is a no-op, etc.).

When `DEFAULT_WORKSPACE_ID` is set, `evalops_register` can fall back to identity federation using standard local provider credentials from `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GITHUB_TOKEN`, or `GOOGLE_OAUTH_ACCESS_TOKEN`. This is intended for local sidecar usage where `agent-mcp` runs alongside the agent process; hosted multi-tenant deployments should continue to use explicit EvalOps bearer tokens.

All services support mTLS via `*_CA_FILE`, `*_CERT_FILE`, `*_KEY_FILE`, `*_SERVER_NAME` env vars.

The Helm chart exposes `session.store`, `session.redis.url`, `session.redis.existingSecretName`, `nats.url`, and `nats.existingSecretName` so shared Redis sessions and durable NATS event publishing can be enabled without rebuilding the service. Prefer secret-backed URLs for production credentials.

## OAuth discovery

`agent-mcp` exposes `GET /.well-known/oauth-protected-resource` and returns
RFC 9728 metadata for the MCP endpoint. Unauthenticated `POST /mcp` requests
receive:

```http
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer resource_metadata="https://agent-mcp.evalops.example/.well-known/oauth-protected-resource"
```

When a bearer token is present, `agent-mcp` introspects it against Identity,
requires the token audience to match the MCP resource URL, and returns
`403 insufficient_scope` when the current tool requires additional scopes.

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
