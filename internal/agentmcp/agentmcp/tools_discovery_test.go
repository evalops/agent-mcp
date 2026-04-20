package agentmcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	governancev1 "github.com/evalops/proto/gen/go/governance/v1"
	"github.com/evalops/proto/gen/go/governance/v1/governancev1connect"
	"github.com/evalops/agent-mcp/internal/agentmcp/clients"
	"github.com/evalops/agent-mcp/internal/agentmcp/config"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolListToolsReturnsPlatformCatalog(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ServiceName: "test", Version: "test"},
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	rc := &requestContext{deps: deps, request: httptest.NewRequest(http.MethodPost, "/mcp", nil), logger: testLogger}

	_, out, err := rc.toolListTools(context.Background(), nil, listToolsInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.NamespaceConvention != toolNamespaceConvention {
		t.Fatalf("NamespaceConvention = %q", out.NamespaceConvention)
	}
	register := findCatalogTool(out.Tools, "evalops.session.register")
	if register == nil {
		t.Fatalf("expected evalops.session.register in catalog, got %#v", out.Tools)
	}
	if register.MCPName != "evalops_register" || !register.Available || register.InvocationMode != "hosted" {
		t.Fatalf("unexpected register catalog entry: %#v", register)
	}
	listTools := findCatalogTool(out.Tools, "evalops.tool.list")
	if listTools == nil || listTools.MCPName != "evalops_list_tools" {
		t.Fatalf("expected evalops.tool.list self-description, got %#v", listTools)
	}
	if len(out.Warnings) == 0 {
		t.Fatal("expected warning when governance/session filtering is unavailable")
	}
}

