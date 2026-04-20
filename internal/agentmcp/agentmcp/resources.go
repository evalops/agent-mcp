package agentmcp

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"
	approvalsv1 "github.com/evalops/proto/gen/go/approvals/v1"
	memoryv1 "github.com/evalops/proto/gen/go/memory/v1"
	"github.com/evalops/service-runtime/downstream"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const protocolInstructions = "## EvalOps Agent Protocol\n" +
	"\n" +
	"### Session Lifecycle\n" +
	"\n" +
	"1. **Start of session**: Call `evalops_register` with your agent_type " +
	"(e.g. \"coding_agent\"), surface (e.g. \"cli\", \"ide\", \"slack\"), " +
	"and any user_token for authentication.\n" +
	"\n" +
	"2. **Every 60 seconds**: Call `evalops_heartbeat` to maintain session " +
	"liveness and agent-registry presence. Failure to heartbeat will mark the agent " +
	"as unavailable after 3 missed intervals.\n" +
	"\n" +
	"3. **End of session**: Call `evalops_deregister` to cleanly revoke the " +
	"session. If omitted, the session expires based on TTL.\n" +
	"\n" +
	"### Governance Protocol\n" +
	"\n" +
	"Before executing actions that modify state \u2014 file writes, shell commands, " +
	"API calls, database mutations, sending messages, or accessing external " +
	"services \u2014 call `evalops_check_action` with:\n" +
	"- `action_type`: category of action (e.g. \"file_write\", \"shell_exec\", " +
	"\"api_call\", \"message_send\")\n" +
	"- `description`: human-readable summary of what the action will do\n" +
	"- `risk_level`: your assessment \u2014 LOW, MEDIUM, HIGH, or CRITICAL\n" +
	"- keep your local `agent-hook` at or above the version advertised " +
	"in `evalops://agent/hook-requirements`; older binaries treat newer " +
	"approval responses such as `AUTO_APPROVED` as denials\n" +
	"\n" +
	"**Responses:**\n" +
	"- `allow` \u2014 proceed with the action\n" +
	"- `deny` \u2014 do not execute; explain the denial reason to the user\n" +
	"- `require_approval` \u2014 call `evalops_check_approval` with the returned " +
	"approval_request_id. Poll every 3 seconds until the state is no longer " +
	"\"pending\".\n" +
	"\n" +
	"### Tool Discovery\n" +
	"\n" +
	"After registration, call `evalops_list_tools` to discover the namespaced " +
	"tool catalog available through agent-mcp. Tools use the convention " +
	"`<service>.<object>.<action>` and include risk, approval, cost, and " +
	"provenance metadata for runtime enumeration.\n" +
	"\n" +
	"### Usage Reporting\n" +
	"\n" +
	"After each LLM inference call, report usage via `evalops_report_usage` " +
	"with model name, input/output token counts, and cost if known. This " +
	"enables cost attribution and budget enforcement.\n" +
	"\n" +
	"### Memory Protocol\n" +
	"\n" +
	"Before tackling work that may depend on prior project context, call " +
	"`evalops_recall` to search the agent, project, team, or organization " +
	"memory scopes for relevant facts. When you learn something durable that " +
	"should survive the current session, call `evalops_store_memory` to " +
	"persist that fact for future runs.\n"

