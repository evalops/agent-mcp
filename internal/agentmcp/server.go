package agentmcp

import (
	"log/slog"
	"net/http"
	"strings"

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
	Metrics    *Metrics
	Events     EventPublisher
	Logger     *slog.Logger
	Breakers   *Breakers
}

// Breakers holds circuit breakers wired into downstream call paths.
type Breakers struct {
	Governance *Breaker
}

func NewBreakers(cfg config.BreakerConfig) *Breakers {
	bc := BreakerConfig{
		FailureThreshold: cfg.FailureThreshold,
		ResetTimeout:     cfg.ResetTimeout,
	}
	return &Breakers{
		Governance: NewBreaker(bc),
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
	}, nil)

	sid := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	logger := deps.Logger.With("mcp_session_id", sid)

	rc := &requestContext{deps: deps, request: r, logger: logger}

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_register",
		Description: "Register this agent with EvalOps — creates identity session and registry presence in one call",
	}, rc.toolRegister)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_heartbeat",
		Description: "Heartbeat the agent session — rotates identity token and updates registry presence",
	}, rc.toolHeartbeat)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_deregister",
		Description: "Deregister the agent — revokes identity session and removes registry presence",
	}, rc.toolDeregister)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_check_action",
		Description: "Evaluate an action against governance policies — returns allow, deny, or require_approval with risk level",
	}, rc.toolCheckAction)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_check_approval",
		Description: "Check the status of a pending approval request",
	}, rc.toolCheckApproval)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "evalops_report_usage",
		Description: "Report token usage and cost to the metering service",
	}, rc.toolReportUsage)

	registerResources(server, deps, sid)

	logger.Info("mcp server created")
	return server
}
