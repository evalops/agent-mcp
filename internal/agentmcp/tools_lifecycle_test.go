package agentmcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
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
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
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
		if err := json.NewEncoder(w).Encode(session); err != nil {
			panic(err)
		}
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
	recorder := &recordingEventPublisher{}
	deps.Events = recorder
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
	if len(recorder.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(recorder.events))
	}
	if recorder.events[0].operation != "registered" {
		t.Fatalf("expected registered event, got %q", recorder.events[0].operation)
	}
	if recorder.events[0].attrs["agent_id"] != "agent_test" {
		t.Fatalf("expected agent_id in register event, got %#v", recorder.events[0].attrs["agent_id"])
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

func TestToolRegisterFallsBackToFederationOnUnauthorizedBearer(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/v1/agents/register":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
		case "/v1/agents/federate":
			var req clients.FederateAgentRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode federate request: %v", err)
			}
			if req.Provider != "openai" {
				t.Fatalf("expected provider openai, got %q", req.Provider)
			}
			if req.ExternalToken != "user_token_123" {
				t.Fatalf("expected fallback to bearer token, got %q", req.ExternalToken)
			}
			if req.OrganizationID != "ws_123" {
				t.Fatalf("expected workspace ws_123, got %q", req.OrganizationID)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(clients.AgentSession{
				AgentID:       "agent_federated",
				AgentToken:    "tok_federated",
				ExpiresAt:     time.Now().Add(time.Hour),
				RunID:         "run_federated",
				ScopesGranted: []string{"governance:read"},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	rc := newRequestContext(deps, "mcp_sess_1")

	_, out, err := rc.toolRegister(context.Background(), nil, registerInput{
		AgentType:   "codex",
		Surface:     "cli",
		WorkspaceID: "ws_123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.AgentID != "agent_federated" {
		t.Fatalf("expected federated agent id, got %q", out.AgentID)
	}
	if len(calls) != 2 || calls[0] != "/v1/agents/register" || calls[1] != "/v1/agents/federate" {
		t.Fatalf("expected register then federate, got %#v", calls)
	}
}

func TestToolRegisterUsesConfiguredFederationCredentialWithoutBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/federate" {
			t.Fatalf("expected only federate path, got %s", r.URL.Path)
		}
		var req clients.FederateAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode federate request: %v", err)
		}
		if req.Provider != "openai" {
			t.Fatalf("expected provider openai, got %q", req.Provider)
		}
		if req.ExternalToken != "openai-local-token" {
			t.Fatalf("expected configured OpenAI token, got %q", req.ExternalToken)
		}
		if req.OrganizationID != "ws_default" {
			t.Fatalf("expected default workspace ws_default, got %q", req.OrganizationID)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(clients.AgentSession{
			AgentID:    "agent_local",
			AgentToken: "tok_local",
			ExpiresAt:  time.Now().Add(time.Hour),
			RunID:      "run_local",
		})
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	deps.Config.Federation.DefaultWorkspaceID = "ws_default"
	deps.Config.Federation.OpenAIAPIKey = "openai-local-token"

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "mcp_sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolRegister(context.Background(), nil, registerInput{
		AgentType: "codex",
		Surface:   "cli",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.AgentID != "agent_local" {
		t.Fatalf("expected federated local agent id, got %q", out.AgentID)
	}
}

func TestToolRegisterConfiguredFederationRequiresDefaultWorkspaceOptIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("identity should not be called")
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	deps.Config.Federation.OpenAIAPIKey = "openai-local-token"

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "mcp_sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, _, err := rc.toolRegister(context.Background(), nil, registerInput{
		AgentType: "codex",
		Surface:   "cli",
	})
	if err == nil {
		t.Fatal("expected error when configured federation is not explicitly enabled")
	}
	if !strings.Contains(err.Error(), "missing user token") {
		t.Fatalf("expected missing token error, got %v", err)
	}
}

