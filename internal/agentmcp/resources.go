package agentmcp

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"
	approvalsv1 "github.com/evalops/proto/gen/go/approvals/v1"
	memoryv1 "github.com/evalops/proto/gen/go/memory/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerResources(server *mcpsdk.Server, deps *Deps, sessionID string) {
	server.AddResource(&mcpsdk.Resource{
		URI:         "evalops://agent/status",
		Name:        "Agent Status",
		Description: "Current agent identity, scopes, and session info",
		MIMEType:    "application/json",
	}, func(_ context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return readAgentStatus(deps, sessionID)
	})

	server.AddResource(&mcpsdk.Resource{
		URI:         "evalops://agent/habits",
		Name:        "Approval Habits",
		Description: "Learned approval habits for this workspace — patterns the system has observed and their auto-approve confidence",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return readApprovalHabits(ctx, deps, sessionID)
	})

	server.AddResource(&mcpsdk.Resource{
		URI:         "evalops://agent/operating-rules",
		Name:        "Operating Rules",
		Description: "Behavioral rules and constraints from the memory service that this agent should follow",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return readOperatingRules(ctx, deps, sessionID)
	})
}

func readAgentStatus(deps *Deps, sessionID string) (*mcpsdk.ReadResourceResult, error) {
	state, ok := deps.Sessions.Get(sessionID)
	if !ok {
		return jsonResource("evalops://agent/status", map[string]any{
			"registered": false,
			"message":    "no active session — call evalops_register first",
		})
	}
	return jsonResource("evalops://agent/status", map[string]any{
		"registered":      true,
		"agent_id":        state.AgentID,
		"agent_type":      state.AgentType,
		"surface":         state.Surface,
		"capabilities":    state.Capabilities,
		"workspace_id":    state.WorkspaceID,
		"organization_id": state.OrganizationID,
		"run_id":          state.RunID,
		"expires_at":      state.ExpiresAt.Format("2006-01-02T15:04:05Z"),
		"active_sessions": deps.Sessions.ActiveCount(),
	})
}

func readApprovalHabits(ctx context.Context, deps *Deps, sessionID string) (*mcpsdk.ReadResourceResult, error) {
	if deps.Approvals == nil || deps.Config.Approvals.BaseURL == "" {
		return jsonResource("evalops://agent/habits", map[string]any{
			"available": false,
			"message":   "approvals service not configured",
		})
	}

	state, _ := deps.Sessions.Get(sessionID)
	workspaceID := ""
	agentToken := ""
	if state != nil {
		workspaceID = state.WorkspaceID
		agentToken = state.AgentToken
	}

	req := connect.NewRequest(&approvalsv1.GetHabitsRequest{
		WorkspaceId: workspaceID,
	})
	if agentToken != "" {
		req.Header().Set("Authorization", "Bearer "+agentToken)
	}

	resp, err := deps.Approvals.GetHabits(ctx, req)
	if err != nil {
		return jsonResource("evalops://agent/habits", map[string]any{
			"available": false,
			"error":     err.Error(),
		})
	}

	habits := make([]map[string]any, 0, len(resp.Msg.GetHabits()))
	for _, h := range resp.Msg.GetHabits() {
		habits = append(habits, map[string]any{
			"pattern":                 h.GetPattern(),
			"observation_count":       h.GetObservationCount(),
			"auto_approve_confidence": h.GetAutoApproveConfidence(),
		})
	}

	return jsonResource("evalops://agent/habits", map[string]any{
		"available": true,
		"habits":    habits,
	})
}

func readOperatingRules(ctx context.Context, deps *Deps, sessionID string) (*mcpsdk.ReadResourceResult, error) {
	if deps.Memory == nil || deps.Config.Memory.BaseURL == "" {
		return jsonResource("evalops://agent/operating-rules", map[string]any{
			"available": false,
			"message":   "memory service not configured",
		})
	}

	state, _ := deps.Sessions.Get(sessionID)
	agentToken := ""
	orgID := ""
	if state != nil {
		agentToken = state.AgentToken
		orgID = state.OrganizationID
	}

	req := connect.NewRequest(&memoryv1.GetOperatingRulesRequest{})
	if agentToken != "" {
		req.Header().Set("Authorization", "Bearer "+agentToken)
	}
	if orgID != "" {
		req.Header().Set("X-Organization-ID", orgID)
	}

	resp, err := deps.Memory.GetOperatingRules(ctx, req)
	if err != nil {
		return jsonResource("evalops://agent/operating-rules", map[string]any{
			"available": false,
			"error":     err.Error(),
		})
	}

	rules := make([]map[string]any, 0, len(resp.Msg.GetMemories()))
	for _, m := range resp.Msg.GetMemories() {
		rules = append(rules, map[string]any{
			"id":      m.GetId(),
			"content": m.GetContent(),
			"tags":    m.GetTags(),
		})
	}

	return jsonResource("evalops://agent/operating-rules", map[string]any{
		"available": true,
		"rules":     rules,
	})
}

func jsonResource(uri string, data any) (*mcpsdk.ReadResourceResult, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal resource: %w", err)
	}
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{
			{
				URI:      uri,
				MIMEType: "application/json",
				Text:     string(b),
			},
		},
	}, nil
}
