package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/evalops/service-runtime/mtls"
	"github.com/evalops/service-runtime/startup"
)

type IdentityConfig struct {
	BaseURL        string
	IssuerURL      string
	IntrospectURL  string
	RequestTimeout time.Duration
	CacheTTL       time.Duration
	TLS            mtls.ClientConfig
}

type FederationConfig struct {
	DefaultWorkspaceID string
	AnthropicAPIKey    string
	OpenAIAPIKey       string
	GitHubToken        string
	GoogleAccessToken  string
}

type RegistryConfig struct {
	BaseURL        string
	RequestTimeout time.Duration
	TLS            mtls.ClientConfig
}

type GovernanceConfig struct {
	BaseURL        string
	RequestTimeout time.Duration
	TLS            mtls.ClientConfig
}

type ApprovalsConfig struct {
	BaseURL        string
	RequestTimeout time.Duration
	PollInterval   time.Duration
	PollTimeout    time.Duration
	EventStream    string
	EventSubject   string
	EventDurable   string
	TLS            mtls.ClientConfig
}

type MeterConfig struct {
	BaseURL        string
	RequestTimeout time.Duration
	TLS            mtls.ClientConfig
}

type MemoryConfig struct {
	BaseURL        string
	RequestTimeout time.Duration
	TLS            mtls.ClientConfig
}

type NATSConfig struct {
	URL     string
	Stream  string
	Subject string
}

type BreakerConfig struct {
	FailureThreshold int
	ResetTimeout     time.Duration
}

type SessionConfig struct {
	Store     string // "memory" for local/test fixtures, "redis" for production
	RedisURL  string
	MaxActive int
}

type RateLimitConfig struct {
	RequestsPerSecond float64
	Burst             int
}

