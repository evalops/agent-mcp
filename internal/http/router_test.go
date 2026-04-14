package http

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"time"
	"testing"

	"github.com/evalops/agent-mcp/internal/config"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func TestBuildRouterValidation(t *testing.T) {
	cfg := config.Config{Addr: ":8080"}
	_, err := BuildRouter(cfg, testLogger)
	if err == nil {
		t.Fatal("expected validation error for missing IDENTITY_BASE_URL")
	}
}

func TestHealthEndpoints(t *testing.T) {
	// Use a fake identity server for the health check.
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName: "test",
		Addr:        ":8080",
		Version: "test", SessionReapInterval: 30 * time.Second,
		Identity: config.IdentityConfig{
			BaseURL: identitySrv.URL,
		},
	}
	result, err := BuildRouter(cfg, testLogger)
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
	// Identity server that returns 500.
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName: "test",
		Addr:        ":8080",
		Version: "test", SessionReapInterval: 30 * time.Second,
		Identity: config.IdentityConfig{
			BaseURL: identitySrv.URL,
		},
	}
	result, err := BuildRouter(cfg, testLogger)
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

func TestMetricsEndpoint(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName: "test",
		Addr:        ":8080",
		Version: "test", SessionReapInterval: 30 * time.Second,
		Identity: config.IdentityConfig{
			BaseURL: identitySrv.URL,
		},
	}
	result, err := BuildRouter(cfg, testLogger)
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

func TestOptionalServicesWiring(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer identitySrv.Close()

	cfg := config.Config{
		ServiceName: "test",
		Addr:        ":8080",
		Version: "test", SessionReapInterval: 30 * time.Second,
		Identity:    config.IdentityConfig{BaseURL: identitySrv.URL},
		Registry:    config.RegistryConfig{BaseURL: "http://registry:8080"},
		Governance:  config.GovernanceConfig{BaseURL: "http://governance:8080"},
		Approvals:   config.ApprovalsConfig{BaseURL: "http://approvals:8080"},
		Meter:       config.MeterConfig{BaseURL: "http://meter:8080"},
		Memory:      config.MemoryConfig{BaseURL: "http://memory:8080"},
	}
	result, err := BuildRouter(cfg, testLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer result.Cleanup(nil)

	// Verify all optional clients are wired.
	if result.Deps.Registry == nil {
		t.Fatal("expected registry client to be wired")
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
