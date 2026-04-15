package http

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/evalops/agent-mcp/internal/clients"
	"github.com/evalops/agent-mcp/internal/config"
)

var authTestLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func TestProtectedResourceMetadataHandler(t *testing.T) {
	cfg := config.Config{
		ResourceURL: "https://mcp.evalops.dev",
		Identity: config.IdentityConfig{
			IssuerURL: "https://identity.evalops.dev",
		},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)

	newProtectedResourceMetadataHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"resource":"https://mcp.evalops.dev"`) {
		t.Fatalf("expected resource URL in body, got %s", body)
	}
	if !strings.Contains(body, `"authorization_servers":["https://identity.evalops.dev"]`) {
		t.Fatalf("expected authorization server in body, got %s", body)
	}
	if !strings.Contains(body, `"agent:register"`) {
		t.Fatalf("expected advertised scopes in body, got %s", body)
	}
}

func TestMCPAuthMiddlewareReturns401WithProtectedResourceMetadata(t *testing.T) {
	cfg := config.Config{ResourceURL: "https://mcp.evalops.dev"}
	identityClient := clients.NewIdentityClient("http://identity.invalid", http.DefaultClient, time.Second)
	handler := newMCPAuthMiddleware(cfg, identityClient, authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_register","arguments":{"agent_type":"codex","surface":"cli"}}}`))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer resource_metadata="https://mcp.evalops.dev/.well-known/oauth-protected-resource"` {
		t.Fatalf("unexpected WWW-Authenticate header %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
		t.Fatalf("expected unauthorized body, got %s", rec.Body.String())
	}
}

func TestMCPAuthMiddlewareAllowsRegisterWithExplicitUserToken(t *testing.T) {
	cfg := config.Config{ResourceURL: "https://mcp.evalops.dev"}
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("identity introspection should not run when user_token is supplied explicitly")
	}))
	defer identitySrv.Close()

	identityClient := clients.NewIdentityClient(identitySrv.URL, identitySrv.Client(), time.Second)
	nextCalled := false
	handler := newMCPAuthMiddleware(cfg, identityClient, authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_register","arguments":{"agent_type":"codex","surface":"cli","user_token":"user_tok"}}}`))
	handler.ServeHTTP(rec, req)

	if !nextCalled {
		t.Fatal("expected next handler to run")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
}

func TestMCPAuthMiddlewareReturns403ForInsufficientScope(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tokens/introspect" {
			t.Fatalf("expected introspect path, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"active":true,"audience":["https://mcp.evalops.dev"],"scopes":["agent:register"]}`))
	}))
	defer identitySrv.Close()

	cfg := config.Config{ResourceURL: "https://mcp.evalops.dev"}
	identityClient := clients.NewIdentityClient(identitySrv.URL, identitySrv.Client(), time.Second)
	handler := newMCPAuthMiddleware(cfg, identityClient, authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_check_action","arguments":{"action":"deploy"}}}`))
	req.Header.Set("Authorization", "Bearer header.payload.sig")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer error="insufficient_scope" scope="governance:evaluate"` {
		t.Fatalf("unexpected WWW-Authenticate header %q", got)
	}
}

func TestMCPAuthMiddlewareRequiresRegisterScopeForDeregister(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"active":true,"audience":["https://mcp.evalops.dev"],"scopes":["agent:heartbeat"]}`))
	}))
	defer identitySrv.Close()

	cfg := config.Config{ResourceURL: "https://mcp.evalops.dev"}
	identityClient := clients.NewIdentityClient(identitySrv.URL, identitySrv.Client(), time.Second)
	handler := newMCPAuthMiddleware(cfg, identityClient, authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_deregister","arguments":{}}}`))
	req.Header.Set("Authorization", "Bearer header.payload.sig")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer error="insufficient_scope" scope="agent:register"` {
		t.Fatalf("unexpected WWW-Authenticate header %q", got)
	}
}

func TestMCPAuthMiddlewareRejectsAudienceMismatch(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"active":true,"audience":["https://other.evalops.dev"],"scopes":["agent:register"]}`))
	}))
	defer identitySrv.Close()

	cfg := config.Config{ResourceURL: "https://mcp.evalops.dev"}
	identityClient := clients.NewIdentityClient(identitySrv.URL, identitySrv.Client(), time.Second)
	handler := newMCPAuthMiddleware(cfg, identityClient, authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_register","arguments":{"agent_type":"codex","surface":"cli"}}}`))
	req.Header.Set("Authorization", "Bearer header.payload.sig")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMCPAuthMiddlewareAllowsValidBearerToken(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"active":true,"audience":["https://mcp.evalops.dev"],"scopes":["agent:register"]}`))
	}))
	defer identitySrv.Close()

	cfg := config.Config{ResourceURL: "https://mcp.evalops.dev"}
	identityClient := clients.NewIdentityClient(identitySrv.URL, identitySrv.Client(), time.Second)
	nextCalled := false
	handler := newMCPAuthMiddleware(cfg, identityClient, authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_register","arguments":{"agent_type":"codex","surface":"cli"}}}`))
	req.Header.Set("Authorization", "Bearer header.payload.sig")
	handler.ServeHTTP(rec, req)

	if !nextCalled {
		t.Fatal("expected next handler to run")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
}

func TestMCPAuthMiddlewareRejectsBearerTokensWithoutConfiguredResourceURL(t *testing.T) {
	identitySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"active":true,"audience":["https://attacker-service.example"],"scopes":["agent:register"]}`))
	}))
	defer identitySrv.Close()

	identityClient := clients.NewIdentityClient(identitySrv.URL, identitySrv.Client(), time.Second)
	handler := newMCPAuthMiddleware(config.Config{}, identityClient, authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_register","arguments":{"agent_type":"codex","surface":"cli"}}}`))
	req.Header.Set("Authorization", "Bearer header.payload.sig")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "attacker-service.example")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
