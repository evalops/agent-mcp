package agentmcp

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	approvalsv1 "github.com/evalops/proto/gen/go/approvals/v1"
	governancev1 "github.com/evalops/proto/gen/go/governance/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type checkActionInput struct {
	ActionType    string `json:"action_type" jsonschema:"required,The action type to evaluate such as Bash or Edit"`
	ActionPayload string `json:"action_payload,omitempty" jsonschema:"The action payload or command to evaluate"`
}

type checkActionOutput struct {
	Decision     string   `json:"decision"`
	RiskLevel    string   `json:"risk_level"`
	Reasons      []string `json:"reasons,omitempty"`
	ApprovalID   string   `json:"approval_id,omitempty"`
	MatchedRules []string `json:"matched_rules,omitempty"`
}

func (rc *requestContext) toolCheckAction(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	input checkActionInput,
) (*mcpsdk.CallToolResult, checkActionOutput, error) {
	if rc.deps.Governance == nil || rc.deps.Config.Governance.BaseURL == "" {
		return nil, checkActionOutput{Decision: "allow", RiskLevel: "low", Reasons: []string{"governance service not configured"}}, nil
	}

	// Circuit breaker: fail closed (deny) when governance is unreachable.
	if rc.deps.Breakers != nil && !rc.deps.Breakers.Governance.Allow() {
		rc.deps.Metrics.GovernanceChecks.WithLabelValues("deny", "unknown").Inc()
		rc.logger.Warn("governance circuit breaker open — failing closed",
			"action_type", input.ActionType,
		)
		return nil, checkActionOutput{
			Decision:  "deny",
			RiskLevel: "critical",
			Reasons:   []string{"governance service unreachable (circuit breaker open) — failing closed for safety"},
		}, nil
	}

	sid := rc.mcpSessionID()
	state, _ := rc.deps.Sessions.Get(sid)

	workspaceID := ""
	agentID := ""
	agentToken := ""
	if state != nil {
		workspaceID = state.WorkspaceID
		agentID = state.AgentID
		agentToken = state.AgentToken
	}

	req := connect.NewRequest(&governancev1.EvaluateActionRequest{
		WorkspaceId:   workspaceID,
		AgentId:       agentID,
		ActionType:    input.ActionType,
		ActionPayload: []byte(input.ActionPayload),
	})
	if agentToken != "" {
		req.Header().Set("Authorization", "Bearer "+agentToken)
	}

	start := time.Now()
	resp, err := rc.deps.Governance.EvaluateAction(ctx, req)
	rc.deps.Metrics.DownstreamLatency.WithLabelValues("governance", "evaluate_action").Observe(time.Since(start).Seconds())
	if err != nil {
		rc.deps.Metrics.DownstreamErrors.WithLabelValues("governance").Inc()
		if rc.deps.Breakers != nil {
			rc.deps.Breakers.Governance.RecordFailure()
		}
		rc.logger.Error("governance evaluation failed — failing closed", "action_type", input.ActionType, "error", err)
		// Fail closed: return deny when governance is unreachable.
		return nil, checkActionOutput{
			Decision:  "deny",
			RiskLevel: "critical",
			Reasons:   []string{fmt.Sprintf("governance evaluation failed: %v — failing closed for safety", err)},
		}, nil
	}

	if rc.deps.Breakers != nil {
		rc.deps.Breakers.Governance.RecordSuccess()
	}

	eval := resp.Msg.GetEvaluation()
	decision := decisionString(eval.GetDecision())
	riskLevel := riskLevelString(eval.GetRiskLevel())

	out := checkActionOutput{
		Decision:     decision,
		RiskLevel:    riskLevel,
		Reasons:      eval.GetReasons(),
		MatchedRules: eval.GetMatchedRules(),
	}

	rc.deps.Metrics.GovernanceChecks.WithLabelValues(decision, riskLevel).Inc()
	rc.logger.Info("governance check completed",
		"action_type", input.ActionType,
		"decision", decision,
		"risk_level", riskLevel,
		"agent_id", agentID,
	)

	// If governance says REQUIRE_APPROVAL, create an approval request.
	if decision == "require_approval" && rc.deps.Approvals != nil && rc.deps.Config.Approvals.BaseURL != "" {
		approvalID, err := rc.createApprovalRequest(ctx, state, input.ActionType, input.ActionPayload, eval.GetRiskLevel())
		if err != nil {
			rc.deps.Metrics.DownstreamErrors.WithLabelValues("approvals").Inc()
			rc.logger.Error("approval request creation failed", "error", err)
		} else {
			out.ApprovalID = approvalID
			rc.deps.Metrics.ApprovalRequests.WithLabelValues(riskLevel).Inc()
			rc.deps.Events.Publish(workspaceID, "approval_request", approvalID, "created", map[string]any{
				"agent_id": agentID, "action_type": input.ActionType, "risk_level": riskLevel,
			})
		}
	}

	return nil, out, nil
}

