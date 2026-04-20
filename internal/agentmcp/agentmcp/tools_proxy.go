package agentmcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/evalops/agent-mcp/internal/agentmcp/config"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type proxyHeaderContextKey struct{}

type proxyToolSpec struct {
	namespace        string
	mcpName          string
	upstreamName     string
	endpoint         string
	description      string
	riskLevel        string
	costClass        string
	provenanceTag    string
	requiresApproval bool
	scopes           []string
}

func proxyToolCatalog(tools []config.ProxyToolConfig) []toolCatalogEntry {
	entries := make([]toolCatalogEntry, 0, len(tools))
	for _, raw := range tools {
		spec, ok := normalizeProxyTool(raw)
		if !ok {
			continue
		}
		entries = append(entries, spec.catalogEntry())
	}
	return entries
}

func registerProxyTools(server *mcpsdk.Server, rc *requestContext) {
	seen := make(map[string]struct{}, len(rc.deps.Config.ProxyTools))
	for _, raw := range rc.deps.Config.ProxyTools {
		spec, ok := normalizeProxyTool(raw)
		if !ok {
			continue
		}
		if _, exists := seen[spec.mcpName]; exists {
			rc.logger.Warn("skipping duplicate proxy MCP tool", "mcp_name", spec.mcpName, "namespace", spec.namespace)
			continue
		}
		seen[spec.mcpName] = struct{}{}
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:        spec.mcpName,
			Description: spec.description,
		}, rc.toolProxy(spec))
	}
}

func normalizeProxyTool(raw config.ProxyToolConfig) (proxyToolSpec, bool) {
	namespace := strings.ToLower(strings.TrimSpace(raw.Namespace))
	service, object, action, ok := splitToolNamespace(namespace)
	if !ok {
		return proxyToolSpec{}, false
	}
	mcpName := strings.TrimSpace(raw.MCPName)
	if mcpName == "" {
		mcpName = namespace
	}
	upstreamName := strings.TrimSpace(raw.UpstreamName)
	if upstreamName == "" {
		upstreamName = mcpName
	}
	description := strings.TrimSpace(raw.Description)
	if description == "" {
		description = fmt.Sprintf("Proxy %s through the configured MCP firewall endpoint.", namespace)
	}
	riskLevel := strings.TrimSpace(raw.RiskLevel)
	if riskLevel == "" {
		riskLevel = "medium"
	}
	costClass := strings.TrimSpace(raw.CostClass)
	if costClass == "" {
		costClass = "external"
	}
	provenanceTag := strings.TrimSpace(raw.ProvenanceTag)
	if provenanceTag == "" {
		provenanceTag = "agent-mcp:mcp-firewall:" + namespace
	}
	return proxyToolSpec{
		namespace:        namespace,
		mcpName:          mcpName,
		upstreamName:     upstreamName,
		endpoint:         strings.TrimSpace(raw.Endpoint),
		description:      description,
		riskLevel:        riskLevel,
		costClass:        costClass,
		provenanceTag:    provenanceTag,
		requiresApproval: raw.RequiresApproval,
		scopes:           append([]string(nil), raw.Scopes...),
	}, service != "" && object != "" && action != ""
}

func (spec proxyToolSpec) catalogEntry() toolCatalogEntry {
	service, object, action, _ := splitToolNamespace(spec.namespace)
	return toolCatalogEntry{
		action:           action,
		available:        true,
		costClass:        spec.costClass,
		description:      spec.description,
		invocationMode:   "proxied",
		mcpName:          spec.mcpName,
		name:             spec.namespace,
		object:           object,
		provenanceTag:    spec.provenanceTag,
		requiresApproval: spec.requiresApproval,
		riskLevel:        spec.riskLevel,
		scopes:           append([]string(nil), spec.scopes...),
		service:          service,
		source:           "mcp_firewall",
	}
}

