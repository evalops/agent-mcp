package agentmcp

import (
	"context"
	"testing"

	"github.com/evalops/agent-mcp/internal/config"
	governancev1 "github.com/evalops/proto/gen/go/governance/v1"
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
		got := decisionString(tt.input)
		if got != tt.want {
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
		got := riskLevelString(tt.input)
		if got != tt.want {
			t.Errorf("riskLevelString(%v) = %s, want %s", tt.input, got, tt.want)
		}
	}
}

func TestCheckActionNoGovernance(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ServiceName: "test", Version: "test"},
		Sessions: NewSessionStore(),
	}
	rc := &requestContext{deps: deps, request: nil}

	_, out, err := rc.toolCheckAction(context.Background(), nil, checkActionInput{
		ActionType: "Bash",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Decision != "allow" {
		t.Fatalf("expected allow when governance not configured, got %s", out.Decision)
	}
}

func TestCheckApprovalNoApprovals(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ServiceName: "test", Version: "test"},
		Sessions: NewSessionStore(),
	}
	rc := &requestContext{deps: deps, request: nil}

	_, _, err := rc.toolCheckApproval(context.Background(), nil, checkApprovalInput{
		ApprovalID: "apr_123",
	})
	if err == nil {
		t.Fatal("expected error when approvals service not configured")
	}
}
