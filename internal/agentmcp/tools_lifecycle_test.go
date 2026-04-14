package agentmcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/evalops/agent-mcp/internal/clients"
	"github.com/evalops/agent-mcp/internal/config"
	agentsv1 "github.com/evalops/proto/gen/go/agents/v1"
	"github.com/evalops/proto/gen/go/agents/v1/agentsv1connect"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// Suppress unused import.
var _ = (*mcpsdk.CallToolRequest)(nil)

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
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Logger:   testLogger,
	}
}

func newRequestContext(deps *Deps, sessionID string) *requestContext {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	req.Header.Set("Authorization", "Bearer user_token_123")
	return &requestContext{deps: deps, request: req, logger: testLogger}
}

func fakeIdentityServer(session clients.AgentSession) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(session)
	}))
}

func TestToolRegister(t *testing.T) {
	srv := fakeIdentityServer(clients.AgentSession{
		AgentID:       "agent_test",
		AgentToken:    "tok_new",
		ExpiresAt:     time.Now().Add(time.Hour),
		RunID:         "run_test",
		ScopesGranted: []string{"governance:read"},
	})
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

	state, ok := deps.Sessions.Get("mcp_sess_1")
	if !ok {
		t.Fatal("expected session to be stored")
	}
	if state.AgentToken != "tok_new" {
		t.Fatalf("expected tok_new, got %s", state.AgentToken)
	}
}

func TestToolRegisterWithRegistry(t *testing.T) {
	srv := fakeIdentityServer(clients.AgentSession{
		AgentID:    "agent_test",
		AgentToken: "tok_new",
		ExpiresAt:  time.Now().Add(time.Hour),
		RunID:      "run_test",
	})
	defer srv.Close()

	// Mock registry ConnectRPC server.
	mockRegistry := &mockAgentService{}
	_, handler := agentsv1connect.NewAgentServiceHandler(mockRegistry)
	registrySrv := httptest.NewServer(handler)
	defer registrySrv.Close()

	deps := newTestDeps(srv)
	deps.Registry = clients.NewRegistryClient(registrySrv.URL, registrySrv.Client())
	deps.Config.Registry.BaseURL = registrySrv.URL

	rc := newRequestContext(deps, "mcp_sess_1")
	_, out, err := rc.toolRegister(context.Background(), nil, registerInput{
		AgentType:   "codex",
		Surface:     "ide",
		WorkspaceID: "ws_123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.RegistryVisible {
		t.Fatal("expected registry_visible=true with mock registry")
	}
	if mockRegistry.registerCalled != 1 {
		t.Fatalf("expected 1 registry register call, got %d", mockRegistry.registerCalled)
	}
}

func TestToolRegisterMissingAgentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("identity should not be called")
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	rc := newRequestContext(deps, "mcp_sess_1")

	_, _, err := rc.toolRegister(context.Background(), nil, registerInput{Surface: "cli"})
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
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "mcp_sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, _, err := rc.toolRegister(context.Background(), nil, registerInput{
		AgentType: "claude-code", Surface: "cli",
	})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestToolHeartbeat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(clients.AgentSession{
			AgentID:    "agent_test",
			AgentToken: "tok_rotated",
			ExpiresAt:  time.Now().Add(2 * time.Hour),
			RunID:      "run_test",
		})
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	deps.Sessions.Set("mcp_sess_1", &SessionState{
		AgentID: "agent_test", AgentToken: "tok_old", ExpiresAt: time.Now().Add(time.Hour),
		RunID: "run_test", Surface: "cli",
	})
	rc := newRequestContext(deps, "mcp_sess_1")

	_, out, err := rc.toolHeartbeat(context.Background(), nil, heartbeatInput{TTLSeconds: 3600})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Renewed {
		t.Fatal("expected renewed=true")
	}

	state, _ := deps.Sessions.Get("mcp_sess_1")
	if state.AgentToken != "tok_rotated" {
		t.Fatalf("expected tok_rotated, got %s", state.AgentToken)
	}
}

func TestToolHeartbeatWithRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(clients.AgentSession{
			AgentID: "agent_test", AgentToken: "tok_rotated", ExpiresAt: time.Now().Add(time.Hour),
		})
	}))
	defer srv.Close()

	mockRegistry := &mockAgentService{}
	_, handler := agentsv1connect.NewAgentServiceHandler(mockRegistry)
	registrySrv := httptest.NewServer(handler)
	defer registrySrv.Close()

	deps := newTestDeps(srv)
	deps.Registry = clients.NewRegistryClient(registrySrv.URL, registrySrv.Client())
	deps.Config.Registry.BaseURL = registrySrv.URL
	deps.Sessions.Set("mcp_sess_1", &SessionState{
		AgentID: "agent_test", AgentToken: "tok_old", Surface: "cli", WorkspaceID: "ws_1",
	})

	rc := newRequestContext(deps, "mcp_sess_1")
	_, _, err := rc.toolHeartbeat(context.Background(), nil, heartbeatInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockRegistry.heartbeatCalled != 1 {
		t.Fatalf("expected 1 registry heartbeat call, got %d", mockRegistry.heartbeatCalled)
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
		AgentID: "agent_test", AgentToken: "tok_old", Surface: "cli",
	})
	rc := newRequestContext(deps, "mcp_sess_1")

	_, out, err := rc.toolDeregister(context.Background(), nil, deregisterInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Deregistered {
		t.Fatal("expected deregistered=true")
	}
	if _, ok := deps.Sessions.Get("mcp_sess_1"); ok {
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

// mockAgentService implements the AgentService ConnectRPC interface for testing.
type mockAgentService struct {
	agentsv1connect.UnimplementedAgentServiceHandler
	registerCalled   int
	heartbeatCalled  int
	deregisterCalled int
}

func (m *mockAgentService) Register(_ context.Context, _ *connect.Request[agentsv1.RegisterRequest]) (*connect.Response[agentsv1.RegisterResponse], error) {
	m.registerCalled++
	return connect.NewResponse(&agentsv1.RegisterResponse{}), nil
}

func (m *mockAgentService) Heartbeat(_ context.Context, _ *connect.Request[agentsv1.HeartbeatRequest]) (*connect.Response[agentsv1.HeartbeatResponse], error) {
	m.heartbeatCalled++
	return connect.NewResponse(&agentsv1.HeartbeatResponse{}), nil
}

func (m *mockAgentService) Deregister(_ context.Context, _ *connect.Request[agentsv1.DeregisterRequest]) (*connect.Response[agentsv1.DeregisterResponse], error) {
	m.deregisterCalled++
	return connect.NewResponse(&agentsv1.DeregisterResponse{}), nil
}
