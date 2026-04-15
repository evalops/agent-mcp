package agentmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evalops/agent-mcp/internal/config"
)

func TestToolCreateAPIKey(t *testing.T) {
	expiresAt := time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/api-keys" {
			t.Fatalf("expected /v1/api-keys, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer user_token_123" {
			t.Fatalf("expected bearer user_token_123, got %s", r.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["name"] != "github-actions-prod" {
			t.Fatalf("expected github-actions-prod, got %v", payload["name"])
		}
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(map[string]any{
			"api_key": "pk_live_abc123",
			"key": map[string]any{
				"id":              "key_123",
				"name":            "github-actions-prod",
				"prefix":          "pk_live_a",
				"scopes":          []string{"agent:register"},
				"created_at":      time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
				"organization_id": "org_123",
				"expires_at":      expiresAt,
			},
			"scopes_granted": []string{"agent:register"},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	rc := newRequestContext(deps, "mcp_sess_1")

	_, out, err := rc.toolCreateAPIKey(context.Background(), nil, createAPIKeyInput{
		Name:          "github-actions-prod",
		Scopes:        []string{"agent:register"},
		ExpiresInDays: 365,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.APIKey != "pk_live_abc123" {
		t.Fatalf("expected api key pk_live_abc123, got %s", out.APIKey)
	}
	if out.KeyID != "key_123" {
		t.Fatalf("expected key_123, got %s", out.KeyID)
	}
	if out.Warning != apiKeyWarning {
		t.Fatalf("unexpected warning %q", out.Warning)
	}
	if out.ExpiresAt != "2027-04-15T00:00:00Z" {
		t.Fatalf("unexpected expires_at %q", out.ExpiresAt)
	}
}

func TestToolCreateAPIKeyRequiresName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("identity should not be called")
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	rc := newRequestContext(deps, "mcp_sess_1")

	_, _, err := rc.toolCreateAPIKey(context.Background(), nil, createAPIKeyInput{})
	if err == nil {
		t.Fatal("expected missing name error")
	}
}

func TestToolCreateAPIKeyUsesCircuitBreaker(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	deps.Breakers = NewBreakers(config.BreakerConfig{FailureThreshold: 1, ResetTimeout: time.Hour})
	rc := newRequestContext(deps, "mcp_sess_1")

	_, _, err := rc.toolCreateAPIKey(context.Background(), nil, createAPIKeyInput{Name: "github-actions-prod"})
	if err == nil || !strings.Contains(err.Error(), "create api key failed") {
		t.Fatalf("expected downstream failure, got %v", err)
	}
	if got := deps.Breakers.Identity.State(); got != BreakerOpen {
		t.Fatalf("expected identity breaker open, got %s", got)
	}

	_, _, err = rc.toolCreateAPIKey(context.Background(), nil, createAPIKeyInput{Name: "github-actions-prod"})
	if err == nil || !strings.Contains(err.Error(), "circuit breaker open") {
		t.Fatalf("expected breaker-open error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one CreateAPIKey call before breaker opened, got %d", calls)
	}
}

func TestToolListAPIKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v1/api-keys" {
			t.Fatalf("expected /v1/api-keys, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(map[string]any{
			"api_keys": []map[string]any{
				{
					"id":           "key_123",
					"name":         "github-actions-prod",
					"prefix":       "pk_live_a",
					"scopes":       []string{"agent:register", "meter:record"},
					"created_at":   time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
					"last_used_at": time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
				},
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	rc := newRequestContext(deps, "mcp_sess_1")

	_, out, err := rc.toolListAPIKeys(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Keys) != 1 {
		t.Fatalf("expected one key, got %#v", out.Keys)
	}
	if out.Keys[0].KeyID != "key_123" {
		t.Fatalf("expected key_123, got %s", out.Keys[0].KeyID)
	}
	if out.Keys[0].LastUsedAt != "2026-04-15T12:00:00Z" {
		t.Fatalf("unexpected last_used_at %q", out.Keys[0].LastUsedAt)
	}
}

func TestToolListAPIKeysUsesCircuitBreaker(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	deps.Breakers = NewBreakers(config.BreakerConfig{FailureThreshold: 1, ResetTimeout: time.Hour})
	rc := newRequestContext(deps, "mcp_sess_1")

	_, _, err := rc.toolListAPIKeys(context.Background(), nil, struct{}{})
	if err == nil || !strings.Contains(err.Error(), "list api keys failed") {
		t.Fatalf("expected downstream failure, got %v", err)
	}
	if got := deps.Breakers.Identity.State(); got != BreakerOpen {
		t.Fatalf("expected identity breaker open, got %s", got)
	}

	_, _, err = rc.toolListAPIKeys(context.Background(), nil, struct{}{})
	if err == nil || !strings.Contains(err.Error(), "circuit breaker open") {
		t.Fatalf("expected breaker-open error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one ListAPIKeys call before breaker opened, got %d", calls)
	}
}
