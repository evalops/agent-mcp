package agentmcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/evalops/agent-mcp/internal/clients"
	"github.com/evalops/agent-mcp/internal/config"
	approvalsv1 "github.com/evalops/proto/gen/go/approvals/v1"
	"github.com/evalops/proto/gen/go/approvals/v1/approvalsv1connect"
	governancev1 "github.com/evalops/proto/gen/go/governance/v1"
	"github.com/evalops/proto/gen/go/governance/v1/governancev1connect"
)

func TestDecisionString(t *testing.T) {
	tests := []struct {
		input governancev1.ActionDecision
		want  string
	}{
		{governancev1.ActionDecision_ACTION_DECISION_ALLOW, "allow"},
		{governancev1.ActionDecision_ACTION_DECISION_DENY, "deny"},
		{governancev1.ActionDecision_ACTION_DECISION_REQUIRE_APPROVAL, "require_approval"},
		{governancev1.ActionDecision_ACTION_DECISION_UNSPECIFIED, "allow"},
	}
	for _, tt := range tests {
		if got := decisionString(tt.input); got != tt.want {
			t.Errorf("decisionString(%v) = %s, want %s", tt.input, got, tt.want)
		}
	}
}

func TestRiskLevelString(t *testing.T) {
	tests := []struct {
		input governancev1.RiskLevel
		want  string
	}{
		{governancev1.RiskLevel_RISK_LEVEL_LOW, "low"},
		{governancev1.RiskLevel_RISK_LEVEL_MEDIUM, "medium"},
		{governancev1.RiskLevel_RISK_LEVEL_HIGH, "high"},
		{governancev1.RiskLevel_RISK_LEVEL_CRITICAL, "critical"},
		{governancev1.RiskLevel_RISK_LEVEL_UNSPECIFIED, "low"},
	}
	for _, tt := range tests {
		if got := riskLevelString(tt.input); got != tt.want {
			t.Errorf("riskLevelString(%v) = %s, want %s", tt.input, got, tt.want)
		}
	}
}

