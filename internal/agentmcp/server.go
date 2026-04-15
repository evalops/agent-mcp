package agentmcp

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/evalops/agent-mcp/internal/clients"
	"github.com/evalops/agent-mcp/internal/config"
	agentsv1connect "github.com/evalops/proto/gen/go/agents/v1/agentsv1connect"
	approvalsv1connect "github.com/evalops/proto/gen/go/approvals/v1/approvalsv1connect"
	governancev1connect "github.com/evalops/proto/gen/go/governance/v1/governancev1connect"
	memoryv1connect "github.com/evalops/proto/gen/go/memory/v1/memoryv1connect"
	meterv1connect "github.com/evalops/proto/gen/go/meter/v1/meterv1connect"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Deps bundles all downstream service clients needed by the MCP tools.
type Deps struct {
	Identity   *clients.IdentityClient
	Registry   agentsv1connect.AgentServiceClient
	Governance governancev1connect.GovernanceServiceClient
	Approvals  approvalsv1connect.ApprovalServiceClient
	Meter      meterv1connect.MeterServiceClient
	Memory     memoryv1connect.MemoryServiceClient
	Config     config.Config
	Sessions   SessionBackend
	HabitCache *ApprovalHabitsCache
	Metrics    *Metrics
	Events     EventPublisher
	Logger     *slog.Logger
	Breakers   *Breakers

	downstreamsOnce sync.Once
	downstreams     *DownstreamClients
}

// Breakers holds circuit breakers wired into downstream call paths.
// Breakers holds circuit breakers for each downstream service.
type Breakers struct {
	Identity   *Breaker
	Registry   *Breaker
	Governance *Breaker
	Approvals  *Breaker
	Meter      *Breaker
	Memory     *Breaker
}

func NewBreakers(cfg config.BreakerConfig) *Breakers {
	bc := BreakerConfig{
		FailureThreshold: cfg.FailureThreshold,
		ResetTimeout:     cfg.ResetTimeout,
	}
	return &Breakers{
		Identity:   NewBreaker(bc),
		Registry:   NewBreaker(bc),
		Governance: NewBreaker(bc),
		Approvals:  NewBreaker(bc),
		Meter:      NewBreaker(bc),
		Memory:     NewBreaker(bc),
	}
}

// requestContext carries per-request state needed by MCP tool handlers.
type requestContext struct {
	deps    *Deps
	request *http.Request
	logger  *slog.Logger
}

func (rc *requestContext) mcpSessionID() string {
	if rc.request == nil {
		return ""
	}
	return strings.TrimSpace(rc.request.Header.Get("Mcp-Session-Id"))
}

func (rc *requestContext) bearerToken() string {
	auth := strings.TrimSpace(rc.request.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
}

// NewHandler returns an http.Handler that serves the unified EvalOps agent MCP server.
func NewHandler(deps *Deps) http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(r *http.Request) *mcpsdk.Server {
		return serverForRequest(deps, r)
	}, nil)
}

func serverForRequest(deps *Deps, r *http.Request) *mcpsdk.Server {
	version := strings.TrimSpace(deps.Config.Version)
	if version == "" {
		version = "dev"
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "evalops-agent-mcp",
		Version: version,
	}, &mcpsdk.ServerOptions{
		Instructions: "EvalOps agent governance server. Read the evalops://agent/instructions resource for the full integration protocol \u2014 session lifecycle, governance checks, and usage reporting.",
	})

	sid := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	logger := deps.Logger.With("mcp_session_id", sid)

	rc := &requestContext{deps: deps, request: r, logger: logger}

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_register",
		Description: "Register this agent with EvalOps \u2014 creates identity session and registry presence in one call. Call this at the start of every session before using any other EvalOps tools",
	}, rc.toolRegister)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_heartbeat",
		Description: "Heartbeat the agent session \u2014 rotates identity token and updates registry presence. Call this every 60 seconds to maintain session liveness",
	}, rc.toolHeartbeat)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_deregister",
		Description: "Deregister the agent \u2014 revokes identity session and removes registry presence",
	}, rc.toolDeregister)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_check_action",
		Description: "Evaluate an action against governance policies \u2014 returns allow, deny, or require_approval with risk level. Call this BEFORE executing any tool that modifies files, runs shell commands, sends messages, or accesses external APIs",
	}, rc.toolCheckAction)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_check_approval",
		Description: "Check the status of a pending approval request",
	}, rc.toolCheckApproval)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_report_usage",
		Description: "Report token usage and cost to the metering service. Call this after each LLM inference call to enable cost attribution",
	}, rc.toolReportUsage)

	registerResources(server, deps, sid)

	logger.Info("mcp server created")
	return server
}
