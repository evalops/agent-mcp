package agentmcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/evalops/agent-mcp/internal/clients"
	"github.com/evalops/agent-mcp/internal/config"
	meterv1 "github.com/evalops/proto/gen/go/meter/v1"
	"github.com/evalops/proto/gen/go/meter/v1/meterv1connect"
)

func TestReportUsageNoMeter(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ServiceName: "test", Version: "test"},
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	rc := &requestContext{deps: deps, request: nil, logger: testLogger}

	_, out, err := rc.toolReportUsage(context.Background(), nil, reportUsageInput{
		Model: "claude-sonnet-4-6", InputTokens: 1000, OutputTokens: 500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Recorded {
		t.Fatal("expected recorded=false when meter not configured")
	}
}

func TestReportUsageWithMeter(t *testing.T) {
	mockMeter := &mockMeterService{}
	_, handler := meterv1connect.NewMeterServiceHandler(mockMeter)
	meterSrv := httptest.NewServer(handler)
	defer meterSrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Meter: config.MeterConfig{BaseURL: meterSrv.URL},
		},
		Meter:    clients.NewMeterClient(meterSrv.URL, meterSrv.Client()),
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   &recordingEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}

	// Pre-populate session for agent attribution.
	deps.Sessions.Set("sess_1", &SessionState{
		AgentID: "agent_1", AgentToken: "tok_1", Surface: "cli",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolReportUsage(context.Background(), nil, reportUsageInput{
		Model: "claude-sonnet-4-6", Provider: "anthropic", InputTokens: 1000, OutputTokens: 500, CostUSD: 0.015,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Recorded {
		t.Fatal("expected recorded=true")
	}
	if mockMeter.recordCalled != 1 {
		t.Fatalf("expected 1 meter call, got %d", mockMeter.recordCalled)
	}
	if mockMeter.lastAgentID != "agent_1" {
		t.Fatalf("expected agent_1, got %s", mockMeter.lastAgentID)
	}
	if mockMeter.lastModel != "claude-sonnet-4-6" {
		t.Fatalf("expected claude-sonnet-4-6, got %s", mockMeter.lastModel)
	}
	recorder := deps.Events.(*recordingEventPublisher)
	if len(recorder.events) != 1 {
		t.Fatalf("expected 1 usage event, got %d", len(recorder.events))
	}
	if recorder.events[0].aggregateType != "usage_report" || recorder.events[0].operation != "recorded" {
		t.Fatalf("unexpected usage event %#v", recorder.events[0])
	}
}

type mockMeterService struct {
	meterv1connect.UnimplementedMeterServiceHandler
	recordCalled int
	lastAgentID  string
	lastModel    string
}

func (m *mockMeterService) RecordUsage(_ context.Context, req *connect.Request[meterv1.RecordUsageRequest]) (*connect.Response[meterv1.RecordUsageResponse], error) {
	m.recordCalled++
	m.lastAgentID = req.Msg.GetAgentId()
	m.lastModel = req.Msg.GetModel()
	return connect.NewResponse(&meterv1.RecordUsageResponse{}), nil
}
