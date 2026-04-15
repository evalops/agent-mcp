package agentmcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	memoryv1 "github.com/evalops/proto/gen/go/memory/v1"
	"github.com/evalops/service-runtime/downstream"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultRecallTopK   = 5
	defaultMemoryType   = "reference"
	defaultMemorySource = "agent-mcp"
)

type recallInput struct {
	Query         string  `json:"query" jsonschema:"required,Search query for relevant prior knowledge"`
	Scope         string  `json:"scope,omitempty" jsonschema:"Memory scope: agent, project, team, organization, user"`
	TopK          int32   `json:"top_k,omitempty" jsonschema:"Maximum number of results to return"`
	MinSimilarity float32 `json:"min_similarity,omitempty" jsonschema:"Minimum similarity score for results"`
	ProjectID     string  `json:"project_id,omitempty" jsonschema:"Optional project scope filter"`
	TeamID        string  `json:"team_id,omitempty" jsonschema:"Optional team scope filter"`
	Repository    string  `json:"repository,omitempty" jsonschema:"Optional repository scope filter"`
	Agent         string  `json:"agent,omitempty" jsonschema:"Optional agent filter; defaults to the current agent when scope=agent"`
	Type          string  `json:"type,omitempty" jsonschema:"Optional memory type filter"`
}

type recallOutput struct {
	Available bool                 `json:"available"`
	Count     int                  `json:"count"`
	Message   string               `json:"message,omitempty"`
	Results   []memoryResultOutput `json:"results,omitempty"`
}

type storeMemoryInput struct {
	Content    string   `json:"content" jsonschema:"required,Durable fact or decision to persist"`
	Scope      string   `json:"scope,omitempty" jsonschema:"Memory scope: agent, project, team, organization, user"`
	Type       string   `json:"type,omitempty" jsonschema:"Memory type such as reference, project, feedback, user, or entity"`
	Source     string   `json:"source,omitempty" jsonschema:"Source label for the memory record"`
	Confidence float32  `json:"confidence,omitempty" jsonschema:"Optional confidence score between 0 and 1"`
	ProjectID  string   `json:"project_id,omitempty" jsonschema:"Optional project scope identifier"`
	TeamID     string   `json:"team_id,omitempty" jsonschema:"Optional team scope identifier"`
	Repository string   `json:"repository,omitempty" jsonschema:"Optional repository scope filter"`
	Agent      string   `json:"agent,omitempty" jsonschema:"Optional agent identifier; defaults to the current agent when scope=agent"`
	IsPolicy   bool     `json:"is_policy,omitempty" jsonschema:"Whether this memory should be treated as policy or rule-like guidance"`
	Tags       []string `json:"tags,omitempty" jsonschema:"Optional tags to aid future recall"`
	ID         string   `json:"id,omitempty" jsonschema:"Optional stable memory identifier"`
}

type storeMemoryOutput struct {
	Available bool                `json:"available"`
	Stored    bool                `json:"stored"`
	Message   string              `json:"message,omitempty"`
	Memory    *memoryResultOutput `json:"memory,omitempty"`
}

type memoryResultOutput struct {
	ID         string   `json:"id"`
	Scope      string   `json:"scope,omitempty"`
	Content    string   `json:"content"`
	Type       string   `json:"type,omitempty"`
	Source     string   `json:"source,omitempty"`
	Confidence float32  `json:"confidence,omitempty"`
	ProjectID  string   `json:"project_id,omitempty"`
	TeamID     string   `json:"team_id,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Agent      string   `json:"agent,omitempty"`
	IsPolicy   bool     `json:"is_policy,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
	Similarity float32  `json:"similarity,omitempty"`
}

