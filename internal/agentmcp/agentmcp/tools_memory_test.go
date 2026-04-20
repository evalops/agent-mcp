package agentmcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	memoryv1 "github.com/evalops/proto/gen/go/memory/v1"
	"github.com/evalops/proto/gen/go/memory/v1/memoryv1connect"
	"github.com/evalops/agent-mcp/internal/agentmcp/clients"
	"github.com/evalops/agent-mcp/internal/agentmcp/config"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRecallNoMemoryConfigured(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ServiceName: "test", Version: "test"},
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	rc := &requestContext{deps: deps, request: nil, logger: testLogger}

	_, out, err := rc.toolRecall(context.Background(), nil, recallInput{Query: "vector search"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Available {
		t.Fatalf("expected unavailable recall output, got %#v", out)
	}
	if out.Message != "memory service not configured" {
		t.Fatalf("unexpected message %q", out.Message)
	}
}

func TestRecallWithMemory(t *testing.T) {
	mockMemory := &mockMemoryService{
		recallResponse: &memoryv1.RecallResponse{
			Results: []*memoryv1.RecallResult{{
				Memory: &memoryv1.Memory{
					Id:        "mem_1",
					Scope:     memoryv1.Scope_SCOPE_AGENT,
					Content:   "Use pgvector for semantic recall",
					Type:      "reference",
					Agent:     "agent_1",
					Tags:      []string{"database", "search"},
					CreatedAt: timestamppb.New(time.Unix(1, 0).UTC()),
					UpdatedAt: timestamppb.New(time.Unix(2, 0).UTC()),
				},
				Similarity: 0.91,
			}},
		},
	}
	_, handler := memoryv1connect.NewMemoryServiceHandler(mockMemory)
	memorySrv := httptest.NewServer(handler)
	defer memorySrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test",
			Version:     "test",
			Memory:      config.MemoryConfig{BaseURL: memorySrv.URL},
		},
		Memory:   clients.NewMemoryClient(memorySrv.URL, memorySrv.Client()),
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	deps.Sessions.Set("sess_1", &SessionState{
		SessionType:    SessionTypeAgent,
		AgentID:        "agent_1",
		AgentToken:     "tok_1",
		OrganizationID: "org_1",
		WorkspaceID:    "ws_1",
		ExpiresAt:      time.Now().Add(time.Hour),
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolRecall(context.Background(), nil, recallInput{Query: "vector search"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Available || out.Count != 1 {
		t.Fatalf("expected available recall result, got %#v", out)
	}
	if got := out.Results[0].ID; got != "mem_1" {
		t.Fatalf("expected mem_1, got %q", got)
	}
	if mockMemory.lastAuthHeader != "Bearer tok_1" {
		t.Fatalf("unexpected auth header %q", mockMemory.lastAuthHeader)
	}
	if mockMemory.lastOrgHeader != "org_1" {
		t.Fatalf("unexpected org header %q", mockMemory.lastOrgHeader)
	}
	if mockMemory.lastRecallReq == nil {
		t.Fatal("expected recall request")
	}
	if mockMemory.lastRecallReq.GetScope() != memoryv1.Scope_SCOPE_AGENT {
		t.Fatalf("expected default agent scope, got %s", mockMemory.lastRecallReq.GetScope())
	}
	if mockMemory.lastRecallReq.GetAgent() != "agent_1" {
		t.Fatalf("expected default agent filter, got %q", mockMemory.lastRecallReq.GetAgent())
	}
}

func TestStoreMemoryWithMemory(t *testing.T) {
	mockMemory := &mockMemoryService{
		storeResponse: &memoryv1.StoreResponse{
			Memory: &memoryv1.Memory{
				Id:        "mem_2",
				Scope:     memoryv1.Scope_SCOPE_AGENT,
				Content:   "Budget breaches page the on-call owner",
				Type:      "reference",
				Source:    "agent-mcp",
				Agent:     "agent_1",
				IsPolicy:  true,
				Tags:      []string{"alerts"},
				CreatedAt: timestamppb.New(time.Unix(3, 0).UTC()),
				UpdatedAt: timestamppb.New(time.Unix(4, 0).UTC()),
			},
		},
	}
	_, handler := memoryv1connect.NewMemoryServiceHandler(mockMemory)
	memorySrv := httptest.NewServer(handler)
	defer memorySrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test",
			Version:     "test",
			Memory:      config.MemoryConfig{BaseURL: memorySrv.URL},
		},
		Memory:   clients.NewMemoryClient(memorySrv.URL, memorySrv.Client()),
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	deps.Sessions.Set("sess_1", &SessionState{
		SessionType:    SessionTypeAgent,
		AgentID:        "agent_1",
		AgentToken:     "tok_1",
		OrganizationID: "org_1",
		WorkspaceID:    "ws_1",
		ExpiresAt:      time.Now().Add(time.Hour),
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolStoreMemory(context.Background(), nil, storeMemoryInput{
		Content:  "Budget breaches page the on-call owner",
		IsPolicy: true,
		Tags:     []string{"alerts"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Available || !out.Stored || out.Memory == nil {
		t.Fatalf("expected stored memory output, got %#v", out)
	}
	if mockMemory.lastStoreReq == nil {
		t.Fatal("expected store request")
	}
	if mockMemory.lastStoreReq.GetType() != defaultMemoryType {
		t.Fatalf("expected default memory type %q, got %q", defaultMemoryType, mockMemory.lastStoreReq.GetType())
	}
	if mockMemory.lastStoreReq.GetAgent() != "agent_1" {
		t.Fatalf("expected default agent id, got %q", mockMemory.lastStoreReq.GetAgent())
	}
	if mockMemory.lastStoreReq.GetSource() != defaultMemorySource {
		t.Fatalf("expected default source %q, got %q", defaultMemorySource, mockMemory.lastStoreReq.GetSource())
	}
	if mockMemory.lastAuthHeader != "Bearer tok_1" {
		t.Fatalf("unexpected auth header %q", mockMemory.lastAuthHeader)
	}
	if mockMemory.lastOrgHeader != "org_1" {
		t.Fatalf("unexpected org header %q", mockMemory.lastOrgHeader)
	}
}

func TestRecallRequiresRegisteredSession(t *testing.T) {
	mockMemory := &mockMemoryService{recallResponse: &memoryv1.RecallResponse{}}
	_, handler := memoryv1connect.NewMemoryServiceHandler(mockMemory)
	memorySrv := httptest.NewServer(handler)
	defer memorySrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test",
			Version:     "test",
			Memory:      config.MemoryConfig{BaseURL: memorySrv.URL},
		},
		Memory:   clients.NewMemoryClient(memorySrv.URL, memorySrv.Client()),
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	result, out, err := rc.toolRecall(context.Background(), nil, recallInput{Query: "vector search"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Available {
		t.Fatalf("expected unavailable output on registration requirement, got %#v", out)
	}
	if !result.IsError {
		t.Fatalf("expected tool error result, got %#v", result)
	}
	payload := callResultMap(t, result)
	if payload["error"] != "registration_required" {
		t.Fatalf("expected registration_required, got %#v", payload["error"])
	}
}

func TestStoreMemoryGracefullyDegradesWhenMemoryFails(t *testing.T) {
	mockMemory := &mockMemoryService{storeErr: errors.New("memory unavailable")}
	_, handler := memoryv1connect.NewMemoryServiceHandler(mockMemory)
	memorySrv := httptest.NewServer(handler)
	defer memorySrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test",
			Version:     "test",
			Memory:      config.MemoryConfig{BaseURL: memorySrv.URL},
		},
		Memory:   clients.NewMemoryClient(memorySrv.URL, memorySrv.Client()),
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	deps.Sessions.Set("sess_1", &SessionState{
		SessionType:    SessionTypeAgent,
		AgentID:        "agent_1",
		AgentToken:     "tok_1",
		OrganizationID: "org_1",
		WorkspaceID:    "ws_1",
		ExpiresAt:      time.Now().Add(time.Hour),
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolStoreMemory(context.Background(), nil, storeMemoryInput{Content: "remember this"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Available || out.Stored {
		t.Fatalf("expected graceful degradation output, got %#v", out)
	}
	if out.Message == "" {
		t.Fatalf("expected degradation message, got %#v", out)
	}
}

type mockMemoryService struct {
	memoryv1connect.UnimplementedMemoryServiceHandler
	recallResponse *memoryv1.RecallResponse
	storeResponse  *memoryv1.StoreResponse
	recallErr      error
	storeErr       error
	lastAuthHeader string
	lastOrgHeader  string
	lastRecallReq  *memoryv1.RecallRequest
	lastStoreReq   *memoryv1.StoreRequest
}

func (m *mockMemoryService) Recall(_ context.Context, req *connect.Request[memoryv1.RecallRequest]) (*connect.Response[memoryv1.RecallResponse], error) {
	m.lastAuthHeader = req.Header().Get("Authorization")
	m.lastOrgHeader = req.Header().Get("X-Organization-ID")
	m.lastRecallReq = req.Msg
	if m.recallErr != nil {
		return nil, m.recallErr
	}
	if m.recallResponse == nil {
		m.recallResponse = &memoryv1.RecallResponse{}
	}
	return connect.NewResponse(m.recallResponse), nil
}

func (m *mockMemoryService) Store(_ context.Context, req *connect.Request[memoryv1.StoreRequest]) (*connect.Response[memoryv1.StoreResponse], error) {
	m.lastAuthHeader = req.Header().Get("Authorization")
	m.lastOrgHeader = req.Header().Get("X-Organization-ID")
	m.lastStoreReq = req.Msg
	if m.storeErr != nil {
		return nil, m.storeErr
	}
	if m.storeResponse == nil {
		m.storeResponse = &memoryv1.StoreResponse{}
	}
	return connect.NewResponse(m.storeResponse), nil
}
