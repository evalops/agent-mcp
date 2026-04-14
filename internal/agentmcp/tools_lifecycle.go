package agentmcp

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	start := time.Now()
	session, err := rc.deps.Identity.RegisterAgent(ctx, userToken, clients.RegisterAgentRequest{
		AgentType:    input.AgentType,
		Capabilities: input.Capabilities,
		Scopes:       input.Scopes,
		Surface:      input.Surface,
		TTLSeconds:   input.TTLSeconds,
	})
	rc.deps.Metrics.DownstreamLatency.WithLabelValues("identity", "register").Observe(time.Since(start).Seconds())
	if err != nil {
		rc.deps.Metrics.DownstreamErrors.WithLabelValues("identity").Inc()
		rc.logger.Error("identity registration failed", "error", err)
		return nil, registerOutput{}, fmt.Errorf("identity registration failed: %w", err)
	}

	rc.logger.Info("agent registered with identity",
		"agent_id", session.AgentID,
		"agent_type", input.AgentType,
		"surface", input.Surface,
	)

	// Store session state.
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
		rc.deps.Metrics.ActiveSessions.Set(float64(rc.deps.Sessions.ActiveCount()))
	}

	// Step 2: Register with Registry (best-effort).
	registryVisible := false
	if rc.deps.Registry != nil && rc.deps.Config.Registry.BaseURL != "" {
		regStart := time.Now()
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
			rc.deps.Metrics.DownstreamErrors.WithLabelValues("registry").Inc()
			rc.logger.Warn("registry registration failed (non-fatal)", "error", err)
		} else {
			registryVisible = true
		}
		rc.deps.Metrics.DownstreamLatency.WithLabelValues("registry", "register").Observe(time.Since(regStart).Seconds())
	}

	rc.deps.Metrics.Registrations.WithLabelValues(input.AgentType, input.Surface).Inc()
	rc.deps.Events.Publish(input.WorkspaceID, "agent", session.AgentID, "registered", map[string]any{
		"agent_type": input.AgentType, "surface": input.Surface, "registry_visible": registryVisible,
	})

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

	start := time.Now()
	session, err := rc.deps.Identity.HeartbeatAgent(ctx, state.AgentToken, input.TTLSeconds)
	rc.deps.Metrics.DownstreamLatency.WithLabelValues("identity", "heartbeat").Observe(time.Since(start).Seconds())
	if err != nil {
		rc.deps.Metrics.DownstreamErrors.WithLabelValues("identity").Inc()
		rc.logger.Error("identity heartbeat failed", "agent_id", state.AgentID, "error", err)
		return nil, heartbeatOutput{}, fmt.Errorf("identity heartbeat failed: %w", err)
	}

	// Update stored state with the rotated token.
	state.AgentToken = session.AgentToken
	state.ExpiresAt = session.ExpiresAt
	rc.deps.Sessions.Set(sid, state)

	// Heartbeat Registry (best-effort).
	if rc.deps.Registry != nil && rc.deps.Config.Registry.BaseURL != "" {
		hbStart := time.Now()
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
			rc.deps.Metrics.DownstreamErrors.WithLabelValues("registry").Inc()
			rc.logger.Warn("registry heartbeat failed (non-fatal)", "error", err)
		}
		rc.deps.Metrics.DownstreamLatency.WithLabelValues("registry", "heartbeat").Observe(time.Since(hbStart).Seconds())
	}

	rc.deps.Metrics.Heartbeats.Inc()
	rc.logger.Info("heartbeat completed", "agent_id", state.AgentID)

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

	// Deregister from Registry first (best-effort).
	if rc.deps.Registry != nil && rc.deps.Config.Registry.BaseURL != "" {
		deregReq := connect.NewRequest(&agentsv1.DeregisterRequest{Id: agentID})
		deregReq.Header().Set("Authorization", "Bearer "+state.AgentToken)
		if state.WorkspaceID != "" {
			deregReq.Header().Set("X-Workspace-ID", state.WorkspaceID)
		}
		if _, err := rc.deps.Registry.Deregister(ctx, deregReq); err != nil {
			rc.deps.Metrics.DownstreamErrors.WithLabelValues("registry").Inc()
			rc.logger.Warn("registry deregister failed (non-fatal)", "error", err)
		}
	}

	// Deregister from Identity.
	start := time.Now()
	if err := rc.deps.Identity.DeregisterAgent(ctx, state.AgentToken); err != nil {
		rc.deps.Metrics.DownstreamErrors.WithLabelValues("identity").Inc()
		rc.deps.Metrics.DownstreamLatency.WithLabelValues("identity", "deregister").Observe(time.Since(start).Seconds())
		return nil, deregisterOutput{}, fmt.Errorf("identity deregister failed: %w", err)
	}
	rc.deps.Metrics.DownstreamLatency.WithLabelValues("identity", "deregister").Observe(time.Since(start).Seconds())

	rc.deps.Sessions.Delete(sid)
	rc.deps.Metrics.ActiveSessions.Set(float64(rc.deps.Sessions.ActiveCount()))
	rc.deps.Metrics.Deregistrations.Inc()
	rc.deps.Events.Publish(state.WorkspaceID, "agent", agentID, "deregistered", nil)
	rc.logger.Info("agent deregistered", "agent_id", agentID)

	return nil, deregisterOutput{AgentID: agentID, Deregistered: true}, nil
}
