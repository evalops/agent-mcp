package config

import (
	"os"
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
	if cfg.Identity.RequestTimeout != 5*time.Second {
		t.Fatalf("expected 5s, got %v", cfg.Identity.RequestTimeout)
	}
	if cfg.NATS.Stream != "agent_mcp_events" {
		t.Fatalf("expected default nats stream agent_mcp_events, got %s", cfg.NATS.Stream)
	}
	if cfg.NATS.Subject != "agent-mcp.events" {
		t.Fatalf("expected default nats subject agent-mcp.events, got %s", cfg.NATS.Subject)
	}
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("IDENTITY_BASE_URL", "http://identity:8080")
	os.Setenv("GOVERNANCE_BASE_URL", "http://governance:8080")
	defer os.Unsetenv("IDENTITY_BASE_URL")
	defer os.Unsetenv("GOVERNANCE_BASE_URL")

	cfg := Load()
	if cfg.Identity.BaseURL != "http://identity:8080" {
		t.Fatalf("expected identity URL, got %s", cfg.Identity.BaseURL)
	}
	if cfg.Governance.BaseURL != "http://governance:8080" {
		t.Fatalf("expected governance URL, got %s", cfg.Governance.BaseURL)
	}
}

func TestValidateRequiresIdentity(t *testing.T) {
	cfg := Load()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing IDENTITY_BASE_URL")
	}

	os.Setenv("IDENTITY_BASE_URL", "http://identity:8080")
	defer os.Unsetenv("IDENTITY_BASE_URL")

	cfg = Load()
	err = cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
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

func TestValidateRejectsUnknownSessionStore(t *testing.T) {
	t.Setenv("IDENTITY_BASE_URL", "http://identity:8080")
	t.Setenv("SESSION_STORE", "sqlite")

	cfg := Load()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unknown session store")
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
}
