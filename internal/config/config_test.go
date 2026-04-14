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