func TestToolHeartbeat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(clients.AgentSession{
			AgentID:    "agent_test",
			AgentToken: "tok_rotated",
			ExpiresAt:  time.Now().Add(2 * time.Hour),
			RunID:      "run_test",
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	recorder := &recordingEventPublisher{}
	deps.Events = recorder
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
	if len(recorder.events) != 1 {
		t.Fatalf("expected 1 heartbeat event, got %d", len(recorder.events))
	}
	if recorder.events[0].operation != "heartbeat" {
		t.Fatalf("expected heartbeat event, got %q", recorder.events[0].operation)
	}
	if recorder.events[0].attrs["agent_id"] != "agent_test" {
		t.Fatalf("expected heartbeat agent_id, got %#v", recorder.events[0].attrs["agent_id"])
	}
}

func TestToolHeartbeatWithRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(clients.AgentSession{
			AgentID: "agent_test", AgentToken: "tok_rotated", ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
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
	// Registry heartbeat is now fire-and-forget; poll until the background goroutine completes.
	deadline := time.After(2 * time.Second)
	for mockRegistry.heartbeatCalled.Load() < 1 {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for background registry heartbeat; called=%d", mockRegistry.heartbeatCalled.Load())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestToolHeartbeatBackgroundRegistryCallUsesConfiguredTimeout(t *testing.T) {
	type timeoutResult struct {
		err         error
		elapsed     time.Duration
		hasDeadline bool
	}

	done := make(chan timeoutResult, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(clients.AgentSession{
			AgentID: "agent_test", AgentToken: "tok_rotated", ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("encode heartbeat response: %v", err)
		}
	}))
	defer srv.Close()

	mockRegistry := &mockAgentService{
		heartbeatFunc: func(ctx context.Context, _ *connect.Request[agentsv1.HeartbeatRequest]) (*connect.Response[agentsv1.HeartbeatResponse], error) {
			_, hasDeadline := ctx.Deadline()
			start := time.Now()
			select {
			case <-ctx.Done():
				done <- timeoutResult{err: ctx.Err(), elapsed: time.Since(start), hasDeadline: hasDeadline}
				return nil, ctx.Err()
			case <-time.After(200 * time.Millisecond):
				done <- timeoutResult{err: errors.New("background registry heartbeat did not time out"), elapsed: time.Since(start), hasDeadline: hasDeadline}
				return connect.NewResponse(&agentsv1.HeartbeatResponse{}), nil
			}
		},
	}
	_, handler := agentsv1connect.NewAgentServiceHandler(mockRegistry)
	registrySrv := httptest.NewServer(handler)
	defer registrySrv.Close()

	deps := newTestDeps(srv)
	deps.Registry = clients.NewRegistryClient(registrySrv.URL, registrySrv.Client())
	deps.Config.Registry = config.RegistryConfig{BaseURL: registrySrv.URL, RequestTimeout: 20 * time.Millisecond}
	deps.Sessions.Set("mcp_sess_1", &SessionState{
		AgentID: "agent_test", AgentToken: "tok_old", Surface: "cli", WorkspaceID: "ws_1",
	})

	rc := newRequestContext(deps, "mcp_sess_1")
	if _, _, err := rc.toolHeartbeat(context.Background(), nil, heartbeatInput{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case result := <-done:
		if result.err == nil {
			t.Fatal("expected background registry heartbeat to be canceled by timeout")
		}
		if result.elapsed >= 150*time.Millisecond {
			t.Fatalf("expected timeout-bound background registry heartbeat, got elapsed=%v err=%v", result.elapsed, result.err)
		}
		if !result.hasDeadline {
			t.Fatal("expected background registry heartbeat context to carry a deadline")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for background registry heartbeat timeout")
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
		if _, err := w.Write([]byte(`{"revoked":true}`)); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	defer srv.Close()

	deps := newTestDeps(srv)
	recorder := &recordingEventPublisher{}
	deps.Events = recorder
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
	if len(recorder.events) != 1 {
		t.Fatalf("expected 1 deregister event, got %d", len(recorder.events))
	}
	if recorder.events[0].operation != "deregistered" {
		t.Fatalf("expected deregistered event, got %q", recorder.events[0].operation)
	}
	if recorder.events[0].attrs["agent_id"] != "agent_test" {
		t.Fatalf("expected deregistered agent_id, got %#v", recorder.events[0].attrs["agent_id"])
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
	heartbeatCalled  atomic.Int32 // atomic: called from background goroutine
	deregisterCalled int
	heartbeatFunc    func(context.Context, *connect.Request[agentsv1.HeartbeatRequest]) (*connect.Response[agentsv1.HeartbeatResponse], error)
}

func (m *mockAgentService) Register(_ context.Context, _ *connect.Request[agentsv1.RegisterRequest]) (*connect.Response[agentsv1.RegisterResponse], error) {
	m.registerCalled++
	return connect.NewResponse(&agentsv1.RegisterResponse{}), nil
}

func (m *mockAgentService) Heartbeat(ctx context.Context, req *connect.Request[agentsv1.HeartbeatRequest]) (*connect.Response[agentsv1.HeartbeatResponse], error) {
	m.heartbeatCalled.Add(1)
	if m.heartbeatFunc != nil {
		return m.heartbeatFunc(ctx, req)
	}
	return connect.NewResponse(&agentsv1.HeartbeatResponse{}), nil
}

func (m *mockAgentService) Deregister(_ context.Context, _ *connect.Request[agentsv1.DeregisterRequest]) (*connect.Response[agentsv1.DeregisterResponse], error) {
	m.deregisterCalled++
	return connect.NewResponse(&agentsv1.DeregisterResponse{}), nil
}