const (
	agentHookRequirementsURI    = "evalops://agent/hook-requirements"
	minimumSupportedHookVersion = "v0.1.36"
	platformHookReleasesURL     = "https://github.com/evalops/platform/releases"
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
		URI:         agentHookRequirementsURI,
		Name:        "Agent Hook Requirements",
		Description: "Operational guidance for local EvalOps agent tooling, including the minimum required hook binary version",
		MIMEType:    "application/json",
	}, func(_ context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return readHookRequirements(deps, sessionID)
	})

	server.AddResource(&mcpsdk.Resource{
		URI:         "evalops://agent/habits",
		Name:        "Approval Habits",
		Description: "Learned approval habits for this workspace \u2014 patterns the system has observed and their auto-approve confidence",
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

	server.AddResource(&mcpsdk.Resource{
		URI:         "evalops://agent/instructions",
		Name:        "Agent Protocol",
		Description: "Integration protocol for EvalOps \u2014 when and how to call each tool",
		MIMEType:    "text/markdown",
	}, func(_ context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return readProtocolInstructions(deps)
	})
}

func readAgentStatus(deps *Deps, sessionID string) (*mcpsdk.ReadResourceResult, error) {
	state, ok := deps.Sessions.Get(sessionID)
	if !ok {
		return jsonResource("evalops://agent/status", map[string]any{
			"registered": false,
			"message":    "no active session \u2014 call evalops_register first",
		})
	}
	if state.IsAnonymous() {
		return jsonResource("evalops://agent/status", map[string]any{
			"active_sessions": deps.Sessions.ActiveCount(),
			"anonymous":       true,
			"expires_at":      state.ExpiresAt.Format("2006-01-02T15:04:05Z"),
			"message":         "anonymous sandbox session active \u2014 authenticate to register an agent or unlock write operations",
			"registered":      false,
			"session_type":    SessionTypeAnonymous,
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

func readHookRequirements(deps *Deps, sessionID string) (*mcpsdk.ReadResourceResult, error) {
	state, _ := deps.Sessions.Get(sessionID)

	data := map[string]any{
		"available":                              true,
		"minimum_hook_version":                   minimumSupportedHookVersion,
		"hook_release_download_url":              platformHookReleasesURL,
		"auto_approved_decisions_require_update": true,
		"message":                                fmt.Sprintf("Update your local agent-hook to %s or newer. Older hook binaries deny AUTO_APPROVED approval decisions with no local remediation hint.", minimumSupportedHookVersion),
		"instructions": []string{
			fmt.Sprintf("Install agent-hook %s or newer from the platform releases.", minimumSupportedHookVersion),
			"If your local hook predates this version, AUTO_APPROVED decisions are treated as denials.",
			"After replacing the binary, restart your local agent session so the updated hook is used for new PreToolUse checks.",
		},
	}
	if state != nil {
		data["agent_id"] = state.AgentID
		data["workspace_id"] = state.WorkspaceID
		data["organization_id"] = state.OrganizationID
		data["surface"] = state.Surface
	}

	return jsonResource(agentHookRequirementsURI, data)
}

func readApprovalHabits(ctx context.Context, deps *Deps, sessionID string) (*mcpsdk.ReadResourceResult, error) {
	state, _ := deps.Sessions.Get(sessionID)
	if state != nil && state.IsAnonymous() {
		return jsonResource("evalops://agent/habits", map[string]any{
			"anonymous": true,
			"available": true,
			"habits":    []any{},
			"message":   "anonymous sandbox exposes workspace-default approval habits only; authenticate for learned workspace habits",
		})
	}
	workspaceID := ""
	agentToken := ""
	if state != nil {
		workspaceID = state.WorkspaceID
		agentToken = state.AgentToken
	}
	if workspaceID != "" && deps.HabitCache != nil {
		if habits, ok := deps.HabitCache.Get(workspaceID); ok {
			return approvalHabitsResource(habits)
		}
	}
	if deps.Approvals == nil || deps.Config.Approvals.BaseURL == "" {
		return jsonResource("evalops://agent/habits", map[string]any{
			"available": false,
			"message":   "approvals service not configured",
		})
	}

	req := connect.NewRequest(&approvalsv1.GetHabitsRequest{
		WorkspaceId: workspaceID,
	})
	if agentToken != "" {
		req.Header().Set("Authorization", "Bearer "+agentToken)
	}

	resp, err := downstream.CallOp(ctx, deps.downstreamClients().Approvals, "get_habits", func(ctx context.Context) (*connect.Response[approvalsv1.GetHabitsResponse], error) {
		return deps.Approvals.GetHabits(ctx, req)
	})
	if err != nil {
		return jsonResource("evalops://agent/habits", map[string]any{
			"available": false,
			"error":     err.Error(),
		})
	}
	if resp == nil {
		return jsonResource("evalops://agent/habits", map[string]any{
			"available": false,
			"message":   "approvals service unavailable",
		})
	}
	if workspaceID != "" && deps.HabitCache != nil {
		deps.HabitCache.Store(workspaceID, resp.Msg.GetHabits())
	}

	return approvalHabitsResource(resp.Msg.GetHabits())
}

func approvalHabitsResource(habits []*approvalsv1.ApprovalHabit) (*mcpsdk.ReadResourceResult, error) {
	items := make([]map[string]any, 0, len(habits))
	for _, h := range habits {
		items = append(items, map[string]any{
			"pattern":                 h.GetPattern(),
			"observation_count":       h.GetObservationCount(),
			"approved_count":          h.GetApprovedCount(),
			"auto_approve_confidence": h.GetAutoApproveConfidence(),
		})
	}

	return jsonResource("evalops://agent/habits", map[string]any{
		"available": true,
		"habits":    items,
	})
}

func readOperatingRules(ctx context.Context, deps *Deps, sessionID string) (*mcpsdk.ReadResourceResult, error) {
	state, _ := deps.Sessions.Get(sessionID)
	if state != nil && state.IsAnonymous() {
		return jsonResource("evalops://agent/operating-rules", map[string]any{
			"anonymous": true,
			"available": true,
			"message":   "anonymous sandbox exposes public/default operating rules only; authenticate for organization-scoped rules",
			"rules":     []any{},
		})
	}
	if deps.Memory == nil || deps.Config.Memory.BaseURL == "" {
		return jsonResource("evalops://agent/operating-rules", map[string]any{
			"available": false,
			"message":   "memory service not configured",
		})
	}

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

	resp, err := downstream.CallOp(ctx, deps.downstreamClients().Memory, "get_operating_rules", func(ctx context.Context) (*connect.Response[memoryv1.GetOperatingRulesResponse], error) {
		return deps.Memory.GetOperatingRules(ctx, req)
	})
	if err != nil {
		return jsonResource("evalops://agent/operating-rules", map[string]any{
			"available": false,
			"error":     err.Error(),
		})
	}
	if resp == nil {
		return jsonResource("evalops://agent/operating-rules", map[string]any{
			"available": false,
			"message":   "memory service unavailable",
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

func readProtocolInstructions(_ *Deps) (*mcpsdk.ReadResourceResult, error) {
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{
			{
				URI:      "evalops://agent/instructions",
				MIMEType: "text/markdown",
				Text:     protocolInstructions,
			},
		},
	}, nil
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