func TestCheckActionNoGovernance(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ServiceName: "test", Version: "test"},
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	rc := &requestContext{deps: deps, request: nil, logger: testLogger}

	_, out, err := rc.toolCheckAction(context.Background(), nil, checkActionInput{ActionType: "Bash"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Decision != "allow" {
		t.Fatalf("expected allow when governance not configured, got %s", out.Decision)
	}
}

func TestCheckActionWithGovernance(t *testing.T) {
	mockGov := &mockGovernanceService{
		decision:  governancev1.ActionDecision_ACTION_DECISION_DENY,
		riskLevel: governancev1.RiskLevel_RISK_LEVEL_CRITICAL,
		reasons:   []string{"destructive command"},
	}
	_, handler := governancev1connect.NewGovernanceServiceHandler(mockGov)
	govSrv := httptest.NewServer(handler)
	defer govSrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Governance: config.GovernanceConfig{BaseURL: govSrv.URL},
		},
		Governance: clients.NewGovernanceClient(govSrv.URL, govSrv.Client()),
		Sessions:   NewSessionStore(),
		Metrics:    NewTestMetrics(),
		Events:     &recordingEventPublisher{},
		Logger:     testLogger,
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolCheckAction(context.Background(), nil, checkActionInput{
		ActionType:    "Bash",
		ActionPayload: "rm -rf /",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Decision != "deny" {
		t.Fatalf("expected deny, got %s", out.Decision)
	}
	if out.RiskLevel != "critical" {
		t.Fatalf("expected critical, got %s", out.RiskLevel)
	}
	if len(out.Reasons) != 1 || out.Reasons[0] != "destructive command" {
		t.Fatalf("unexpected reasons: %v", out.Reasons)
	}
	recorder := deps.Events.(*recordingEventPublisher)
	if len(recorder.events) != 1 {
		t.Fatalf("expected 1 governance event, got %d", len(recorder.events))
	}
	if recorder.events[0].aggregateType != "governance_check" || recorder.events[0].operation != "evaluated" {
		t.Fatalf("unexpected governance event %#v", recorder.events[0])
	}
}

func TestCheckActionRequireApproval(t *testing.T) {
	mockGov := &mockGovernanceService{
		decision:  governancev1.ActionDecision_ACTION_DECISION_REQUIRE_APPROVAL,
		riskLevel: governancev1.RiskLevel_RISK_LEVEL_HIGH,
	}
	_, govHandler := governancev1connect.NewGovernanceServiceHandler(mockGov)
	govSrv := httptest.NewServer(govHandler)
	defer govSrv.Close()

	mockApprovals := &mockApprovalService{approvalID: "apr_test_123"}
	_, appHandler := approvalsv1connect.NewApprovalServiceHandler(mockApprovals)
	appSrv := httptest.NewServer(appHandler)
	defer appSrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Governance: config.GovernanceConfig{BaseURL: govSrv.URL},
			Approvals:  config.ApprovalsConfig{BaseURL: appSrv.URL},
		},
		Governance: clients.NewGovernanceClient(govSrv.URL, govSrv.Client()),
		Approvals:  clients.NewApprovalsClient(appSrv.URL, appSrv.Client()),
		Sessions:   NewSessionStore(),
		Metrics:    NewTestMetrics(),
		Events:     &recordingEventPublisher{},
		Logger:     testLogger,
	}

	// Pre-populate session so approval request has context.
	deps.Sessions.Set("sess_1", &SessionState{
		AgentID: "agent_1", AgentToken: "tok_1", WorkspaceID: "ws_1", Surface: "cli",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolCheckAction(context.Background(), nil, checkActionInput{
		ActionType: "Bash", ActionPayload: "delete database",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Decision != "require_approval" {
		t.Fatalf("expected require_approval, got %s", out.Decision)
	}
	if out.ApprovalID != "apr_test_123" {
		t.Fatalf("expected apr_test_123, got %s", out.ApprovalID)
	}
	recorder := deps.Events.(*recordingEventPublisher)
	if len(recorder.events) != 2 {
		t.Fatalf("expected 2 governance-related events, got %d", len(recorder.events))
	}
	if recorder.events[0].aggregateType != "governance_check" || recorder.events[0].operation != "evaluated" {
		t.Fatalf("unexpected first event %#v", recorder.events[0])
	}
	if recorder.events[1].aggregateType != "approval_request" || recorder.events[1].operation != "created" {
		t.Fatalf("unexpected second event %#v", recorder.events[1])
	}
}

func TestCheckApprovalNoApprovals(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ServiceName: "test", Version: "test"},
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	rc := &requestContext{deps: deps, request: nil, logger: testLogger}

	_, _, err := rc.toolCheckApproval(context.Background(), nil, checkApprovalInput{ApprovalID: "apr_123"})
	if err == nil {
		t.Fatal("expected error when approvals not configured")
	}
}

func TestCheckApprovalPending(t *testing.T) {
	mockApprovals := &mockApprovalService{
		approvalStates: map[string]string{"apr_123": "pending", "apr_456": "pending"},
	}
	_, handler := approvalsv1connect.NewApprovalServiceHandler(mockApprovals)
	appSrv := httptest.NewServer(handler)
	defer appSrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Approvals: config.ApprovalsConfig{BaseURL: appSrv.URL},
		},
		Approvals: clients.NewApprovalsClient(appSrv.URL, appSrv.Client()),
		Sessions:  NewSessionStore(),
		Metrics:   NewTestMetrics(),
		Events:    NoopEventPublisher{},
		Logger:    testLogger,
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolCheckApproval(context.Background(), nil, checkApprovalInput{ApprovalID: "apr_123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.State != "pending" {
		t.Fatalf("expected pending, got %s", out.State)
	}
}

func TestCheckApprovalResolved(t *testing.T) {
	mockApprovals := &mockApprovalService{approvalStates: map[string]string{"apr_other": "pending"}}
	_, handler := approvalsv1connect.NewApprovalServiceHandler(mockApprovals)
	appSrv := httptest.NewServer(handler)
	defer appSrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Approvals: config.ApprovalsConfig{BaseURL: appSrv.URL},
		},
		Approvals: clients.NewApprovalsClient(appSrv.URL, appSrv.Client()),
		Sessions:  NewSessionStore(),
		Metrics:   NewTestMetrics(),
		Events:    NoopEventPublisher{},
		Logger:    testLogger,
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolCheckApproval(context.Background(), nil, checkApprovalInput{ApprovalID: "apr_123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.State != "resolved" {
		t.Fatalf("expected resolved, got %s", out.State)
	}
}

func TestCheckApprovalUsesCircuitBreaker(t *testing.T) {
	mockApprovals := &mockApprovalService{
		getApprovalErr: connect.NewError(connect.CodeUnavailable, errors.New("approvals unavailable")),
	}
	_, handler := approvalsv1connect.NewApprovalServiceHandler(mockApprovals)
	appSrv := httptest.NewServer(handler)
	defer appSrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Approvals: config.ApprovalsConfig{BaseURL: appSrv.URL},
			Breaker:   config.BreakerConfig{FailureThreshold: 1, ResetTimeout: time.Hour},
		},
		Approvals: clients.NewApprovalsClient(appSrv.URL, appSrv.Client()),
		Sessions:  NewSessionStore(),
		Metrics:   NewTestMetrics(),
		Events:    NoopEventPublisher{},
		Breakers:  NewBreakers(config.BreakerConfig{FailureThreshold: 1, ResetTimeout: time.Hour}),
		Logger:    testLogger,
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, _, err := rc.toolCheckApproval(context.Background(), nil, checkApprovalInput{ApprovalID: "apr_123"})
	if err == nil || !strings.Contains(err.Error(), "get approval failed") {
		t.Fatalf("expected downstream failure, got %v", err)
	}
	if got := deps.Breakers.Approvals.State(); got != BreakerOpen {
		t.Fatalf("expected approvals breaker open, got %s", got)
	}

	_, _, err = rc.toolCheckApproval(context.Background(), nil, checkApprovalInput{ApprovalID: "apr_123"})
	if err == nil || !strings.Contains(err.Error(), "circuit breaker open") {
		t.Fatalf("expected breaker-open error, got %v", err)
	}
	if mockApprovals.getApprovalCalls != 1 {
		t.Fatalf("expected one GetApproval call before breaker opened, got %d", mockApprovals.getApprovalCalls)
	}
}

// Mock ConnectRPC services.

type mockGovernanceService struct {
	governancev1connect.UnimplementedGovernanceServiceHandler
	decision  governancev1.ActionDecision
	riskLevel governancev1.RiskLevel
	reasons   []string
}

func (m *mockGovernanceService) EvaluateAction(_ context.Context, _ *connect.Request[governancev1.EvaluateActionRequest]) (*connect.Response[governancev1.EvaluateActionResponse], error) {
	return connect.NewResponse(&governancev1.EvaluateActionResponse{
		Evaluation: &governancev1.ActionEvaluation{
			Decision:  m.decision,
			RiskLevel: m.riskLevel,
			Reasons:   m.reasons,
		},
	}), nil
}

type mockApprovalService struct {
	approvalsv1connect.UnimplementedApprovalServiceHandler
	approvalID       string
	pendingIDs       []string
	approvalStates   map[string]string
	getApprovalErr   error
	getApprovalCalls int
}

func (m *mockApprovalService) RequestApproval(_ context.Context, _ *connect.Request[approvalsv1.RequestApprovalRequest]) (*connect.Response[approvalsv1.RequestApprovalResponse], error) {
	return connect.NewResponse(&approvalsv1.RequestApprovalResponse{
		ApprovalRequest: &approvalsv1.ApprovalRequest{Id: m.approvalID},
	}), nil
}

func (m *mockApprovalService) GetApproval(_ context.Context, req *connect.Request[approvalsv1.GetApprovalRequest]) (*connect.Response[approvalsv1.GetApprovalResponse], error) {
	m.getApprovalCalls++
	if m.getApprovalErr != nil {
		return nil, m.getApprovalErr
	}
	id := req.Msg.GetApprovalRequestId()
	state := "resolved"
	if m.approvalStates != nil {
		if s, ok := m.approvalStates[id]; ok {
			state = s
		}
	}
	return connect.NewResponse(&approvalsv1.GetApprovalResponse{
		ApprovalRequest: &approvalsv1.ApprovalRequest{Id: id},
		State:           state,
	}), nil
}

func (m *mockApprovalService) ListPending(_ context.Context, _ *connect.Request[approvalsv1.ListPendingRequest]) (*connect.Response[approvalsv1.ListPendingResponse], error) {
	reqs := make([]*approvalsv1.ApprovalRequest, 0, len(m.pendingIDs))
	for _, id := range m.pendingIDs {
		reqs = append(reqs, &approvalsv1.ApprovalRequest{Id: id})
	}
	return connect.NewResponse(&approvalsv1.ListPendingResponse{Requests: reqs}), nil
}
