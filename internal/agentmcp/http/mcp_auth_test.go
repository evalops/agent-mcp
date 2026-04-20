package http

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/evalops/agent-mcp/internal/agentmcp/agentmcp"
	"github.com/evalops/agent-mcp/internal/agentmcp/clients"
	"github.com/evalops/agent-mcp/internal/agentmcp/config"
	"github.com/evalops/service-runtime/authmw"
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

func TestMCPAuthMiddlewareCreatesAnonymousSessionForUnauthenticatedRequests(t *testing.T) {
	cfg := config.Config{ResourceURL: "https://mcp.evalops.dev"}
	identityClient := clients.NewIdentityClient("http://identity.invalid", http.DefaultClient, time.Second)
	sessions := agentmcp.NewSessionStore()
	nextCalled := false
	handler := newMCPAuthMiddleware(cfg, identityClient, sessions, authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_check_action","arguments":{"action_type":"Bash"}}}`))
	req.Header.Set("Mcp-Session-Id", "sess_anon")
	handler.ServeHTTP(rec, req)

	if !nextCalled {
		t.Fatal("expected next handler to run")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	state, ok := sessions.Get("sess_anon")
	if !ok || state == nil || !state.IsAnonymous() {
		t.Fatalf("expected anonymous session state, got %#v", state)
	}
}

func TestMCPAuthMiddlewareAllowsRegisterWhenFederationCredentialConfigured(t *testing.T) {
	cfg := config.Config{
		Federation:  config.FederationConfig{OpenAIAPIKey: "sk-openai"},
		ResourceURL: "https://mcp.evalops.dev",
	}
	identityClient := clients.NewIdentityClient("http://identity.invalid", http.DefaultClient, time.Second)
	sessions := agentmcp.NewSessionStore()
	nextCalled := false
	handler := newMCPAuthMiddleware(cfg, identityClient, sessions, authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_register","arguments":{"agent_type":"codex","surface":"cli"}}}`))
	req.Header.Set("Mcp-Session-Id", "sess_reg")
	handler.ServeHTTP(rec, req)

	if !nextCalled {
		t.Fatal("expected next handler to run")
	}
	if _, ok := sessions.Get("sess_reg"); ok {
		t.Fatal("did not expect anonymous session when register can use configured federation")
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
	handler := newMCPAuthMiddleware(cfg, identityClient, agentmcp.NewSessionStore(), authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := newMCPAuthMiddleware(cfg, identityClient, agentmcp.NewSessionStore(), authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := newMCPAuthMiddleware(cfg, identityClient, agentmcp.NewSessionStore(), authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := newMCPAuthMiddleware(cfg, identityClient, agentmcp.NewSessionStore(), authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		_, _ = w.Write([]byte(`{"active":true,"audience":["https://mcp.evalops.dev"],"organization_id":"org_123","scopes":["agent:register"],"subject":"user_123","token_type":"user"}`))
	}))
	defer identitySrv.Close()

	cfg := config.Config{ResourceURL: "https://mcp.evalops.dev"}
	identityClient := clients.NewIdentityClient(identitySrv.URL, identitySrv.Client(), time.Second)
	nextCalled := false
	handler := newMCPAuthMiddleware(cfg, identityClient, agentmcp.NewSessionStore(), authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		principal, ok := authmw.PrincipalFromContext(r.Context())
		if !ok {
			t.Fatal("expected authmw principal in request context")
		}
		if principal.OrganizationID != "org_123" || principal.WorkspaceID != "org_123" || principal.Subject != "user_123" {
			t.Fatalf("unexpected principal: %#v", principal)
		}
		if principal.TokenType != "user" || !principal.IsHuman {
			t.Fatalf("expected human user principal, got %#v", principal)
		}
		if !authmw.HasAllScopes(principal.Scopes, []string{"agent:register"}) {
			t.Fatalf("unexpected principal scopes: %#v", principal.Scopes)
		}
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
	handler := newMCPAuthMiddleware(config.Config{}, identityClient, agentmcp.NewSessionStore(), authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestMCPAuthMiddlewareRateLimitsAnonymousRequests(t *testing.T) {
	cfg := config.Config{ResourceURL: "https://mcp.evalops.dev"}
	identityClient := clients.NewIdentityClient("http://identity.invalid", http.DefaultClient, time.Second)
	handler := newMCPAuthMiddleware(cfg, identityClient, agentmcp.NewSessionStore(), authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := 0; i < anonymousPerMinuteLimit; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_check_action","arguments":{"action_type":"Bash"}}}`))
		req.RemoteAddr = "203.0.113.10:54321"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: expected 204, got %d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_check_action","arguments":{"action_type":"Bash"}}}`))
	req.RemoteAddr = "203.0.113.10:54321"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCPAuthMiddlewareRejectsAnonymousSessionWhenSessionLimitReached(t *testing.T) {
	cfg := config.Config{
		ResourceURL: "https://mcp.evalops.dev",
		Session:     config.SessionConfig{MaxActive: 1},
	}
	identityClient := clients.NewIdentityClient("http://identity.invalid", http.DefaultClient, time.Second)
	sessions := agentmcp.NewSessionStore()
	sessions.Set("existing", &agentmcp.SessionState{SessionType: agentmcp.SessionTypeAnonymous, ExpiresAt: time.Now().Add(time.Hour)})
	handler := newMCPAuthMiddleware(cfg, identityClient, sessions, authTestLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not run when session limit is reached")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"evalops_check_action","arguments":{"action_type":"Bash"}}}`))
	req.Header.Set("Mcp-Session-Id", "new_anon")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "session_limit_exceeded") {
		t.Fatalf("expected session_limit_exceeded response, got %s", rec.Body.String())
	}
	if _, ok := sessions.Get("new_anon"); ok {
		t.Fatal("new anonymous session should not be created after limit rejection")
	}
}
