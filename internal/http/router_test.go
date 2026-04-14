package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/evalops/agent-mcp/internal/config"
)

func TestBuildRouterValidation(t *testing.T) {
	cfg := config.Config{Addr: ":8080"}
	_, err := BuildRouter(cfg)
	if err == nil {
		t.Fatal("expected validation error for missing IDENTITY_BASE_URL")
	}
}

func TestHealthEndpoints(t *testing.T) {
	os.Setenv("IDENTITY_BASE_URL", "http://localhost:9999")
	defer os.Unsetenv("IDENTITY_BASE_URL")

	cfg := config.Load()
	handler, err := BuildRouter(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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
		handler.ServeHTTP(rec, req)
		if rec.Code != tt.wantStatus {
			t.Errorf("%s: expected %d, got %d", tt.path, tt.wantStatus, rec.Code)
		}
	}
}

func TestMetricsEndpoint(t *testing.T) {
	os.Setenv("IDENTITY_BASE_URL", "http://localhost:9999")
	defer os.Unsetenv("IDENTITY_BASE_URL")

	cfg := config.Load()
	handler, err := BuildRouter(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") == "" {
		t.Fatal("expected Content-Type header on metrics")
	}
}
