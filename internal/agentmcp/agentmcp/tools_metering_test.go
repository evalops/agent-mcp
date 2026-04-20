package agentmcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	meterv1 "github.com/evalops/proto/gen/go/meter/v1"
	"github.com/evalops/proto/gen/go/meter/v1/meterv1connect"
	"github.com/evalops/agent-mcp/internal/agentmcp/clients"
	"github.com/evalops/agent-mcp/internal/agentmcp/config"
	dto "github.com/prometheus/client_model/go"
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

	recorder := &meteringEventPublisher{}
	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Meter: config.MeterConfig{BaseURL: meterSrv.URL},
		},
		Meter:    clients.NewMeterClient(meterSrv.URL, meterSrv.Client()),
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   recorder,
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
	waitForMeteringCondition(t, "background usage report", func() bool {
		return mockMeter.recordCalled.Load() >= 1
	})
	if mockMeter.lastAgentID.Load() != "agent_1" {
		t.Fatalf("expected agent_1, got %s", mockMeter.lastAgentID.Load())
	}
	if mockMeter.lastModel.Load() != "claude-sonnet-4-6" {
		t.Fatalf("expected claude-sonnet-4-6, got %s", mockMeter.lastModel.Load())
	}
	waitForMeteringCondition(t, "usage event publication", func() bool {
		return len(recorder.snapshot()) == 1
	})
	if got := usageReportCount(t, deps.Metrics); got != 1 {
		t.Fatalf("expected usage report metric 1, got %v", got)
	}
	events := recorder.snapshot()
	if events[0].aggregateType != "usage_report" || events[0].operation != "recorded" {
		t.Fatalf("unexpected usage event %#v", events[0])
	}
}

func TestReportUsageRequiresActiveSessionWhenMeterConfigured(t *testing.T) {
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
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "missing")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	result, out, err := rc.toolReportUsage(context.Background(), nil, reportUsageInput{
		Model: "claude-sonnet-4-6", Provider: "anthropic", InputTokens: 1000, OutputTokens: 500, CostUSD: 0.015,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected authentication error result, got %#v", result)
	}
	if out.Recorded {
		t.Fatalf("expected recorded=false without active session, got %#v", out)
	}
	if mockMeter.recordCalled.Load() != 0 {
		t.Fatalf("expected no meter calls without active session, got %d", mockMeter.recordCalled.Load())
	}
}

func TestReportUsageRejectsWhenBackgroundCapacityExhausted(t *testing.T) {
	mockMeter := &mockMeterService{}
	_, handler := meterv1connect.NewMeterServiceHandler(mockMeter)
	meterSrv := httptest.NewServer(handler)
	defer meterSrv.Close()

	runner := NewAsyncRunner(1, testLogger)
	release := make(chan struct{})
	started := make(chan struct{})
	if !runner.TryGo(context.Background(), "block", func(context.Context) error {
		close(started)
		<-release
		return nil
	}) {
		t.Fatal("expected blocking background task to start")
	}
	defer close(release)
	<-started

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Meter: config.MeterConfig{BaseURL: meterSrv.URL},
		},
		Meter:    clients.NewMeterClient(meterSrv.URL, meterSrv.Client()),
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
		Async:    runner,
	}
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
	if out.Recorded || out.Async {
		t.Fatalf("expected usage report to be rejected before enqueue, got %#v", out)
	}
	if mockMeter.recordCalled.Load() != 0 {
		t.Fatalf("expected no meter calls when background capacity is exhausted, got %d", mockMeter.recordCalled.Load())
	}
}

type mockMeterService struct {
	meterv1connect.UnimplementedMeterServiceHandler
	recordCalled    atomic.Int32 // atomic: called from background goroutine
	lastAgentID     atomic.Value // atomic: written from background goroutine
	lastModel       atomic.Value // atomic: written from background goroutine
	err             error
	recordUsageFunc func(context.Context, *connect.Request[meterv1.RecordUsageRequest]) (*connect.Response[meterv1.RecordUsageResponse], error)
}

func (m *mockMeterService) RecordUsage(ctx context.Context, req *connect.Request[meterv1.RecordUsageRequest]) (*connect.Response[meterv1.RecordUsageResponse], error) {
	m.lastAgentID.Store(req.Msg.GetAgentId())
	m.lastModel.Store(req.Msg.GetModel())
	m.recordCalled.Add(1)
	if m.recordUsageFunc != nil {
		return m.recordUsageFunc(ctx, req)
	}
	if m.err != nil {
		return nil, m.err
	}
	return connect.NewResponse(&meterv1.RecordUsageResponse{}), nil
}

