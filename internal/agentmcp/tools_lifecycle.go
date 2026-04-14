package agentmcp

import (
	"context"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	"github.com/evalops/agent-mcp/internal/clients"
	agentsv1 "github.com/evalops/proto/gen/go/agents/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type registerInput struct {
	AgentType    string   `json:"agent_type" jsonschema:"required,Agent type such as claude-code or codex"`
	Capabilities []string `json:"capabilities,omitempty" jsonschema:"Declared agent capabilities"`
	Scopes       []string `json:"scopes,omitempty" jsonschema:"Scopes to request from the launching user token"`
	Surface      string   `json:"surface" jsonschema:"required,Execution surface such as cli or ide"`
	TTLSeconds   int      `json:"ttl_seconds,omitempty" jsonschema:"Requested session TTL in seconds"`
	UserToken    string   `json:"user_token,omitempty" jsonschema:"Launching user access token; defaults to the MCP Authorization bearer token"`
	WorkspaceID  string   `json:"workspace_id,omitempty" jsonschema:"EvalOps workspace or organization ID"`
}

type registerOutput struct {
	AgentID         string   `json:"agent_id"`
	ExpiresAt       string   `json:"expires_at"`
	Registered      bool     `json:"registered"`
	RegistryVisible bool     `json:"registry_visible"`
	RunID           string   `json:"run_id"`
	ScopesGranted   []string `json:"scopes_granted,omitempty"`
	ScopesDenied    []string `json:"scopes_denied,omitempty"`
}

func (rc *requestContext) toolRegister(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	input registerInput,
) (*mcpsdk.CallToolResult, registerOutput, error) {
	userToken := strings.TrimSpace(input.UserToken)
	if userToken == "" {
		userToken = rc.bearerToken()
	}
	if userToken == "" {
		return nil, registerOutput{}, fmt.Errorf("missing user token: provide user_token or set Authorization bearer header")
	}
	if strings.TrimSpace(input.AgentType) == "" {
		return nil, registerOutput{}, fmt.Errorf("agent_type is required")
	}
	if strings.TrimSpace(input.Surface) == "" {
		return nil, registerOutput{}, fmt.Errorf("surface is required")
	}

	// Step 1: Register with Identity.
	session, err := rc.deps.Identity.RegisterAgent(ctx, userToken, clients.RegisterAgentRequest{
		AgentType:    input.AgentType,
		Capabilities: input.Capabilities,
		Scopes:       input.Scopes,
		Surface:      input.Surface,
		TTLSeconds:   input.TTLSeconds,
	})
	if err != nil {
		return nil, registerOutput{}, fmt.Errorf("identity registration failed: %w", err)
	}

	sid := rc.mcpSessionID()
	if sid != "" {
		rc.deps.Sessions.Set(sid, &SessionState{
			AgentID:        session.AgentID,
			AgentToken:     session.AgentToken,
			AgentType:      input.AgentType,
			Capabilities:   input.Capabilities,
			ExpiresAt:      session.ExpiresAt,
			OrganizationID: input.WorkspaceID,
			RunID:          session.RunID,
			Surface:        input.Surface,
			WorkspaceID:    input.WorkspaceID,
		})
	}

	// Step 2: Register with Registry.
	registryVisible := false
	if rc.deps.Registry != nil && rc.deps.Config.Registry.BaseURL != "" {
		regReq := connect.NewRequest(&agentsv1.RegisterRequest{
			Name:         fmt.Sprintf("%s/%s", input.AgentType, input.Surface),
			AgentType:    input.AgentType,
			Capabilities: input.Capabilities,
			Surfaces:     []string{input.Surface},
		})
		if input.WorkspaceID != "" {
			regReq.Header().Set("X-Workspace-ID", input.WorkspaceID)
		}
		regReq.Header().Set("Authorization", "Bearer "+session.AgentToken)

		if _, err := rc.deps.Registry.Register(ctx, regReq); err != nil {
			log.Printf("registry registration failed (non-fatal): %v", err)
		} else {
			registryVisible = true
		}
	}

	return nil, registerOutput{
		AgentID:         session.AgentID,
		ExpiresAt:       session.ExpiresAt.Format("2006-01-02T15:04:05Z"),
		Registered:      true,
		RegistryVisible: registryVisible,
		RunID:           session.RunID,
		ScopesGranted:   session.ScopesGranted,
		ScopesDenied:    session.ScopesDenied,
	}, nil
}

