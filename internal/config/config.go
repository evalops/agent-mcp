package config

import (
	"fmt"
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
	Store    string // "memory" or "redis"
	RedisURL string
}

type Config struct {
	ServiceName         string
	Environment         string
	Version             string
	Addr                string
	ResourceURL         string
	SessionReapInterval time.Duration
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
}

func Load() Config {
	return Config{
		ServiceName:         envOrDefault("SERVICE_NAME", "agent-mcp"),
		Environment:         envOrDefault("ENVIRONMENT", "development"),
		Version:             envOrDefault("VERSION", "dev"),
		Addr:                envOrDefault("ADDR", ":8080"),
		ResourceURL:         trimEnv("MCP_RESOURCE_URL"),
		SessionReapInterval: envOrDefaultDuration("SESSION_REAP_INTERVAL", 30*time.Second),
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
			BaseURL:        trimEnv("REGISTRY_BASE_URL"),
			RequestTimeout: envOrDefaultDuration("REGISTRY_REQUEST_TIMEOUT", 5*time.Second),
			TLS: mtls.ClientConfig{
				CAFile:     trimEnv("REGISTRY_CA_FILE"),
				CertFile:   trimEnv("REGISTRY_CERT_FILE"),
				KeyFile:    trimEnv("REGISTRY_KEY_FILE"),
				ServerName: trimEnv("REGISTRY_SERVER_NAME"),
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
			Store:    strings.ToLower(envOrDefault("SESSION_STORE", "memory")),
			RedisURL: trimEnv("SESSION_REDIS_URL"),
		},
	}
}

func (c Config) Validate() error {
	if c.Addr == "" {
		return fmt.Errorf("addr is required")
	}
	if c.Identity.BaseURL == "" {
		return fmt.Errorf("IDENTITY_BASE_URL is required")
	}
	if strings.TrimSpace(c.ResourceURL) == "" {
		return fmt.Errorf("MCP_RESOURCE_URL is required")
	}
	switch c.Session.Store {
	case "", "memory":
	case "redis":
		if c.Session.RedisURL == "" {
			return fmt.Errorf("SESSION_REDIS_URL is required when SESSION_STORE=redis")
		}
	default:
		return fmt.Errorf("SESSION_STORE must be one of memory or redis")
	}
	return nil
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