func (rc *requestContext) createApprovalRequest(
	ctx context.Context,
	state *SessionState,
	actionType, actionPayload string,
	riskLevel governancev1.RiskLevel,
) (string, error) {
	agentID := ""
	agentToken := ""
	workspaceID := ""
	surface := ""
	if state != nil {
		agentID = state.AgentID
		agentToken = state.AgentToken
		workspaceID = state.WorkspaceID
		surface = state.Surface
	}

	req := connect.NewRequest(&approvalsv1.RequestApprovalRequest{
		WorkspaceId:   workspaceID,
		AgentId:       agentID,
		Surface:       surface,
		ActionType:    actionType,
		ActionPayload: []byte(actionPayload),
		RiskLevel:     mapRiskLevel(riskLevel),
	})
	if agentToken != "" {
		req.Header().Set("Authorization", "Bearer "+agentToken)
	}

	if rc.deps.Breakers != nil && !rc.deps.Breakers.Approvals.Allow() {
		rc.logger.Warn("approvals circuit breaker open -- failing closed")
		return "", fmt.Errorf("approvals service unreachable (circuit breaker open)")
	}
	start := time.Now()
	resp, err := rc.deps.Approvals.RequestApproval(ctx, req)
	rc.deps.Metrics.DownstreamLatency.WithLabelValues("approvals", "request_approval").Observe(time.Since(start).Seconds())
	if err != nil {
		if rc.deps.Breakers != nil {
			rc.deps.Breakers.Approvals.RecordFailure()
		}
		return "", err
	}
	if rc.deps.Breakers != nil {
		rc.deps.Breakers.Approvals.RecordSuccess()
	}
	return resp.Msg.GetApprovalRequest().GetId(), nil
}

type checkApprovalInput struct {
	ApprovalID string `json:"approval_id" jsonschema:"required,The approval request ID to check"`
	Wait       bool   `json:"wait,omitempty" jsonschema:"If true poll until resolved or timeout"`
}

type checkApprovalOutput struct {
	ApprovalID string `json:"approval_id"`
	State      string `json:"state"`
}

func (rc *requestContext) toolCheckApproval(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	input checkApprovalInput,
) (*mcpsdk.CallToolResult, checkApprovalOutput, error) {
	if rc.deps.Approvals == nil || rc.deps.Config.Approvals.BaseURL == "" {
		return nil, checkApprovalOutput{}, fmt.Errorf("approvals service not configured")
	}

	sid := rc.mcpSessionID()
	state, _ := rc.deps.Sessions.Get(sid)
	agentToken := ""
	workspaceID := ""
	if state != nil {
		agentToken = state.AgentToken
		workspaceID = state.WorkspaceID
	}

	if !input.Wait {
		return rc.checkApprovalOnce(ctx, input.ApprovalID, workspaceID, agentToken)
	}

	deadline := time.Now().Add(rc.deps.Config.Approvals.PollTimeout)
	for time.Now().Before(deadline) {
		result, out, err := rc.checkApprovalOnce(ctx, input.ApprovalID, workspaceID, agentToken)
		if err != nil {
			return nil, checkApprovalOutput{}, err
		}
		if out.State != "pending" {
			return result, out, nil
		}
		select {
		case <-ctx.Done():
			return nil, checkApprovalOutput{ApprovalID: input.ApprovalID, State: "timeout"}, nil
		case <-time.After(rc.deps.Config.Approvals.PollInterval):
		}
	}
	return nil, checkApprovalOutput{ApprovalID: input.ApprovalID, State: "timeout"}, nil
}

func (rc *requestContext) checkApprovalOnce(
	ctx context.Context,
	approvalID, workspaceID, agentToken string,
) (*mcpsdk.CallToolResult, checkApprovalOutput, error) {
	req := connect.NewRequest(&approvalsv1.ListPendingRequest{
		WorkspaceId: workspaceID,
	})
	if agentToken != "" {
		req.Header().Set("Authorization", "Bearer "+agentToken)
	}

	resp, err := rc.deps.Approvals.ListPending(ctx, req)
	if err != nil {
		return nil, checkApprovalOutput{}, fmt.Errorf("list pending approvals failed: %w", err)
	}

	for _, r := range resp.Msg.GetRequests() {
		if r.GetId() == approvalID {
			return nil, checkApprovalOutput{ApprovalID: approvalID, State: "pending"}, nil
		}
	}

	return nil, checkApprovalOutput{ApprovalID: approvalID, State: "resolved"}, nil
}

func decisionString(d governancev1.ActionDecision) string {
	switch d {
	case governancev1.ActionDecision_ACTION_DECISION_ALLOW:
		return "allow"
	case governancev1.ActionDecision_ACTION_DECISION_DENY:
		return "deny"
	case governancev1.ActionDecision_ACTION_DECISION_REQUIRE_APPROVAL:
		return "require_approval"
	default:
		return "allow"
	}
}

func riskLevelString(r governancev1.RiskLevel) string {
	switch r {
	case governancev1.RiskLevel_RISK_LEVEL_LOW:
		return "low"
	case governancev1.RiskLevel_RISK_LEVEL_MEDIUM:
		return "medium"
	case governancev1.RiskLevel_RISK_LEVEL_HIGH:
		return "high"
	case governancev1.RiskLevel_RISK_LEVEL_CRITICAL:
		return "critical"
	default:
		return "low"
	}
}

func mapRiskLevel(g governancev1.RiskLevel) approvalsv1.RiskLevel {
	switch g {
	case governancev1.RiskLevel_RISK_LEVEL_LOW:
		return approvalsv1.RiskLevel_RISK_LEVEL_LOW
	case governancev1.RiskLevel_RISK_LEVEL_MEDIUM:
		return approvalsv1.RiskLevel_RISK_LEVEL_MEDIUM
	case governancev1.RiskLevel_RISK_LEVEL_HIGH:
		return approvalsv1.RiskLevel_RISK_LEVEL_HIGH
	case governancev1.RiskLevel_RISK_LEVEL_CRITICAL:
		return approvalsv1.RiskLevel_RISK_LEVEL_CRITICAL
	default:
		return approvalsv1.RiskLevel_RISK_LEVEL_UNSPECIFIED
	}
}
