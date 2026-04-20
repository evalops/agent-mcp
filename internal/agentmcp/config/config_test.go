package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ServiceName != "agent-mcp" {
		t.Fatalf("expected agent-mcp, got %s", cfg.ServiceName)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("expected :8080, got %s", cfg.Addr)
	}
	if cfg.MaxBodyBytes != 1<<20 {
		t.Fatalf("expected default max body bytes 1MiB, got %d", cfg.MaxBodyBytes)
	}
	if cfg.Identity.RequestTimeout != 5*time.Second {
		t.Fatalf("expected 5s, got %v", cfg.Identity.RequestTimeout)
	}
	if cfg.NATS.Stream != "agent_mcp_events" {
		t.Fatalf("expected default nats stream agent_mcp_events, got %s", cfg.NATS.Stream)
	}
	if cfg.NATS.Subject != "agent-mcp.events" {
		t.Fatalf("expected default nats subject agent-mcp.events, got %s", cfg.NATS.Subject)
	}
	if cfg.Approvals.EventStream != "approvals_events" {
		t.Fatalf("expected default approvals event stream approvals_events, got %s", cfg.Approvals.EventStream)
	}
	if cfg.Approvals.EventSubject != "approvals.events.approval_habit.*" {
		t.Fatalf("expected default approvals event subject, got %s", cfg.Approvals.EventSubject)
	}
	if cfg.Approvals.EventDurable != "agent-mcp-approval-habits" {
		t.Fatalf("expected default approvals event durable, got %s", cfg.Approvals.EventDurable)
	}
	if cfg.Session.MaxActive != 10000 {
		t.Fatalf("expected default max active sessions 10000, got %d", cfg.Session.MaxActive)
	}
	if cfg.Session.Store != "" {
		t.Fatalf("expected session store to require explicit configuration, got %q", cfg.Session.Store)
	}
	if cfg.BackgroundMaxTasks != 128 {
		t.Fatalf("expected default background max tasks 128, got %d", cfg.BackgroundMaxTasks)
	}
	if cfg.MCPRateLimit.RequestsPerSecond != 50 || cfg.MCPRateLimit.Burst != 100 {
		t.Fatalf("unexpected default mcp rate limit: %#v", cfg.MCPRateLimit)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("IDENTITY_BASE_URL", "http://identity:8080")
	t.Setenv("IDENTITY_ISSUER_URL", "https://identity.evalops.dev")
	t.Setenv("GOVERNANCE_BASE_URL", "http://governance:8080")
	t.Setenv("MCP_RESOURCE_URL", "https://mcp.evalops.dev")
	t.Setenv("SESSION_MAX_ACTIVE", "42")
	t.Setenv("BACKGROUND_MAX_IN_FLIGHT", "7")
	t.Setenv("MCP_MAX_BODY_BYTES", "2048")
	t.Setenv("MCP_RATE_LIMIT_RPS", "12.5")
	t.Setenv("MCP_RATE_LIMIT_BURST", "13")

	cfg := Load()
	if cfg.Identity.BaseURL != "http://identity:8080" {
		t.Fatalf("expected identity URL, got %s", cfg.Identity.BaseURL)
	}
	if cfg.Identity.IssuerURL != "https://identity.evalops.dev" {
		t.Fatalf("expected identity issuer URL, got %s", cfg.Identity.IssuerURL)
	}
	if cfg.Governance.BaseURL != "http://governance:8080" {
		t.Fatalf("expected governance URL, got %s", cfg.Governance.BaseURL)
	}
	if cfg.ResourceURL != "https://mcp.evalops.dev" {
		t.Fatalf("expected protected resource URL, got %s", cfg.ResourceURL)
	}
	if cfg.Session.MaxActive != 42 {
		t.Fatalf("expected max active sessions from env, got %d", cfg.Session.MaxActive)
	}
	if cfg.BackgroundMaxTasks != 7 {
		t.Fatalf("expected background max tasks from env, got %d", cfg.BackgroundMaxTasks)
	}
	if cfg.MaxBodyBytes != 2048 {
		t.Fatalf("expected max body bytes from env, got %d", cfg.MaxBodyBytes)
	}
	if cfg.MCPRateLimit.RequestsPerSecond != 12.5 || cfg.MCPRateLimit.Burst != 13 {
		t.Fatalf("unexpected mcp rate limit from env: %#v", cfg.MCPRateLimit)
	}
}