func (rc *requestContext) toolRecall(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	input recallInput,
) (*mcpsdk.CallToolResult, recallOutput, error) {
	if rc.deps.Memory == nil || rc.deps.Config.Memory.BaseURL == "" {
		return nil, recallOutput{
			Available: false,
			Message:   "memory service not configured",
		}, nil
	}
	if strings.TrimSpace(input.Query) == "" {
		return nil, recallOutput{}, fmt.Errorf("missing query")
	}
	if rc.isAnonymousRequest() {
		return rc.authenticationRequiredResult("recall memories"), recallOutput{}, nil
	}

	state, result := rc.requireRegisteredSessionResult("recall memories")
	if result != nil {
		return result, recallOutput{}, nil
	}

	scope, err := parseMemoryScope(input.Scope)
	if err != nil {
		return nil, recallOutput{}, err
	}

	orgID := sessionOrganizationID(state)
	if orgID == "" {
		return rc.registrationRequiredResult("recall memories"), recallOutput{}, nil
	}

	agentFilter := strings.TrimSpace(input.Agent)
	if agentFilter == "" && scope == memoryv1.Scope_SCOPE_AGENT {
		agentFilter = state.AgentID
	}
	topK := input.TopK
	if topK <= 0 {
		topK = defaultRecallTopK
	}

	req := connect.NewRequest(&memoryv1.RecallRequest{
		Query:         strings.TrimSpace(input.Query),
		Scope:         scope,
		TopK:          topK,
		MinSimilarity: input.MinSimilarity,
		ProjectId:     strings.TrimSpace(input.ProjectID),
		TeamId:        strings.TrimSpace(input.TeamID),
		Repository:    strings.TrimSpace(input.Repository),
		Agent:         agentFilter,
		Type:          strings.TrimSpace(input.Type),
	})
	req.Header().Set("Authorization", "Bearer "+state.AgentToken)
	req.Header().Set("X-Organization-ID", orgID)

	resp, err := downstream.CallOp(ctx, rc.deps.downstreamClients().Memory, "recall", func(ctx context.Context) (*connect.Response[memoryv1.RecallResponse], error) {
		return rc.deps.Memory.Recall(ctx, req)
	})
	if err != nil {
		rc.logger.Warn("memory recall failed (non-fatal)", "error", err)
		return nil, recallOutput{
			Available: false,
			Message:   fmt.Sprintf("memory recall unavailable: %v", err),
		}, nil
	}
	if resp == nil {
		return nil, recallOutput{
			Available: false,
			Message:   "memory service unavailable",
		}, nil
	}

	results := make([]memoryResultOutput, 0, len(resp.Msg.GetResults()))
	for _, item := range resp.Msg.GetResults() {
		results = append(results, memoryToOutput(item.GetMemory(), item.GetSimilarity()))
	}

	workspaceID := strings.TrimSpace(state.WorkspaceID)
	if workspaceID == "" {
		workspaceID = orgID
	}
	rc.deps.Events.Publish(ctx, workspaceID, "memory", state.AgentID, "recalled", map[string]any{
		"agent_id":        state.AgentID,
		"organization_id": orgID,
		"query":           strings.TrimSpace(input.Query),
		"result_count":    len(results),
		"scope":           memoryScopeString(scope),
		"workspace_id":    workspaceID,
	})

	return nil, recallOutput{
		Available: true,
		Count:     len(results),
		Results:   results,
	}, nil
}

func (rc *requestContext) toolStoreMemory(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	input storeMemoryInput,
) (*mcpsdk.CallToolResult, storeMemoryOutput, error) {
	if rc.deps.Memory == nil || rc.deps.Config.Memory.BaseURL == "" {
		return nil, storeMemoryOutput{
			Available: false,
			Message:   "memory service not configured",
		}, nil
	}
	if strings.TrimSpace(input.Content) == "" {
		return nil, storeMemoryOutput{}, fmt.Errorf("missing content")
	}
	if rc.isAnonymousRequest() {
		return rc.authenticationRequiredResult("store memories"), storeMemoryOutput{}, nil
	}

	state, result := rc.requireRegisteredSessionResult("store memories")
	if result != nil {
		return result, storeMemoryOutput{}, nil
	}

	scope, err := parseMemoryScope(input.Scope)
	if err != nil {
		return nil, storeMemoryOutput{}, err
	}

	orgID := sessionOrganizationID(state)
	if orgID == "" {
		return rc.registrationRequiredResult("store memories"), storeMemoryOutput{}, nil
	}

	agentID := strings.TrimSpace(input.Agent)
	if agentID == "" && scope == memoryv1.Scope_SCOPE_AGENT {
		agentID = state.AgentID
	}

	memType := strings.TrimSpace(input.Type)
	if memType == "" {
		memType = defaultMemoryType
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = defaultMemorySource
	}

	req := connect.NewRequest(&memoryv1.StoreRequest{
		Scope:      scope,
		Content:    strings.TrimSpace(input.Content),
		Type:       memType,
		Source:     source,
		Confidence: input.Confidence,
		ProjectId:  strings.TrimSpace(input.ProjectID),
		TeamId:     strings.TrimSpace(input.TeamID),
		Repository: strings.TrimSpace(input.Repository),
		Agent:      agentID,
		IsPolicy:   input.IsPolicy,
		Tags:       append([]string(nil), input.Tags...),
		Id:         strings.TrimSpace(input.ID),
	})
	req.Header().Set("Authorization", "Bearer "+state.AgentToken)
	req.Header().Set("X-Organization-ID", orgID)

	resp, err := downstream.CallOp(ctx, rc.deps.downstreamClients().Memory, "store", func(ctx context.Context) (*connect.Response[memoryv1.StoreResponse], error) {
		return rc.deps.Memory.Store(ctx, req)
	})
	if err != nil {
		rc.logger.Warn("memory store failed (non-fatal)", "error", err)
		return nil, storeMemoryOutput{
			Available: false,
			Stored:    false,
			Message:   fmt.Sprintf("memory store unavailable: %v", err),
		}, nil
	}
	if resp == nil || resp.Msg.GetMemory() == nil {
		return nil, storeMemoryOutput{
			Available: false,
			Stored:    false,
			Message:   "memory service unavailable",
		}, nil
	}

	memoryOut := memoryToOutput(resp.Msg.GetMemory(), 0)
	workspaceID := strings.TrimSpace(state.WorkspaceID)
	if workspaceID == "" {
		workspaceID = orgID
	}
	rc.deps.Events.Publish(ctx, workspaceID, "memory", memoryOut.ID, "stored", map[string]any{
		"agent_id":        state.AgentID,
		"memory_id":       memoryOut.ID,
		"organization_id": orgID,
		"scope":           memoryOut.Scope,
		"type":            memoryOut.Type,
		"workspace_id":    workspaceID,
	})

	return nil, storeMemoryOutput{
		Available: true,
		Stored:    true,
		Memory:    &memoryOut,
	}, nil
}

