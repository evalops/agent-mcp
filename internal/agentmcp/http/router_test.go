package http

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/evalops/agent-mcp/internal/agentmcp/agentmcp"
	"github.com/evalops/agent-mcp/internal/agentmcp/config"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func TestBuildRouterValidation(t *testing.T) {
	cfg := config.Config{Addr: ":8080"}
	_, err := BuildRouter(context.Background(), cfg, testLogger)
	if err == nil {
		t.Fatal("expected validation error for missing IDENTITY_BASE_URL")
	}
}

func TestHealthEndpoints(t *testing.T) {
	// Use a fake identity server for the health check.
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName: "test",
		Addr:        ":8080",
		Version:     "test", SessionReapInterval: 30 * time.Second,
		Breaker: config.BreakerConfig{FailureThreshold: 5, ResetTimeout: 30 * time.Second},
		Identity: config.IdentityConfig{
			BaseURL: identitySrv.URL,
		},
		Session: config.SessionConfig{Store: "memory"},
	}
	result, err := BuildRouter(context.Background(), cfg, testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer result.Cleanup(nil)

	tests := []struct {
		path       string
		wantStatus int
	}{
		{"/healthz", http.StatusOK},
		{"/readyz", http.StatusOK},
	}

	for _, tt := range tests {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		result.Handler.ServeHTTP(rec, req)
		if rec.Code != tt.wantStatus {
			t.Errorf("%s: expected %d, got %d", tt.path, tt.wantStatus, rec.Code)
		}
	}
}