type heartbeatInput struct {
	TTLSeconds int `json:"ttl_seconds,omitempty" jsonschema:"Requested session TTL extension in seconds"`
}

type heartbeatOutput struct {
	AgentID   string `json:"agent_id"`
	ExpiresAt string `json:"expires_at"`
	Renewed   bool   `json:"renewed"`
}

func (rc *requestContext) toolHeartbeat(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	input heartbeatInput,
) (*mcpsdk.CallToolResult, heartbeatOutput, error) {
	sid := rc.mcpSessionID()
	state, ok := rc.deps.Sessions.Get(sid)
	if !ok {
		return nil, heartbeatOutput{}, fmt.Errorf("no active agent session — call evalops_register first")
	}

	session, err := rc.deps.Identity.HeartbeatAgent(ctx, state.AgentToken, input.TTLSeconds)
	if err != nil {
		return nil, heartbeatOutput{}, fmt.Errorf("identity heartbeat failed: %w", err)
	}

	state.AgentToken = session.AgentToken
	state.ExpiresAt = session.ExpiresAt
	rc.deps.Sessions.Set(sid, state)

	if rc.deps.Registry != nil && rc.deps.Config.Registry.BaseURL != "" {
		hbReq := connect.NewRequest(&agentsv1.HeartbeatRequest{
			AgentId: state.AgentID,
			Status:  agentsv1.AgentStatus_AGENT_STATUS_ACTIVE,
			Surface: state.Surface,
		})
		hbReq.Header().Set("Authorization", "Bearer "+session.AgentToken)
		if state.WorkspaceID != "" {
			hbReq.Header().Set("X-Workspace-ID", state.WorkspaceID)
		}
		if _, err := rc.deps.Registry.Heartbeat(ctx, hbReq); err != nil {
			log.Printf("registry heartbeat failed (non-fatal): %v", err)
		}
	}

	return nil, heartbeatOutput{
		AgentID:   session.AgentID,
		ExpiresAt: session.ExpiresAt.Format("2006-01-02T15:04:05Z"),
		Renewed:   true,
	}, nil
}

type deregisterInput struct{}

type deregisterOutput struct {
	AgentID      string `json:"agent_id"`
	Deregistered bool   `json:"deregistered"`
}

func (rc *requestContext) toolDeregister(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	_ deregisterInput,
) (*mcpsdk.CallToolResult, deregisterOutput, error) {
	sid := rc.mcpSessionID()
	state, ok := rc.deps.Sessions.Get(sid)
	if !ok {
		return nil, deregisterOutput{}, fmt.Errorf("no active agent session")
	}

	agentID := state.AgentID

	if rc.deps.Registry != nil && rc.deps.Config.Registry.BaseURL != "" {
		deregReq := connect.NewRequest(&agentsv1.DeregisterRequest{
			Id: agentID,
		})
		deregReq.Header().Set("Authorization", "Bearer "+state.AgentToken)
		if state.WorkspaceID != "" {
			deregReq.Header().Set("X-Workspace-ID", state.WorkspaceID)
		}
		if _, err := rc.deps.Registry.Deregister(ctx, deregReq); err != nil {
			log.Printf("registry deregister failed (non-fatal): %v", err)
		}
	}

	if err := rc.deps.Identity.DeregisterAgent(ctx, state.AgentToken); err != nil {
		return nil, deregisterOutput{}, fmt.Errorf("identity deregister failed: %w", err)
	}

	rc.deps.Sessions.Delete(sid)
	return nil, deregisterOutput{AgentID: agentID, Deregistered: true}, nil
}