func TestReportUsageWithMeterFailureDoesNotPublishRecordedSignals(t *testing.T) {
	mockMeter := &mockMeterService{err: errors.New("meter unavailable")}
	_, handler := meterv1connect.NewMeterServiceHandler(mockMeter)
	meterSrv := httptest.NewServer(handler)
	defer meterSrv.Close()

	recorder := &meteringEventPublisher{}
	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Meter: config.MeterConfig{BaseURL: meterSrv.URL},
		},
		Meter:    clients.NewMeterClient(meterSrv.URL, meterSrv.Client()),
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   recorder,
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}

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

	waitForMeteringCondition(t, "background usage report failure", func() bool {
		return mockMeter.recordCalled.Load() >= 1
	})
	if got := usageReportCount(t, deps.Metrics); got != 0 {
		t.Fatalf("expected usage report metric 0 after failed record, got %v", got)
	}
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("expected no recorded usage events after failed record, got %#v", events)
	}
}

func TestReportUsageBackgroundCallUsesConfiguredTimeout(t *testing.T) {
	type timeoutResult struct {
		err         error
		elapsed     time.Duration
		hasDeadline bool
	}

	done := make(chan timeoutResult, 1)
	mockMeter := &mockMeterService{
		recordUsageFunc: func(ctx context.Context, _ *connect.Request[meterv1.RecordUsageRequest]) (*connect.Response[meterv1.RecordUsageResponse], error) {
			_, hasDeadline := ctx.Deadline()
			start := time.Now()
			select {
			case <-ctx.Done():
				done <- timeoutResult{err: ctx.Err(), elapsed: time.Since(start), hasDeadline: hasDeadline}
				return nil, ctx.Err()
			case <-time.After(200 * time.Millisecond):
				done <- timeoutResult{err: errors.New("background metering call did not time out"), elapsed: time.Since(start), hasDeadline: hasDeadline}
				return connect.NewResponse(&meterv1.RecordUsageResponse{}), nil
			}
		},
	}
	_, handler := meterv1connect.NewMeterServiceHandler(mockMeter)
	meterSrv := httptest.NewServer(handler)
	defer meterSrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Meter: config.MeterConfig{BaseURL: meterSrv.URL, RequestTimeout: 20 * time.Millisecond},
		},
		Meter:    clients.NewMeterClient(meterSrv.URL, meterSrv.Client()),
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	deps.Sessions.Set("sess_1", &SessionState{
		AgentID: "agent_1", AgentToken: "tok_1", Surface: "cli",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	if _, out, err := rc.toolReportUsage(context.Background(), nil, reportUsageInput{
		Model: "claude-sonnet-4-6", Provider: "anthropic", InputTokens: 1000, OutputTokens: 500, CostUSD: 0.015,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if !out.Recorded {
		t.Fatal("expected recorded=true")
	}

	select {
	case result := <-done:
		if result.err == nil {
			t.Fatal("expected background metering call to be canceled by timeout")
		}
		if result.elapsed >= 150*time.Millisecond {
			t.Fatalf("expected timeout-bound background metering call, got elapsed=%v err=%v", result.elapsed, result.err)
		}
		if !result.hasDeadline {
			t.Fatal("expected background metering context to carry a deadline")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for background metering timeout")
	}
}

type meteringEventPublisher struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (p *meteringEventPublisher) Publish(_ context.Context, tenantID, aggregateType, aggregateID, operation string, attrs map[string]any) {
	cloned := make(map[string]any, len(attrs))
	for key, value := range attrs {
		cloned[key] = value
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, recordedEvent{
		tenantID:      tenantID,
		aggregateType: aggregateType,
		aggregateID:   aggregateID,
		operation:     operation,
		attrs:         cloned,
	})
}

func (*meteringEventPublisher) Close() {}

func (p *meteringEventPublisher) snapshot() []recordedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedEvent(nil), p.events...)
}

func waitForMeteringCondition(t *testing.T, description string, condition func() bool) {
	t.Helper()

	deadline := time.After(2 * time.Second)
	for !condition() {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", description)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func usageReportCount(t *testing.T, metrics *Metrics) float64 {
	t.Helper()

	metric := &dto.Metric{}
	if err := metrics.UsageReports.Write(metric); err != nil {
		t.Fatalf("read usage report metric: %v", err)
	}
	return metric.GetCounter().GetValue()
}