func TestLoadMCPProxyToolsFromEnv(t *testing.T) {
	t.Setenv("MCP_PROXY_TOOLS_JSON", `[{"namespace":"github.pr.search","endpoint":"https://mcp-firewall.example/mcp","mcp_name":"github_pr_search","upstream_name":"search_pull_requests","description":"Search GitHub pull requests","risk_level":"medium","cost_class":"read","requires_approval":true,"scopes":["github:read"],"provenance_tag":"agent-mcp:mcp-firewall:github.pr.search"}]`)

	cfg := Load()
	if cfg.ProxyToolsParseErr != nil {
		t.Fatalf("unexpected parse error: %v", cfg.ProxyToolsParseErr)
	}
	if len(cfg.ProxyTools) != 1 {
		t.Fatalf("expected one proxy tool, got %#v", cfg.ProxyTools)
	}
	tool := cfg.ProxyTools[0]
	if tool.Namespace != "github.pr.search" || tool.Endpoint != "https://mcp-firewall.example/mcp" {
		t.Fatalf("unexpected proxy tool: %#v", tool)
	}
	if tool.MCPName != "github_pr_search" || tool.UpstreamName != "search_pull_requests" {
		t.Fatalf("unexpected proxy mcp names: %#v", tool)
	}
	if !tool.RequiresApproval || len(tool.Scopes) != 1 || tool.Scopes[0] != "github:read" {
		t.Fatalf("unexpected proxy policy metadata: %#v", tool)
	}
}

func TestLoadMCPProxyToolsFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-tools.json")
	if err := os.WriteFile(path, []byte(`[{"namespace":"linear.issue.create","endpoint":"http://mcp-firewall:8080/mcp"}]`), 0o600); err != nil {
		t.Fatalf("write proxy tool fixture: %v", err)
	}
	t.Setenv("MCP_PROXY_TOOLS_FILE", path)

	cfg := Load()
	if cfg.ProxyToolsParseErr != nil {
		t.Fatalf("unexpected parse error: %v", cfg.ProxyToolsParseErr)
	}
	if len(cfg.ProxyTools) != 1 || cfg.ProxyTools[0].Namespace != "linear.issue.create" {
		t.Fatalf("unexpected proxy tools from file: %#v", cfg.ProxyTools)
	}
}

func TestValidateRequiresIdentity(t *testing.T) {
	cfg := Load()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing IDENTITY_BASE_URL")
	}

	t.Setenv("IDENTITY_BASE_URL", "http://identity:8080")
	t.Setenv("SESSION_STORE", "memory")

	cfg = Load()
	err = cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRequiresExplicitSessionStore(t *testing.T) {
	t.Setenv("IDENTITY_BASE_URL", "http://identity:8080")

	cfg := Load()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing session store")
	}
}

func TestValidateRequiresRedisURLForRedisSessionStore(t *testing.T) {
	t.Setenv("IDENTITY_BASE_URL", "http://identity:8080")
	t.Setenv("SESSION_STORE", "redis")

	cfg := Load()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for redis session store without redis URL")
	}
}

func TestValidateRequiresRedisSessionStoreInProduction(t *testing.T) {
	t.Setenv("IDENTITY_BASE_URL", "http://identity:8080")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("SESSION_STORE", "memory")

	cfg := Load()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for memory session store in production")
	}
}

func TestValidateAllowsRedisSessionStoreInProduction(t *testing.T) {
	t.Setenv("IDENTITY_BASE_URL", "http://identity:8080")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("SESSION_STORE", "redis")
	t.Setenv("SESSION_REDIS_URL", "redis://redis:6379/0")

	cfg := Load()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRejectsUnknownSessionStore(t *testing.T) {
	t.Setenv("IDENTITY_BASE_URL", "http://identity:8080")
	t.Setenv("SESSION_STORE", "sqlite")

	cfg := Load()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unknown session store")
	}
}

func TestValidateRejectsNegativeSafetyLimits(t *testing.T) {
	cfg := Config{
		Addr:               ":8080",
		Identity:           IdentityConfig{BaseURL: "http://identity:8080"},
		BackgroundMaxTasks: -1,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for negative background task limit")
	}

	cfg.BackgroundMaxTasks = 1
	cfg.Session.MaxActive = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for negative session limit")
	}

	cfg.Session.MaxActive = 1
	cfg.MCPRateLimit.RequestsPerSecond = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for negative mcp rps")
	}

	cfg.MCPRateLimit.RequestsPerSecond = 1
	cfg.MCPRateLimit.Burst = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for negative mcp burst")
	}

	cfg.MCPRateLimit.Burst = 1
	cfg.MaxBodyBytes = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for negative max body bytes")
	}
}