func (rc *requestContext) toolProxy(spec proxyToolSpec) func(context.Context, *mcpsdk.CallToolRequest, map[string]any) (*mcpsdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, input map[string]any) (*mcpsdk.CallToolResult, any, error) {
		state, registered := rc.currentSession()
		if state != nil && state.IsAnonymous() {
			return rc.authenticationRequiredResult("invoke " + spec.namespace), nil, nil
		}
		if !registered {
			return rc.authenticationRequiredResult("invoke " + spec.namespace), nil, nil
		}
		if rc.deps.Governance != nil && rc.deps.Config.Governance.BaseURL != "" {
			evaluated, err := rc.evaluateToolCatalogEntry(ctx, state, spec.catalogEntry())
			if err != nil {
				return nil, nil, fmt.Errorf("evaluate proxy tool %s: %w", spec.namespace, err)
			}
			switch evaluated.Decision {
			case "deny":
				return nil, nil, fmt.Errorf("governance denied proxy tool %s: %s", spec.namespace, strings.Join(evaluated.Reasons, "; "))
			case "require_approval":
				return nil, nil, fmt.Errorf("governance requires approval for proxy tool %s: %s", spec.namespace, strings.Join(evaluated.Reasons, "; "))
			}
		}

		if input == nil {
			input = map[string]any{}
		}
		headers := rc.proxyForwardHeaders(req, state)
		proxyCtx := context.WithValue(ctx, proxyHeaderContextKey{}, headers)
		version := strings.TrimSpace(rc.deps.Config.Version)
		if version == "" {
			version = "dev"
		}
		client := mcpsdk.NewClient(&mcpsdk.Implementation{
			Name:    "evalops-agent-mcp-proxy",
			Version: version,
		}, &mcpsdk.ClientOptions{
			Logger: rc.logger,
		})
		session, err := client.Connect(proxyCtx, &mcpsdk.StreamableClientTransport{
			Endpoint:             spec.endpoint,
			HTTPClient:           newProxyHTTPClient(),
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("connect proxy upstream %s: %w", spec.namespace, err)
		}
		defer func() {
			if err := session.Close(); err != nil {
				rc.logger.Warn("proxy MCP session close failed", "namespace", spec.namespace, "error", err)
			}
		}()

		result, err := session.CallTool(proxyCtx, &mcpsdk.CallToolParams{
			Name:      spec.upstreamName,
			Arguments: input,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("call proxy upstream %s (%s): %w", spec.namespace, spec.upstreamName, err)
		}
		return result, nil, nil
	}
}

func (rc *requestContext) proxyForwardHeaders(req *mcpsdk.CallToolRequest, state *SessionState) http.Header {
	headers := make(http.Header)
	copyHeader := func(source http.Header, name string) {
		for _, value := range source.Values(name) {
			headers.Add(name, value)
		}
	}
	if req != nil && req.Extra != nil {
		for _, name := range []string{"Authorization", "X-Request-Id", "Traceparent", "Tracestate"} {
			copyHeader(req.Extra.Header, name)
		}
	}
	if rc.request != nil {
		for _, name := range []string{"Authorization", "X-Request-Id", "Traceparent", "Tracestate"} {
			if headers.Get(name) == "" {
				copyHeader(rc.request.Header, name)
			}
		}
	}
	if state != nil {
		if state.AgentToken != "" {
			headers.Set("Authorization", "Bearer "+state.AgentToken)
		}
		if state.WorkspaceID != "" {
			headers.Set("X-EvalOps-Workspace-Id", state.WorkspaceID)
		}
		if state.AgentID != "" {
			headers.Set("X-EvalOps-Agent-Id", state.AgentID)
		}
	}
	if sid := rc.mcpSessionID(); sid != "" {
		headers.Set("X-EvalOps-MCP-Session-Id", sid)
	}
	return headers
}

func newProxyHTTPClient() *http.Client {
	return &http.Client{Transport: proxyHeaderForwardingTransport{base: http.DefaultTransport}}
}

type proxyHeaderForwardingTransport struct {
	base http.RoundTripper
}

func (t proxyHeaderForwardingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	next := req.Clone(req.Context())
	if headers, ok := req.Context().Value(proxyHeaderContextKey{}).(http.Header); ok {
		for key, values := range headers {
			for _, value := range values {
				next.Header.Add(key, value)
			}
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(next)
}