func (rc *requestContext) requireRegisteredSessionResult(action string) (*SessionState, *mcpsdk.CallToolResult) {
	state, ok := rc.currentSession()
	if !ok || state == nil || state.IsAnonymous() || strings.TrimSpace(state.AgentToken) == "" {
		return nil, rc.registrationRequiredResult(action)
	}
	return state, nil
}

func (rc *requestContext) registrationRequiredResult(action string) *mcpsdk.CallToolResult {
	message := "This action requires an active EvalOps agent session. Call evalops_register first."
	if trimmed := strings.TrimSpace(action); trimmed != "" {
		message = fmt.Sprintf("%s Then retry to %s.", message, trimmed)
	}
	return structuredToolError(map[string]any{
		"error":         "registration_required",
		"message":       message,
		"required_tool": "evalops_register",
	})
}

func sessionOrganizationID(state *SessionState) string {
	if state == nil {
		return ""
	}
	if orgID := strings.TrimSpace(state.OrganizationID); orgID != "" {
		return orgID
	}
	return strings.TrimSpace(state.WorkspaceID)
}

func parseMemoryScope(raw string) (memoryv1.Scope, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "agent":
		return memoryv1.Scope_SCOPE_AGENT, nil
	case "organization", "org", "workspace":
		return memoryv1.Scope_SCOPE_ORGANIZATION, nil
	case "project":
		return memoryv1.Scope_SCOPE_PROJECT, nil
	case "team":
		return memoryv1.Scope_SCOPE_TEAM, nil
	case "user":
		return memoryv1.Scope_SCOPE_USER, nil
	default:
		return memoryv1.Scope_SCOPE_UNSPECIFIED, fmt.Errorf("invalid scope %q", raw)
	}
}

func memoryScopeString(scope memoryv1.Scope) string {
	switch scope {
	case memoryv1.Scope_SCOPE_USER:
		return "user"
	case memoryv1.Scope_SCOPE_TEAM:
		return "team"
	case memoryv1.Scope_SCOPE_ORGANIZATION:
		return "organization"
	case memoryv1.Scope_SCOPE_AGENT:
		return "agent"
	case memoryv1.Scope_SCOPE_PROJECT:
		return "project"
	default:
		return "unspecified"
	}
}

func memoryToOutput(memory *memoryv1.Memory, similarity float32) memoryResultOutput {
	if memory == nil {
		return memoryResultOutput{}
	}

	out := memoryResultOutput{
		ID:         memory.GetId(),
		Scope:      memoryScopeString(memory.GetScope()),
		Content:    memory.GetContent(),
		Type:       memory.GetType(),
		Source:     memory.GetSource(),
		Confidence: memory.GetConfidence(),
		ProjectID:  memory.GetProjectId(),
		TeamID:     memory.GetTeamId(),
		Repository: memory.GetRepository(),
		Agent:      memory.GetAgent(),
		IsPolicy:   memory.GetIsPolicy(),
		Tags:       append([]string(nil), memory.GetTags()...),
		Similarity: similarity,
	}
	if createdAt := memory.GetCreatedAt(); createdAt != nil {
		out.CreatedAt = createdAt.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if updatedAt := memory.GetUpdatedAt(); updatedAt != nil {
		out.UpdatedAt = updatedAt.AsTime().UTC().Format(time.RFC3339Nano)
	}
	return out
}