type ProxyToolConfig struct {
	Namespace        string   `json:"namespace"`
	MCPName          string   `json:"mcp_name,omitempty"`
	UpstreamName     string   `json:"upstream_name,omitempty"`
	Endpoint         string   `json:"endpoint"`
	Description      string   `json:"description,omitempty"`
	RiskLevel        string   `json:"risk_level,omitempty"`
	CostClass        string   `json:"cost_class,omitempty"`
	ProvenanceTag    string   `json:"provenance_tag,omitempty"`
	RequiresApproval bool     `json:"requires_approval,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
}

type Config struct {
	ServiceName         string
	Environment         string
	Version             string
	Addr                string
	MaxBodyBytes        int64
	ShutdownTimeout     time.Duration
	ResourceURL         string
	SessionReapInterval time.Duration
	BackgroundMaxTasks  int
	StartupRetry        startup.Config
	TLS                 mtls.ServerConfig
	Identity            IdentityConfig
	Federation          FederationConfig
	Registry            RegistryConfig
	Governance          GovernanceConfig
	Approvals           ApprovalsConfig
	Meter               MeterConfig
	Memory              MemoryConfig
	NATS                NATSConfig
	Breaker             BreakerConfig
	Session             SessionConfig
	MCPRateLimit        RateLimitConfig
	ProxyTools          []ProxyToolConfig
	ProxyToolsParseErr  error
}

func Load() Config {
	proxyTools, proxyToolsErr := loadProxyTools()
	return Config{
		ServiceName:         envOrDefault("SERVICE_NAME", "agent-mcp"),
		Environment:         envOrDefault("ENVIRONMENT", "development"),
		Version:             envOrDefault("VERSION", "dev"),
		Addr:                envOrDefault("ADDR", ":8080"),
		MaxBodyBytes:        envOrDefaultInt64("MCP_MAX_BODY_BYTES", 1<<20),
		ShutdownTimeout:     envOrDefaultDuration("SHUTDOWN_TIMEOUT", startup.DefaultShutdownTimeout),
		ResourceURL:         trimEnv("MCP_RESOURCE_URL"),
		SessionReapInterval: envOrDefaultDuration("SESSION_REAP_INTERVAL", 30*time.Second),
		BackgroundMaxTasks:  envOrDefaultInt("BACKGROUND_MAX_IN_FLIGHT", 128),
		StartupRetry: startup.Config{
			MaxAttempts: envOrDefaultInt("STARTUP_MAX_ATTEMPTS", startup.DefaultMaxAttempts),
			Delay:       envOrDefaultDuration("STARTUP_DELAY", startup.DefaultDelay),
		},
		TLS: mtls.ServerConfig{
			CertFile:     trimEnv("TLS_CERT_FILE"),
			KeyFile:      trimEnv("TLS_KEY_FILE"),
			ClientCAFile: trimEnv("TLS_CLIENT_CA_FILE"),
		},
		Identity: IdentityConfig{
			BaseURL:        trimEnv("IDENTITY_BASE_URL"),
			IssuerURL:      trimEnv("IDENTITY_ISSUER_URL"),
			IntrospectURL:  trimEnv("IDENTITY_INTROSPECT_URL"),
			RequestTimeout: envOrDefaultDuration("IDENTITY_REQUEST_TIMEOUT", 5*time.Second),
			CacheTTL:       envOrDefaultDuration("IDENTITY_CACHE_TTL", 30*time.Second),
			TLS: mtls.ClientConfig{
				CAFile:     trimEnv("IDENTITY_CA_FILE"),
				CertFile:   trimEnv("IDENTITY_CERT_FILE"),
				KeyFile:    trimEnv("IDENTITY_KEY_FILE"),
				ServerName: trimEnv("IDENTITY_SERVER_NAME"),
			},
		},
		Federation: FederationConfig{
			DefaultWorkspaceID: envFirstNonEmpty("DEFAULT_WORKSPACE_ID", "DEFAULT_ORGANIZATION_ID"),
			AnthropicAPIKey:    trimEnv("ANTHROPIC_API_KEY"),
			OpenAIAPIKey:       trimEnv("OPENAI_API_KEY"),
			GitHubToken:        trimEnv("GITHUB_TOKEN"),
			GoogleAccessToken:  envFirstNonEmpty("GOOGLE_OAUTH_ACCESS_TOKEN", "GOOGLE_ACCESS_TOKEN"),
		},
		Registry: RegistryConfig{
			BaseURL:        trimEnv("AGENT_REGISTRY_BASE_URL"),
			RequestTimeout: envOrDefaultDuration("AGENT_REGISTRY_REQUEST_TIMEOUT", 5*time.Second),
			TLS: mtls.ClientConfig{
				CAFile:     trimEnv("AGENT_REGISTRY_CA_FILE"),
				CertFile:   trimEnv("AGENT_REGISTRY_CERT_FILE"),
				KeyFile:    trimEnv("AGENT_REGISTRY_KEY_FILE"),
				ServerName: trimEnv("AGENT_REGISTRY_SERVER_NAME"),
			},
		},
		Governance: GovernanceConfig{
			BaseURL:        trimEnv("GOVERNANCE_BASE_URL"),
			RequestTimeout: envOrDefaultDuration("GOVERNANCE_REQUEST_TIMEOUT", 5*time.Second),
			TLS: mtls.ClientConfig{
				CAFile:     trimEnv("GOVERNANCE_CA_FILE"),
				CertFile:   trimEnv("GOVERNANCE_CERT_FILE"),
				KeyFile:    trimEnv("GOVERNANCE_KEY_FILE"),
				ServerName: trimEnv("GOVERNANCE_SERVER_NAME"),
			},
		},
		Approvals: ApprovalsConfig{
			BaseURL:        trimEnv("APPROVALS_BASE_URL"),
			RequestTimeout: envOrDefaultDuration("APPROVALS_REQUEST_TIMEOUT", 5*time.Second),
			PollInterval:   envOrDefaultDuration("APPROVALS_POLL_INTERVAL", 3*time.Second),
			PollTimeout:    envOrDefaultDuration("APPROVALS_POLL_TIMEOUT", 5*time.Minute),
			EventStream:    envOrDefault("APPROVALS_EVENT_STREAM", "approvals_events"),
			EventSubject:   envOrDefault("APPROVALS_EVENT_SUBJECT", "approvals.events.approval_habit.*"),
			EventDurable:   envOrDefault("APPROVALS_EVENT_DURABLE", "agent-mcp-approval-habits"),
			TLS: mtls.ClientConfig{
				CAFile:     trimEnv("APPROVALS_CA_FILE"),
				CertFile:   trimEnv("APPROVALS_CERT_FILE"),
				KeyFile:    trimEnv("APPROVALS_KEY_FILE"),
				ServerName: trimEnv("APPROVALS_SERVER_NAME"),
			},
		},
		Meter: MeterConfig{
			BaseURL:        trimEnv("METER_BASE_URL"),
			RequestTimeout: envOrDefaultDuration("METER_REQUEST_TIMEOUT", 5*time.Second),
			TLS: mtls.ClientConfig{
				CAFile:     trimEnv("METER_CA_FILE"),
				CertFile:   trimEnv("METER_CERT_FILE"),
				KeyFile:    trimEnv("METER_KEY_FILE"),
				ServerName: trimEnv("METER_SERVER_NAME"),
			},
		},
		Memory: MemoryConfig{
			BaseURL:        trimEnv("MEMORY_BASE_URL"),
			RequestTimeout: envOrDefaultDuration("MEMORY_REQUEST_TIMEOUT", 5*time.Second),
			TLS: mtls.ClientConfig{
				CAFile:     trimEnv("MEMORY_CA_FILE"),
				CertFile:   trimEnv("MEMORY_CERT_FILE"),
				KeyFile:    trimEnv("MEMORY_KEY_FILE"),
				ServerName: trimEnv("MEMORY_SERVER_NAME"),
			},
		},
		NATS: NATSConfig{
			URL:     trimEnv("NATS_URL"),
			Stream:  envOrDefault("NATS_STREAM", "agent_mcp_events"),
			Subject: envOrDefault("NATS_SUBJECT_PREFIX", "agent-mcp.events"),
		},
		Breaker: BreakerConfig{
			FailureThreshold: envOrDefaultInt("BREAKER_FAILURE_THRESHOLD", 5),
			ResetTimeout:     envOrDefaultDuration("BREAKER_RESET_TIMEOUT", 30*time.Second),
		},
		Session: SessionConfig{
			Store:     strings.ToLower(trimEnv("SESSION_STORE")),
			RedisURL:  trimEnv("SESSION_REDIS_URL"),
			MaxActive: envOrDefaultInt("SESSION_MAX_ACTIVE", 10000),
		},
		MCPRateLimit: RateLimitConfig{
			RequestsPerSecond: envOrDefaultFloat64("MCP_RATE_LIMIT_RPS", 50),
			Burst:             envOrDefaultInt("MCP_RATE_LIMIT_BURST", 100),
		},
		ProxyTools:         proxyTools,
		ProxyToolsParseErr: proxyToolsErr,
	}
}

func (c Config) Validate() error {
	if c.Addr == "" {
		return fmt.Errorf("addr is required")
	}
	if c.Identity.BaseURL == "" {
		return fmt.Errorf("IDENTITY_BASE_URL is required")
	}
	if c.BackgroundMaxTasks < 0 {
		return fmt.Errorf("BACKGROUND_MAX_IN_FLIGHT must be >= 0")
	}
	if c.MaxBodyBytes < 0 {
		return fmt.Errorf("MCP_MAX_BODY_BYTES must be >= 0")
	}
	if c.Session.MaxActive < 0 {
		return fmt.Errorf("SESSION_MAX_ACTIVE must be >= 0")
	}
	if c.MCPRateLimit.RequestsPerSecond < 0 {
		return fmt.Errorf("MCP_RATE_LIMIT_RPS must be >= 0")
	}
	if c.MCPRateLimit.Burst < 0 {
		return fmt.Errorf("MCP_RATE_LIMIT_BURST must be >= 0")
	}
	if c.ProxyToolsParseErr != nil {
		return c.ProxyToolsParseErr
	}
	if err := validateProxyTools(c.ProxyTools); err != nil {
		return err
	}
	switch c.Session.Store {
	case "":
		return fmt.Errorf("SESSION_STORE is required; use redis for hosted deployments or memory for explicit local/test fixtures")
	case "memory":
		if strings.EqualFold(strings.TrimSpace(c.Environment), "production") {
			return fmt.Errorf("SESSION_STORE=redis is required when ENVIRONMENT=production")
		}
	case "redis":
		if c.Session.RedisURL == "" {
			return fmt.Errorf("SESSION_REDIS_URL is required when SESSION_STORE=redis")
		}
	default:
		return fmt.Errorf("SESSION_STORE must be one of memory or redis")
	}
	return nil
}

func loadProxyTools() ([]ProxyToolConfig, error) {
	raw := trimEnv("MCP_PROXY_TOOLS_JSON")
	if path := trimEnv("MCP_PROXY_TOOLS_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read MCP_PROXY_TOOLS_FILE: %w", err)
		}
		raw = string(data)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var tools []ProxyToolConfig
	if err := json.Unmarshal([]byte(raw), &tools); err != nil {
		return nil, fmt.Errorf("parse MCP proxy tools: %w", err)
	}
	return tools, nil
}

func validateProxyTools(tools []ProxyToolConfig) error {
	seenNamespaces := make(map[string]struct{}, len(tools))
	seenMCPNames := make(map[string]struct{}, len(tools))
	for i, tool := range tools {
		namespace := strings.ToLower(strings.TrimSpace(tool.Namespace))
		if !validToolNamespace(namespace) {
			return fmt.Errorf("MCP proxy tool %d namespace must follow <service>.<object>.<action>", i)
		}
		if _, ok := seenNamespaces[namespace]; ok {
			return fmt.Errorf("MCP proxy tool namespace %q is duplicated", namespace)
		}
		seenNamespaces[namespace] = struct{}{}

		mcpName := strings.TrimSpace(tool.MCPName)
		if mcpName == "" {
			mcpName = namespace
		}
		if !validMCPToolName(mcpName) {
			return fmt.Errorf("MCP proxy tool %q has invalid MCP tool name %q", namespace, mcpName)
		}
		if _, ok := seenMCPNames[mcpName]; ok {
			return fmt.Errorf("MCP proxy tool MCP name %q is duplicated", mcpName)
		}
		seenMCPNames[mcpName] = struct{}{}

		upstreamName := strings.TrimSpace(tool.UpstreamName)
		if upstreamName != "" && !validMCPToolName(upstreamName) {
			return fmt.Errorf("MCP proxy tool %q has invalid upstream tool name %q", namespace, upstreamName)
		}
		endpoint := strings.TrimSpace(tool.Endpoint)
		if endpoint == "" {
			return fmt.Errorf("MCP proxy tool %q endpoint is required", namespace)
		}
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("MCP proxy tool %q endpoint must be an absolute URL", namespace)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("MCP proxy tool %q endpoint must use http or https", namespace)
		}
	}
	return nil
}

func validToolNamespace(name string) bool {
	if strings.Count(name, ".") < 2 {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if part == "" || !validMCPToolName(part) {
			return false
		}
	}
	return true
}

func validMCPToolName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func envOrDefault(key, fallback string) string {
	if v := trimEnv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int) int {
	v := trimEnv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envOrDefaultInt64(key string, fallback int64) int64 {
	v := trimEnv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func envOrDefaultDuration(key string, fallback time.Duration) time.Duration {
	v := trimEnv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func envOrDefaultFloat64(key string, fallback float64) float64 {
	v := trimEnv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return n
}

func trimEnv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func envFirstNonEmpty(keys ...string) string {
	for _, key := range keys {
		if value := trimEnv(key); value != "" {
			return value
		}
	}
	return ""
}