func TestToolListToolsIncludesDeclaredSessionCapabilities(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ServiceName: "test", Version: "test"},
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	deps.Sessions.Set("sess_1", &SessionState{
		AgentID:      "agent_1",
		AgentToken:   "tok_1",
		Capabilities: []string{"TOOL:GitHub.PR.Search", "shell", "github.pr.search"},
		WorkspaceID:  "ws_1",
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolListTools(context.Background(), nil, listToolsInput{NamespacePrefix: "github."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("expected one github tool, got %#v", out.Tools)
	}
	tool := out.Tools[0]
	if tool.Name != "github.pr.search" || tool.Service != "github" || tool.Object != "pr" || tool.Action != "search" {
		t.Fatalf("unexpected namespaced tool: %#v", tool)
	}
	if tool.Available || tool.InvocationMode != "declared_only" {
		t.Fatalf("declared session tools must not be marked invocable: %#v", tool)
	}
}

func TestToolListToolsMarksConfiguredProxyCapabilitiesInvocable(t *testing.T) {
	deps := &Deps{
		Config: config.Config{
			ServiceName: "test",
			Version:     "test",
			ProxyTools: []config.ProxyToolConfig{
				{
					Namespace:     "github.pr.search",
					Endpoint:      "https://mcp-firewall.example/mcp",
					MCPName:       "github_pr_search",
					UpstreamName:  "search_pull_requests",
					Description:   "Search GitHub pull requests through mcp-firewall.",
					RiskLevel:     "medium",
					CostClass:     "read",
					Scopes:        []string{"github:read"},
					ProvenanceTag: "agent-mcp:mcp-firewall:github.pr.search",
				},
			},
		},
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	deps.Sessions.Set("sess_1", &SessionState{
		AgentID:      "agent_1",
		AgentToken:   "tok_1",
		Capabilities: []string{"tool:github.pr.search"},
		WorkspaceID:  "ws_1",
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolListTools(context.Background(), nil, listToolsInput{NamespacePrefix: "github."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("expected one github tool, got %#v", out.Tools)
	}
	tool := out.Tools[0]
	if tool.Name != "github.pr.search" || tool.MCPName != "github_pr_search" {
		t.Fatalf("unexpected proxy catalog identity: %#v", tool)
	}
	if !tool.Available || tool.InvocationMode != "proxied" || tool.Source != "mcp_firewall" {
		t.Fatalf("proxy tool must be marked invocable: %#v", tool)
	}
	if tool.CostClass != "read" || tool.RiskLevel != "medium" || len(tool.Scopes) != 1 || tool.Scopes[0] != "github:read" {
		t.Fatalf("proxy metadata not preserved: %#v", tool)
	}
}

func TestToolProxyForwardsToConfiguredMCPUpstream(t *testing.T) {
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "upstream", Version: "test"}, nil)
	mcpsdk.AddTool(upstream, &mcpsdk.Tool{
		Name:        "search_pull_requests",
		Description: "Search pull requests",
	}, func(_ context.Context, req *mcpsdk.CallToolRequest, args map[string]any) (*mcpsdk.CallToolResult, map[string]any, error) {
		return nil, map[string]any{
			"authorization":  req.Extra.Header.Get("Authorization"),
			"mcp_session_id": req.Extra.Header.Get("X-EvalOps-MCP-Session-Id"),
			"workspace_id":   req.Extra.Header.Get("X-EvalOps-Workspace-Id"),
			"agent_id":       req.Extra.Header.Get("X-EvalOps-Agent-Id"),
			"query":          args["query"],
		}, nil
	})
	upstreamHTTP := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(_ *http.Request) *mcpsdk.Server {
		return upstream
	}, nil))
	defer upstreamHTTP.Close()

	raw := config.ProxyToolConfig{
		Namespace:    "github.pr.search",
		Endpoint:     upstreamHTTP.URL,
		MCPName:      "github_pr_search",
		UpstreamName: "search_pull_requests",
	}
	spec, ok := normalizeProxyTool(raw)
	if !ok {
		t.Fatalf("failed to normalize proxy tool")
	}
	deps := &Deps{
		Config:   config.Config{ServiceName: "test", Version: "test", ProxyTools: []config.ProxyToolConfig{raw}},
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	deps.Sessions.Set("sess_1", &SessionState{
		AgentID:     "agent_1",
		AgentToken:  "tok_1",
		WorkspaceID: "ws_1",
	})
	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	httpReq.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: httpReq, logger: testLogger}

	result, _, err := rc.toolProxy(spec)(context.Background(), &mcpsdk.CallToolRequest{
		Extra: &mcpsdk.RequestExtra{Header: http.Header{"X-Request-Id": []string{"req_1"}}},
	}, map[string]any{"query": "is:open"})
	if err != nil {
		t.Fatalf("toolProxy() error = %v", err)
	}
	var got struct {
		Authorization string `json:"authorization"`
		MCPSessionID  string `json:"mcp_session_id"`
		WorkspaceID   string `json:"workspace_id"`
		AgentID       string `json:"agent_id"`
		Query         string `json:"query"`
	}
	content, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal proxy structured content: %v", err)
	}
	if err := json.Unmarshal(content, &got); err != nil {
		t.Fatalf("decode proxy result: %v", err)
	}
	if got.Authorization != "Bearer tok_1" || got.MCPSessionID != "sess_1" {
		t.Fatalf("proxy did not forward auth/session headers: %#v", got)
	}
	if got.WorkspaceID != "ws_1" || got.AgentID != "agent_1" || got.Query != "is:open" {
		t.Fatalf("proxy did not forward session metadata or args: %#v", got)
	}
}

func TestToolListToolsFiltersGovernanceDeniedTools(t *testing.T) {
	mockGov := &recordingDiscoveryGovernance{}
	_, handler := governancev1connect.NewGovernanceServiceHandler(mockGov)
	govSrv := httptest.NewServer(handler)
	defer govSrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Governance: config.GovernanceConfig{BaseURL: govSrv.URL},
		},
		Governance: clients.NewGovernanceClient(govSrv.URL, govSrv.Client()),
		Sessions:   NewSessionStore(),
		Metrics:    NewTestMetrics(),
		Events:     NoopEventPublisher{},
		Breakers:   NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:     testLogger,
	}
	deps.Sessions.Set("sess_1", &SessionState{
		AgentID: "agent_1", AgentToken: "tok_1", WorkspaceID: "ws_1", Surface: "cli",
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolListTools(context.Background(), nil, listToolsInput{NamespacePrefix: "evalops.memory"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findCatalogTool(out.Tools, "evalops.memory.store") != nil {
		t.Fatalf("expected governance-denied memory store to be hidden, got %#v", out.Tools)
	}
	if search := findCatalogTool(out.Tools, "evalops.memory.search"); search == nil || search.RiskLevel != "low" {
		t.Fatalf("expected allowed memory search with governance risk, got %#v", search)
	}
	if mockGov.callCount() == 0 {
		t.Fatal("expected governance evaluation calls")
	}

	_, withDenied, err := rc.toolListTools(context.Background(), nil, listToolsInput{
		IncludeDenied:   true,
		NamespacePrefix: "evalops.memory.store",
	})
	if err != nil {
		t.Fatalf("unexpected include_denied error: %v", err)
	}
	store := findCatalogTool(withDenied.Tools, "evalops.memory.store")
	if store == nil || store.Decision != "deny" || store.RiskLevel != "critical" {
		t.Fatalf("expected denied memory store to include governance decision, got %#v", store)
	}
}

func TestToolListToolsPreservesStaticApprovalRequirement(t *testing.T) {
	mockGov := &recordingDiscoveryGovernance{}
	_, handler := governancev1connect.NewGovernanceServiceHandler(mockGov)
	govSrv := httptest.NewServer(handler)
	defer govSrv.Close()

	deps := &Deps{
		Config: config.Config{
			ServiceName: "test", Version: "test",
			Governance: config.GovernanceConfig{BaseURL: govSrv.URL},
		},
		Governance: clients.NewGovernanceClient(govSrv.URL, govSrv.Client()),
		Sessions:   NewSessionStore(),
		Metrics:    NewTestMetrics(),
		Events:     NoopEventPublisher{},
		Breakers:   NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:     testLogger,
	}
	deps.Sessions.Set("sess_1", &SessionState{
		AgentID: "agent_1", AgentToken: "tok_1", WorkspaceID: "ws_1", Surface: "conductor",
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_1")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolListTools(context.Background(), nil, listToolsInput{NamespacePrefix: "evalops.api_key.create"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tool := findCatalogTool(out.Tools, "evalops.api_key.create")
	if tool == nil {
		t.Fatalf("expected api key create tool, got %#v", out.Tools)
	}
	if tool.Decision != "allow" {
		t.Fatalf("Decision = %q, want allow: %#v", tool.Decision, tool)
	}
	if !tool.RequiresApproval {
		t.Fatalf("RequiresApproval = false, want static approval requirement preserved: %#v", tool)
	}
}

func TestNormalizeCapabilityToolNameStripsCaseInsensitivePrefixes(t *testing.T) {
	tests := map[string]string{
		"TOOL:GitHub.PR.Search":        "github.pr.search",
		" MCP:EvalOps.Memory.Store ":   "evalops.memory.store",
		"evalops.Governance.Evaluate":  "evalops.governance.evaluate",
		"TOOL:missing_namespace_parts": "",
	}
	for value, want := range tests {
		if got := normalizeCapabilityToolName(value); got != want {
			t.Fatalf("normalizeCapabilityToolName(%q) = %q, want %q", value, got, want)
		}
	}
}

type recordingDiscoveryGovernance struct {
	governancev1connect.UnimplementedGovernanceServiceHandler
	mu    sync.Mutex
	calls int
}

func (g *recordingDiscoveryGovernance) EvaluateAction(_ context.Context, req *connect.Request[governancev1.EvaluateActionRequest]) (*connect.Response[governancev1.EvaluateActionResponse], error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	var payload struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(req.Msg.GetActionPayload(), &payload); err != nil {
		return nil, err
	}
	if req.Msg.GetActionType() != "mcp_tool_discovery" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unexpected action type"))
	}
	if strings.TrimSpace(req.Msg.GetWorkspaceId()) != "ws_1" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unexpected workspace"))
	}
	if payload.Namespace == "evalops.memory.store" {
		return connect.NewResponse(&governancev1.EvaluateActionResponse{
			Evaluation: &governancev1.ActionEvaluation{
				Decision:  governancev1.ActionDecision_ACTION_DECISION_DENY,
				RiskLevel: governancev1.RiskLevel_RISK_LEVEL_CRITICAL,
				Reasons:   []string{"memory writes disabled"},
			},
		}), nil
	}
	return connect.NewResponse(&governancev1.EvaluateActionResponse{
		Evaluation: &governancev1.ActionEvaluation{
			Decision:  governancev1.ActionDecision_ACTION_DECISION_ALLOW,
			RiskLevel: governancev1.RiskLevel_RISK_LEVEL_LOW,
		},
	}), nil
}

func (g *recordingDiscoveryGovernance) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func findCatalogTool(tools []toolCatalogOutput, name string) *toolCatalogOutput {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}
