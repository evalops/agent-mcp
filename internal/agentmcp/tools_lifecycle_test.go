package agentmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evalops/agent-mcp/internal/clients"
	"github.com/evalops/agent-mcp/internal/config"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestDeps(identitySrv *httptest.Server) *Deps {
	cfg := config.Config{
		ServiceName: "agent-mcp-test",
		Version:     "test",
		Identity: config.IdentityConfig{
			BaseURL:        identitySrv.URL,
			RequestTimeout: 5 * time.Second,
		},
	}
	return &Deps{
		Identity: clients.NewIdentityClient(identitySrv.URL, identitySrv.Client(), 5*time.Second),
		Config:   cfg,
		Sessions: NewSessionStore(),
	}
}

func newRequestContext(deps *Deps, sessionID string) *requestContext {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	req.Header.Set("Authorization", "Bearer user_token_123")
	return &requestContext{deps: deps, request: req}
}

func TestToolRegister(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(clients.AgentSession{
			AgentID:       "agent_test",
			AgentToken:    "tok_new",
			ExpiresAt:     time.Now().Add(time.Hour),
			RunID:         "run_test",
			ScopesGranted: []string{"governance:read"},
		})
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	rc := newRequestContext(deps, "mcp_sess_1")

	_, out, err := rc.toolRegister(context.Background(), nil, registerInput{
		AgentType: "claude-code",
		Surface:   "cli",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Registered {
		t.Fatal("expected registered=true")
	}
	if out.AgentID != "agent_test" {
		t.Fatalf("expected agent_test, got %s", out.AgentID)
	}
	if out.RunID != "run_test" {
		t.Fatalf("expected run_test, got %s", out.RunID)
	}

	// Verify session was stored.
	state, ok := deps.Sessions.Get("mcp_sess_1")
	if !ok {
		t.Fatal("expected session to be stored")
	}
	if state.AgentID != "agent_test" {
		t.Fatalf("expected agent_test in session, got %s", state.AgentID)
	}
	if state.AgentToken != "tok_new" {
		t.Fatalf("expected tok_new in session, got %s", state.AgentToken)
	}
}

func TestToolRegisterMissingAgentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("identity should not be called")
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	rc := newRequestContext(deps, "mcp_sess_1")

	_, _, err := rc.toolRegister(context.Background(), nil, registerInput{
		Surface: "cli",
	})
	if err == nil {
		t.Fatal("expected error for missing agent_type")
	}
}

func TestToolRegisterMissingToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("identity should not be called")
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	// No authorization header.
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "mcp_sess_1")
	rc := &requestContext{deps: deps, request: req}

	_, _, err := rc.toolRegister(context.Background(), nil, registerInput{
		AgentType: "claude-code",
		Surface:   "cli",
	})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestToolHeartbeat(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(clients.AgentSession{
			AgentID:    "agent_test",
			AgentToken: "tok_rotated",
			ExpiresAt:  time.Now().Add(2 * time.Hour),
			RunID:      "run_test",
		})
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	// Pre-populate session state.
	deps.Sessions.Set("mcp_sess_1", &SessionState{
		AgentID:    "agent_test",
		AgentToken: "tok_old",
		ExpiresAt:  time.Now().Add(time.Hour),
		RunID:      "run_test",
		Surface:    "cli",
	})
	rc := newRequestContext(deps, "mcp_sess_1")

	_, out, err := rc.toolHeartbeat(context.Background(), nil, heartbeatInput{TTLSeconds: 3600})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Renewed {
		t.Fatal("expected renewed=true")
	}
	if out.AgentID != "agent_test" {
		t.Fatalf("expected agent_test, got %s", out.AgentID)
	}

	// Verify token was rotated in session store.
	state, _ := deps.Sessions.Get("mcp_sess_1")
	if state.AgentToken != "tok_rotated" {
		t.Fatalf("expected tok_rotated in session, got %s", state.AgentToken)
	}
}

func TestToolHeartbeatNoSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	rc := newRequestContext(deps, "mcp_sess_empty")

	_, _, err := rc.toolHeartbeat(context.Background(), nil, heartbeatInput{})
	if err == nil {
		t.Fatal("expected error for missing session")
	}
}

func TestToolDeregister(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"revoked":true}`))
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	deps.Sessions.Set("mcp_sess_1", &SessionState{
		AgentID:    "agent_test",
		AgentToken: "tok_old",
		Surface:    "cli",
	})
	rc := newRequestContext(deps, "mcp_sess_1")

	_, out, err := rc.toolDeregister(context.Background(), nil, deregisterInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Deregistered {
		t.Fatal("expected deregistered=true")
	}
	if out.AgentID != "agent_test" {
		t.Fatalf("expected agent_test, got %s", out.AgentID)
	}

	// Verify session was deleted.
	_, ok := deps.Sessions.Get("mcp_sess_1")
	if ok {
		t.Fatal("expected session to be deleted")
	}
}

func TestToolDeregisterNoSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	rc := newRequestContext(deps, "mcp_sess_empty")

	_, _, err := rc.toolDeregister(context.Background(), nil, deregisterInput{})
	if err == nil {
		t.Fatal("expected error for missing session")
	}
}

// Verify requestContext helpers.
func TestRequestContextBearerToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer my_token")
	rc := &requestContext{request: req}

	if rc.bearerToken() != "my_token" {
		t.Fatalf("expected my_token, got %s", rc.bearerToken())
	}
}

func TestRequestContextBearerTokenMissing(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rc := &requestContext{request: req}

	if rc.bearerToken() != "" {
		t.Fatalf("expected empty, got %s", rc.bearerToken())
	}
}

func TestRequestContextMCPSessionID(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_abc")
	rc := &requestContext{request: req}

	if rc.mcpSessionID() != "sess_abc" {
		t.Fatalf("expected sess_abc, got %s", rc.mcpSessionID())
	}
}

// Suppress unused import warning for mcpsdk.
var _ = (*mcpsdk.CallToolRequest)(nil)
