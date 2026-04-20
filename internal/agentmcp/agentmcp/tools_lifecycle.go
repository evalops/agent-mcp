package agentmcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	agentsv1 "github.com/evalops/proto/gen/go/agents/v1"
	"github.com/evalops/agent-mcp/internal/agentmcp/clients"
	"github.com/evalops/agent-mcp/internal/agentmcp/config"
	"github.com/evalops/service-runtime/downstream"
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
	if rc.isAnonymousRequest() {
		return rc.authenticationRequiredResult("register this agent"), registerOutput{}, nil
	}
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(rc.deps.Config.Federation.DefaultWorkspaceID)
	}
	if strings.TrimSpace(input.AgentType) == "" {
		return nil, registerOutput{}, fmt.Errorf("agent_type is required")
	}
	if strings.TrimSpace(input.Surface) == "" {
		return nil, registerOutput{}, fmt.Errorf("surface is required")
	}

	sid := rc.mcpSessionID()
	if sid != "" && !hasSessionCapacity(rc.deps.Sessions, sid, rc.deps.Config.Session.MaxActive) {
		return nil, registerOutput{}, fmt.Errorf("active session limit reached")
	}

	session, err := rc.registerWithIdentity(ctx, input, workspaceID)
	if err != nil {
		rc.logger.Error("identity registration failed", "error", err)
		return nil, registerOutput{}, fmt.Errorf("identity registration failed: %w", err)
	}

	rc.logger.Info("agent registered with identity",
		"agent_id", session.AgentID,
		"agent_type", input.AgentType,
		"surface", input.Surface,
	)

	// Store session state.
	if sid != "" {
		state := &SessionState{
			SessionType:    SessionTypeAgent,
			AgentID:        session.AgentID,
			AgentToken:     session.AgentToken,
			AgentType:      input.AgentType,
			Capabilities:   input.Capabilities,
			ExpiresAt:      session.ExpiresAt,
			OrganizationID: workspaceID,
			RunID:          session.RunID,
			Surface:        input.Surface,
			WorkspaceID:    workspaceID,
		}
		if !rc.deps.Sessions.SetIfUnderLimit(sid, state, rc.deps.Config.Session.MaxActive) {
			if session.AgentToken != "" {
				if err := rc.deps.Identity.DeregisterAgent(ctx, session.AgentToken); err != nil {
					rc.logger.Warn("identity deregister after session limit rejection failed", "agent_id", session.AgentID, "error", err)
				}
			}
			return nil, registerOutput{}, fmt.Errorf("active session limit reached")
		}
		rc.deps.Metrics.ActiveSessions.Set(float64(rc.deps.Sessions.ActiveCount()))
	}

	// Step 2: Register with Agent Registry (best-effort, fail-open).
	registryVisible := false
	if rc.deps.Registry != nil && rc.deps.Config.Registry.BaseURL != "" {
		regReq := connect.NewRequest(&agentsv1.RegisterRequest{
			Name:         fmt.Sprintf("%s/%s", input.AgentType, input.Surface),
			AgentType:    input.AgentType,
			Capabilities: input.Capabilities,
			Surfaces:     []string{input.Surface},
		})
		if workspaceID != "" {
			regReq.Header().Set("X-Workspace-ID", workspaceID)
		}
		regReq.Header().Set("Authorization", "Bearer "+session.AgentToken)

		regResp, _ := downstream.CallOp(ctx, rc.deps.downstreamClients().Registry, "register", func(ctx context.Context) (*connect.Response[agentsv1.RegisterResponse], error) {
			return rc.deps.Registry.Register(ctx, regReq)
		})
		if regResp != nil {
			registryVisible = true
		}
	}

	rc.deps.Metrics.Registrations.WithLabelValues(input.AgentType, input.Surface).Inc()
	rc.deps.Events.Publish(ctx, workspaceID, "agent", session.AgentID, "registered", map[string]any{
		"agent_id":         session.AgentID,
		"agent_type":       input.AgentType,
		"expires_at":       session.ExpiresAt.Format(time.RFC3339Nano),
		"registry_visible": registryVisible,
		"run_id":           session.RunID,
		"scopes_denied":    session.ScopesDenied,
		"scopes_granted":   session.ScopesGranted,
		"surface":          input.Surface,
		"workspace_id":     workspaceID,
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

func (rc *requestContext) registerWithIdentity(ctx context.Context, input registerInput, workspaceID string) (clients.AgentSession, error) {
	userToken := strings.TrimSpace(input.UserToken)
	if userToken == "" {
		userToken = rc.bearerToken()
	}
	if userToken != "" {
		session, err := downstream.CallOp(ctx, rc.deps.downstreamClients().Identity, "register", func(ctx context.Context) (clients.AgentSession, error) {
			return rc.deps.Identity.RegisterAgent(ctx, userToken, clients.RegisterAgentRequest{
				AgentType:    input.AgentType,
				Capabilities: input.Capabilities,
				Scopes:       input.Scopes,
				Surface:      input.Surface,
				TTLSeconds:   input.TTLSeconds,
			})
		})
		if err == nil {
			return session, nil
		}
		if !shouldFallbackToFederation(err) {
			return clients.AgentSession{}, err
		}
		provider := inferFederationProvider(input.AgentType)
		if provider == "" || workspaceID == "" {
			return clients.AgentSession{}, err
		}
		rc.logger.Info("identity registration returned unauthorized, retrying through federation", "agent_type", input.AgentType, "provider", provider)
		return downstream.CallOp(ctx, rc.deps.downstreamClients().Identity, "federate", func(ctx context.Context) (clients.AgentSession, error) {
			return rc.deps.Identity.FederateAgent(ctx, clients.FederateAgentRequest{
				AgentType:      input.AgentType,
				Capabilities:   input.Capabilities,
				ExternalToken:  userToken,
				OrganizationID: workspaceID,
				Provider:       provider,
				Scopes:         input.Scopes,
				Surface:        input.Surface,
				TTLSeconds:     input.TTLSeconds,
			})
		})
	}

	provider, externalToken := rc.configuredFederationCredential(input.AgentType)
	if provider == "" || externalToken == "" {
		return clients.AgentSession{}, fmt.Errorf("missing user token: provide user_token or set Authorization bearer header")
	}
	if workspaceID == "" {
		return clients.AgentSession{}, fmt.Errorf("missing workspace_id: provide workspace_id or set DEFAULT_WORKSPACE_ID for federation")
	}
	rc.logger.Info("registering agent through configured federation credential", "agent_type", input.AgentType, "provider", provider)
	return downstream.CallOp(ctx, rc.deps.downstreamClients().Identity, "federate", func(ctx context.Context) (clients.AgentSession, error) {
		return rc.deps.Identity.FederateAgent(ctx, clients.FederateAgentRequest{
			AgentType:      input.AgentType,
			Capabilities:   input.Capabilities,
			ExternalToken:  externalToken,
			OrganizationID: workspaceID,
			Provider:       provider,
			Scopes:         input.Scopes,
			Surface:        input.Surface,
			TTLSeconds:     input.TTLSeconds,
		})
	})
}

func shouldFallbackToFederation(err error) bool {
	var httpErr *clients.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}

func (rc *requestContext) configuredFederationCredential(agentType string) (string, string) {
	federation := rc.deps.Config.Federation
	if federation.DefaultWorkspaceID == "" {
		return "", ""
	}
	provider := inferFederationProvider(agentType)
	if provider != "" {
		if token := federationTokenForProvider(federation, provider); token != "" {
			return provider, token
		}
	}
	configured := configuredFederationProviders(federation)
	if len(configured) == 1 {
		for onlyProvider, token := range configured {
			return onlyProvider, token
		}
	}
	return "", ""
}

func inferFederationProvider(agentType string) string {
	normalized := strings.ToLower(strings.TrimSpace(agentType))
	switch {
	case strings.Contains(normalized, "claude"):
		return "anthropic"
	case strings.Contains(normalized, "codex"), strings.Contains(normalized, "openai"):
		return "openai"
	case strings.Contains(normalized, "copilot"), strings.Contains(normalized, "github"):
		return "github"
	case strings.Contains(normalized, "gemini"), strings.Contains(normalized, "google"):
		return "google"
	default:
		return ""
	}
}

func configuredFederationProviders(federation config.FederationConfig) map[string]string {
	configured := make(map[string]string, 4)
	if federation.AnthropicAPIKey != "" {
		configured["anthropic"] = federation.AnthropicAPIKey
	}
	if federation.OpenAIAPIKey != "" {
		configured["openai"] = federation.OpenAIAPIKey
	}
	if federation.GitHubToken != "" {
		configured["github"] = federation.GitHubToken
	}
	if federation.GoogleAccessToken != "" {
		configured["google"] = federation.GoogleAccessToken
	}
	return configured
}

func federationTokenForProvider(federation config.FederationConfig, provider string) string {
	switch provider {
	case "anthropic":
		return federation.AnthropicAPIKey
	case "openai":
		return federation.OpenAIAPIKey
	case "github":
		return federation.GitHubToken
	case "google":
		return federation.GoogleAccessToken
	default:
		return ""
	}
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
	if rc.isAnonymousRequest() {
		return rc.authenticationRequiredResult("renew a registered agent session"), heartbeatOutput{}, nil
	}
	sid := rc.mcpSessionID()
	state, ok := rc.deps.Sessions.Get(sid)
	if !ok {
		return nil, heartbeatOutput{}, fmt.Errorf("no active agent session — call evalops_register first")
	}

	session, err := downstream.CallOp(ctx, rc.deps.downstreamClients().Identity, "heartbeat", func(ctx context.Context) (clients.AgentSession, error) {
		return rc.deps.Identity.HeartbeatAgent(ctx, state.AgentToken, input.TTLSeconds)
	})
	if err != nil {
		rc.logger.Error("identity heartbeat failed", "agent_id", state.AgentID, "error", err)
		return nil, heartbeatOutput{}, fmt.Errorf("identity heartbeat failed: %w", err)
	}

	// Update stored state with the rotated token.
	state.AgentToken = session.AgentToken
	state.ExpiresAt = session.ExpiresAt
	rc.deps.Sessions.Set(sid, state)

	rc.deps.Metrics.Heartbeats.Inc()
	rc.deps.Events.Publish(ctx, state.WorkspaceID, "agent", state.AgentID, "heartbeat", map[string]any{
		"agent_id":     state.AgentID,
		"agent_type":   state.AgentType,
		"expires_at":   session.ExpiresAt.Format(time.RFC3339Nano),
		"renewed":      true,
		"run_id":       state.RunID,
		"surface":      state.Surface,
		"workspace_id": state.WorkspaceID,
	})
	rc.logger.Info("heartbeat completed", "agent_id", state.AgentID)

	// Heartbeat Agent Registry in background (best-effort, fail-open, fire-and-forget).
	// Launched after session renewal so the agent doesn't wait on agent-registry latency.
	if rc.deps.Registry != nil && rc.deps.Config.Registry.BaseURL != "" {
		// Capture immutable request values before the goroutine to prevent races.
		clonedMsg := &agentsv1.HeartbeatRequest{
			AgentId: state.AgentID,
			Status:  agentsv1.AgentStatus_AGENT_STATUS_ACTIVE,
			Surface: state.Surface,
		}
		agentToken := session.AgentToken
		workspaceID := state.WorkspaceID
		if !rc.deps.RunBackground("agent-registry.heartbeat", func() {
			// Detach from the request cancellation while still bounding the
			// downstream RPC lifetime.
			hbCtx, cancel := detachedContextWithTimeout(ctx, rc.deps.Config.Registry.RequestTimeout)
			defer cancel()

			hbReq := connect.NewRequest(clonedMsg)
			hbReq.Header().Set("Authorization", "Bearer "+agentToken)
			if workspaceID != "" {
				hbReq.Header().Set("X-Workspace-ID", workspaceID)
			}
			_, _ = downstream.CallOp(hbCtx, rc.deps.downstreamClients().Registry, "heartbeat", func(ctx context.Context) (*connect.Response[agentsv1.HeartbeatResponse], error) {
				return rc.deps.Registry.Heartbeat(ctx, hbReq)
			})
		}) {
			rc.logger.Warn("agent-registry heartbeat skipped: background task capacity exhausted", "agent_id", state.AgentID)
		}
	}

	return nil, heartbeatOutput{
		AgentID:   session.AgentID,
		ExpiresAt: session.ExpiresAt.Format("2006-01-02T15:04:05Z"),
		Renewed:   true,
	}, nil
}

func hasSessionCapacity(store SessionBackend, sessionID string, maxActive int) bool {
	if store == nil || maxActive <= 0 {
		return true
	}
	if _, ok := store.Get(sessionID); ok {
		return true
	}
	return store.ActiveCount() < maxActive
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
	if rc.isAnonymousRequest() {
		return rc.authenticationRequiredResult("deregister a registered agent"), deregisterOutput{}, nil
	}
	sid := rc.mcpSessionID()
	state, ok := rc.deps.Sessions.Get(sid)
	if !ok {
		return nil, deregisterOutput{}, fmt.Errorf("no active agent session")
	}

	agentID := state.AgentID

	// Deregister from Agent Registry first (best-effort, fail-open).
	if rc.deps.Registry != nil && rc.deps.Config.Registry.BaseURL != "" {
		deregReq := connect.NewRequest(&agentsv1.DeregisterRequest{Id: agentID})
		deregReq.Header().Set("Authorization", "Bearer "+state.AgentToken)
		if state.WorkspaceID != "" {
			deregReq.Header().Set("X-Workspace-ID", state.WorkspaceID)
		}
		_, _ = downstream.CallOp(ctx, rc.deps.downstreamClients().Registry, "deregister", func(ctx context.Context) (*connect.Response[agentsv1.DeregisterResponse], error) {
			return rc.deps.Registry.Deregister(ctx, deregReq)
		})
	}

	if _, err := downstream.CallOp(ctx, rc.deps.downstreamClients().Identity, "deregister", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, rc.deps.Identity.DeregisterAgent(ctx, state.AgentToken)
	}); err != nil {
		return nil, deregisterOutput{}, fmt.Errorf("identity deregister failed: %w", err)
	}

	rc.deps.Sessions.Delete(sid)
	rc.deps.Metrics.ActiveSessions.Set(float64(rc.deps.Sessions.ActiveCount()))
	rc.deps.Metrics.Deregistrations.Inc()
	rc.deps.Events.Publish(ctx, state.WorkspaceID, "agent", agentID, "deregistered", map[string]any{
		"agent_id":     agentID,
		"agent_type":   state.AgentType,
		"run_id":       state.RunID,
		"surface":      state.Surface,
		"workspace_id": state.WorkspaceID,
	})
	rc.logger.Info("agent deregistered", "agent_id", agentID)

	return nil, deregisterOutput{AgentID: agentID, Deregistered: true}, nil
}