func TestReadyzFailsWhenIdentityDown(t *testing.T) {
	// Use a port nothing listens on so the health check request fails to connect.
	cfg := config.Config{
		ServiceName: "test",
		Addr:        ":8080",
		Version:     "test", SessionReapInterval: 30 * time.Second,
		Breaker: config.BreakerConfig{FailureThreshold: 5, ResetTimeout: 30 * time.Second},
		Identity: config.IdentityConfig{
			BaseURL: "http://127.0.0.1:19999",
		},
		Session: config.SessionConfig{Store: "memory"},
	}
	result, err := BuildRouter(context.Background(), cfg, testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer result.Cleanup(nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	result.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when identity is down, got %d", rec.Code)
	}
}

func TestReadyzFailsWhenIdentityHealthzReturnsError(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName: "test",
		Addr:        ":8080",
		Version:     "test", SessionReapInterval: 30 * time.Second,
		Breaker: config.BreakerConfig{FailureThreshold: 5, ResetTimeout: 30 * time.Second},
		Identity: config.IdentityConfig{
			BaseURL: identitySrv.URL,
		},
		Session: config.SessionConfig{Store: "memory"},
	}
	result, err := BuildRouter(context.Background(), cfg, testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer result.Cleanup(nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	result.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when /healthz returns 500, got %d", rec.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName: "test",
		Addr:        ":8080",
		Version:     "test", SessionReapInterval: 30 * time.Second,
		Breaker: config.BreakerConfig{FailureThreshold: 5, ResetTimeout: 30 * time.Second},
		Identity: config.IdentityConfig{
			BaseURL: identitySrv.URL,
		},
		Session: config.SessionConfig{Store: "memory"},
	}
	result, err := BuildRouter(context.Background(), cfg, testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer result.Cleanup(nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	result.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMCPRouteAppliesEndpointRateLimit(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName:         "test",
		Addr:                ":8080",
		Version:             "test",
		SessionReapInterval: 30 * time.Second,
		Breaker:             config.BreakerConfig{FailureThreshold: 5, ResetTimeout: 30 * time.Second},
		Identity:            config.IdentityConfig{BaseURL: identitySrv.URL},
		Session:             config.SessionConfig{Store: "memory"},
		MCPRateLimit:        config.RateLimitConfig{RequestsPerSecond: 0.1, Burst: 1},
	}
	result, err := BuildRouter(context.Background(), cfg, testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer result.Cleanup(context.Background())

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	firstReq.RemoteAddr = "203.0.113.55:10001"
	result.Handler.ServeHTTP(first, firstReq)
	if first.Code == http.StatusTooManyRequests {
		t.Fatalf("first request should consume the burst, got 429 body=%s", first.Body.String())
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	secondReq.RemoteAddr = "203.0.113.55:10002"
	result.Handler.ServeHTTP(second, secondReq)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second request to be rate limited, got %d body=%s", second.Code, second.Body.String())
	}
}

func TestMCPRouteAppliesMaxBodySize(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName:         "test",
		Addr:                ":8080",
		Version:             "test",
		MaxBodyBytes:        8,
		SessionReapInterval: 30 * time.Second,
		Breaker:             config.BreakerConfig{FailureThreshold: 5, ResetTimeout: 30 * time.Second},
		Identity:            config.IdentityConfig{BaseURL: identitySrv.URL},
		Session:             config.SessionConfig{Store: "memory"},
		MCPRateLimit:        config.RateLimitConfig{RequestsPerSecond: 50, Burst: 100},
	}
	result, err := BuildRouter(context.Background(), cfg, testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer result.Cleanup(context.Background())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", 32)))
	req.RemoteAddr = "203.0.113.56:10001"
	result.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOptionalServicesWiring(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName: "test",
		Addr:        ":8080",
		Version:     "test", SessionReapInterval: 30 * time.Second,
		Breaker:    config.BreakerConfig{FailureThreshold: 5, ResetTimeout: 30 * time.Second},
		Identity:   config.IdentityConfig{BaseURL: identitySrv.URL},
		Registry:   config.RegistryConfig{BaseURL: "http://agent-registry:8080"},
		Governance: config.GovernanceConfig{BaseURL: "http://governance:8080"},
		Approvals:  config.ApprovalsConfig{BaseURL: "http://approvals:8080"},
		Meter:      config.MeterConfig{BaseURL: "http://meter:8080"},
		Memory:     config.MemoryConfig{BaseURL: "http://memory:8080"},
		Session:    config.SessionConfig{Store: "memory"},
	}
	result, err := BuildRouter(context.Background(), cfg, testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer result.Cleanup(nil)

	// Verify all optional clients are wired.
	if result.Deps.Registry == nil {
		t.Fatal("expected agent-registry client to be wired")
	}
	if result.Deps.Governance == nil {
		t.Fatal("expected governance client to be wired")
	}
	if result.Deps.Approvals == nil {
		t.Fatal("expected approvals client to be wired")
	}
	if result.Deps.Meter == nil {
		t.Fatal("expected meter client to be wired")
	}
	if result.Deps.Memory == nil {
		t.Fatal("expected memory client to be wired")
	}
}

func TestCleanupSkipsDeregisterForRedisSessions(t *testing.T) {
	redisServer := miniredis.RunT(t)

	deregisterCalls := 0
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/v1/agents/deregister":
			deregisterCalls++
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName: "test",
		Addr:        ":8080",
		Version:     "test", SessionReapInterval: 30 * time.Second,
		Breaker:  config.BreakerConfig{FailureThreshold: 5, ResetTimeout: 30 * time.Second},
		Identity: config.IdentityConfig{BaseURL: identitySrv.URL},
		Session:  config.SessionConfig{Store: "redis", RedisURL: "redis://" + redisServer.Addr()},
	}
	result, err := BuildRouter(context.Background(), cfg, testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result.Deps.Sessions.Set("sess_1", &agentmcp.SessionState{
		AgentID:    "agent_1",
		AgentToken: "tok_1",
		ExpiresAt:  time.Now().Add(time.Hour),
	})

	result.Cleanup(context.Background())

	if deregisterCalls != 0 {
		t.Fatalf("expected redis-backed cleanup to skip deregistration, got %d calls", deregisterCalls)
	}
}