func TestValidateRejectsInvalidProxyTools(t *testing.T) {
	base := Config{
		Addr:     ":8080",
		Identity: IdentityConfig{BaseURL: "http://identity:8080"},
		Session:  SessionConfig{Store: "memory"},
	}

	cases := []struct {
		name string
		tool ProxyToolConfig
		want string
	}{
		{
			name: "namespace",
			tool: ProxyToolConfig{Namespace: "github", Endpoint: "https://mcp-firewall.example/mcp"},
			want: "namespace must follow",
		},
		{
			name: "endpoint",
			tool: ProxyToolConfig{Namespace: "github.pr.search"},
			want: "endpoint is required",
		},
		{
			name: "mcp_name",
			tool: ProxyToolConfig{Namespace: "github.pr.search", Endpoint: "https://mcp-firewall.example/mcp", MCPName: "github pr search"},
			want: "invalid MCP tool name",
		},
		{
			name: "scheme",
			tool: ProxyToolConfig{Namespace: "github.pr.search", Endpoint: "ftp://mcp-firewall.example/mcp"},
			want: "http or https",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.ProxyTools = []ProxyToolConfig{tc.tool}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected validation error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateRejectsDuplicateProxyTools(t *testing.T) {
	cfg := Config{
		Addr:     ":8080",
		Identity: IdentityConfig{BaseURL: "http://identity:8080"},
		Session:  SessionConfig{Store: "memory"},
		ProxyTools: []ProxyToolConfig{
			{Namespace: "github.pr.search", Endpoint: "https://mcp-firewall.example/mcp", MCPName: "github_pr_search"},
			{Namespace: "github.issue.search", Endpoint: "https://mcp-firewall.example/mcp", MCPName: "github_pr_search"},
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected duplicate validation error, got %v", err)
	}
}

func TestLoadNormalizesSessionStoreCase(t *testing.T) {
	t.Setenv("SESSION_STORE", "ReDiS")

	cfg := Load()
	if cfg.Session.Store != "redis" {
		t.Fatalf("expected normalized session store redis, got %q", cfg.Session.Store)
	}
}

func TestLoadNATSFromEnv(t *testing.T) {
	t.Setenv("NATS_URL", "nats://nats:4222")
	t.Setenv("NATS_STREAM", "shared_events")
	t.Setenv("NATS_SUBJECT_PREFIX", "shared.events")
	t.Setenv("APPROVALS_EVENT_STREAM", "approval_events")
	t.Setenv("APPROVALS_EVENT_SUBJECT", "approvals.events.approval_habit.habit-learned")
	t.Setenv("APPROVALS_EVENT_DURABLE", "agent-mcp-approval-habits-test")

	cfg := Load()
	if cfg.NATS.URL != "nats://nats:4222" {
		t.Fatalf("expected nats url from env, got %q", cfg.NATS.URL)
	}
	if cfg.NATS.Stream != "shared_events" {
		t.Fatalf("expected nats stream shared_events, got %q", cfg.NATS.Stream)
	}
	if cfg.NATS.Subject != "shared.events" {
		t.Fatalf("expected nats subject shared.events, got %q", cfg.NATS.Subject)
	}
	if cfg.Approvals.EventStream != "approval_events" {
		t.Fatalf("expected approvals event stream approval_events, got %q", cfg.Approvals.EventStream)
	}
	if cfg.Approvals.EventSubject != "approvals.events.approval_habit.habit-learned" {
		t.Fatalf("expected approvals event subject from env, got %q", cfg.Approvals.EventSubject)
	}
	if cfg.Approvals.EventDurable != "agent-mcp-approval-habits-test" {
		t.Fatalf("expected approvals event durable from env, got %q", cfg.Approvals.EventDurable)
	}
}

func TestLoadFederationFromEnv(t *testing.T) {
	t.Setenv("DEFAULT_WORKSPACE_ID", "ws_default")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "google-token")

	cfg := Load()
	if cfg.Federation.DefaultWorkspaceID != "ws_default" {
		t.Fatalf("expected default workspace ws_default, got %q", cfg.Federation.DefaultWorkspaceID)
	}
	if cfg.Federation.AnthropicAPIKey != "anthropic-key" {
		t.Fatalf("expected anthropic key, got %q", cfg.Federation.AnthropicAPIKey)
	}
	if cfg.Federation.OpenAIAPIKey != "openai-key" {
		t.Fatalf("expected openai key, got %q", cfg.Federation.OpenAIAPIKey)
	}
	if cfg.Federation.GitHubToken != "github-token" {
		t.Fatalf("expected github token, got %q", cfg.Federation.GitHubToken)
	}
	if cfg.Federation.GoogleAccessToken != "google-token" {
		t.Fatalf("expected google token, got %q", cfg.Federation.GoogleAccessToken)
	}
}

func TestLoadFederationDefaultWorkspaceFallsBackToOrganizationEnv(t *testing.T) {
	t.Setenv("DEFAULT_ORGANIZATION_ID", "org_default")

	cfg := Load()
	if cfg.Federation.DefaultWorkspaceID != "org_default" {
		t.Fatalf("expected default workspace from DEFAULT_ORGANIZATION_ID, got %q", cfg.Federation.DefaultWorkspaceID)
	}
}
