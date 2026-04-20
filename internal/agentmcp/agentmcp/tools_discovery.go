package agentmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	governancev1 "github.com/evalops/proto/gen/go/governance/v1"
	"github.com/evalops/service-runtime/downstream"
	"github.com/evalops/service-runtime/resilience"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const toolNamespaceConvention = "<service>.<object>.<action>"

type listToolsInput struct {
	IncludeDenied   bool   `json:"include_denied,omitempty" jsonschema:"Include tools that governance denies for the current session"`
	NamespacePrefix string `json:"namespace_prefix,omitempty" jsonschema:"Optional namespace prefix filter such as github. or evalops.memory"`
}

type listToolsOutput struct {
	AggregationModel    string              `json:"aggregation_model"`
	FilteredBy          []string            `json:"filtered_by,omitempty"`
	NamespaceConvention string              `json:"namespace_convention"`
	Tools               []toolCatalogOutput `json:"tools"`
	Warnings            []string            `json:"warnings,omitempty"`
}

type toolCatalogOutput struct {
	Action           string   `json:"action"`
	Available        bool     `json:"available"`
	CostClass        string   `json:"cost_class"`
	Decision         string   `json:"decision,omitempty"`
	Description      string   `json:"description"`
	InvocationMode   string   `json:"invocation_mode"`
	MCPName          string   `json:"mcp_name"`
	Name             string   `json:"name"`
	Object           string   `json:"object"`
	ProvenanceTag    string   `json:"provenance_tag"`
	Reasons          []string `json:"reasons,omitempty"`
	RequiresApproval bool     `json:"requires_approval"`
	RiskLevel        string   `json:"risk_level"`
	Scopes           []string `json:"scopes,omitempty"`
	Service          string   `json:"service"`
	Source           string   `json:"source"`
}

type toolCatalogEntry struct {
	action           string
	available        bool
	costClass        string
	description      string
	invocationMode   string
	mcpName          string
	name             string
	object           string
	provenanceTag    string
	requiresApproval bool
	riskLevel        string
	scopes           []string
	service          string
	source           string
}

func (rc *requestContext) toolListTools(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	input listToolsInput,
) (*mcpsdk.CallToolResult, listToolsOutput, error) {
	state, registered := rc.currentSession()
	if state != nil && state.IsAnonymous() {
		registered = false
	}

	entries := mergeToolCatalogs(
		builtinToolCatalog(),
		proxyToolCatalog(rc.deps.Config.ProxyTools),
		sessionCapabilityCatalog(state),
	)

	filteredBy := []string{"platform_static_catalog"}
	warnings := make([]string, 0, 2)
	if registered && rc.deps.Governance != nil && rc.deps.Config.Governance.BaseURL != "" {
		filteredBy = append(filteredBy, "governance")
	} else {
		warnings = append(warnings, "governance filtering unavailable; returning static risk defaults")
	}
	if !registered {
		warnings = append(warnings, "no active registered session; call evalops_register to filter by the current organization")
	}

	prefix := strings.TrimSpace(input.NamespacePrefix)
	candidates := make([]toolCatalogEntry, 0, len(entries))
	evaluatedTools := make([]toolCatalogOutput, 0, len(entries))
	for _, entry := range entries {
		if prefix != "" && !strings.HasPrefix(entry.name, prefix) {
			continue
		}
		candidates = append(candidates, entry)
		evaluatedTools = append(evaluatedTools, entry.toOutput())
	}
	if registered && rc.deps.Governance != nil && rc.deps.Config.Governance.BaseURL != "" {
		rc.evaluateToolCatalogEntries(ctx, state, candidates, evaluatedTools)
	}

	tools := make([]toolCatalogOutput, 0, len(evaluatedTools))
	for _, tool := range evaluatedTools {
		if tool.Decision == "deny" && !input.IncludeDenied {
			continue
		}
		tools = append(tools, tool)
	}

	sort.SliceStable(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})

	return nil, listToolsOutput{
		AggregationModel:    "agent-mcp-owned tools are hosted in-process; configured integration tools are proxied through mcp-firewall; undeclared proxy gaps remain declared_only",
		FilteredBy:          filteredBy,
		NamespaceConvention: toolNamespaceConvention,
		Tools:               tools,
		Warnings:            warnings,
	}, nil
}

func (rc *requestContext) evaluateToolCatalogEntries(ctx context.Context, state *SessionState, entries []toolCatalogEntry, out []toolCatalogOutput) {
	var wg sync.WaitGroup
	for index := range entries {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			evaluated, err := rc.evaluateToolCatalogEntry(ctx, state, entries[index])
			if err != nil {
				out[index] = governanceDiscoveryErrorOutput(out[index], err)
				return
			}
			out[index] = evaluated
		}(index)
	}
	wg.Wait()
}

func governanceDiscoveryErrorOutput(tool toolCatalogOutput, err error) toolCatalogOutput {
	tool.Decision = "deny"
	tool.RiskLevel = "critical"
	if errors.Is(err, resilience.ErrCircuitOpen) {
		tool.Reasons = []string{"governance service unreachable (circuit breaker open) - hiding unavailable tool by default"}
	} else {
		tool.Reasons = []string{fmt.Sprintf("governance evaluation failed: %v", err)}
	}
	return tool
}

func mergeToolCatalogs(catalogs ...[]toolCatalogEntry) []toolCatalogEntry {
	var total int
	for _, catalog := range catalogs {
		total += len(catalog)
	}
	entries := make([]toolCatalogEntry, 0, total)
	seen := make(map[string]struct{}, total)
	for _, catalog := range catalogs {
		for _, entry := range catalog {
			if entry.name == "" {
				continue
			}
			if _, ok := seen[entry.name]; ok {
				continue
			}
			seen[entry.name] = struct{}{}
			entries = append(entries, entry)
		}
	}
	return entries
}

func (rc *requestContext) evaluateToolCatalogEntry(ctx context.Context, state *SessionState, entry toolCatalogEntry) (toolCatalogOutput, error) {
	out := entry.toOutput()
	if state == nil {
		return out, nil
	}

	payload, err := json.Marshal(map[string]string{
		"mcp_name":  entry.mcpName,
		"namespace": entry.name,
		"source":    entry.source,
	})
	if err != nil {
		return out, fmt.Errorf("encode tool catalog payload: %w", err)
	}

	req := connect.NewRequest(&governancev1.EvaluateActionRequest{
		WorkspaceId:   state.WorkspaceID,
		AgentId:       state.AgentID,
		ActionType:    "mcp_tool_discovery",
		ActionPayload: payload,
	})
	if state.AgentToken != "" {
		req.Header().Set("Authorization", "Bearer "+state.AgentToken)
	}

	resp, err := downstream.CallOp(ctx, rc.deps.downstreamClients().Governance, "evaluate_tool_discovery", func(ctx context.Context) (*connect.Response[governancev1.EvaluateActionResponse], error) {
		return rc.deps.Governance.EvaluateAction(ctx, req)
	})
	if err != nil {
		return out, err
	}
	if resp == nil || resp.Msg.GetEvaluation() == nil {
		out.Decision = "deny"
		out.RiskLevel = "critical"
		out.Reasons = []string{"governance evaluation missing"}
		return out, nil
	}

	eval := resp.Msg.GetEvaluation()
	out.Decision = decisionString(eval.GetDecision())
	out.RiskLevel = riskLevelString(eval.GetRiskLevel())
	out.Reasons = eval.GetReasons()
	out.RequiresApproval = out.RequiresApproval || out.Decision == "require_approval"
	return out, nil
}

func (entry toolCatalogEntry) toOutput() toolCatalogOutput {
	return toolCatalogOutput{
		Action:           entry.action,
		Available:        entry.available,
		CostClass:        entry.costClass,
		Description:      entry.description,
		InvocationMode:   entry.invocationMode,
		MCPName:          entry.mcpName,
		Name:             entry.name,
		Object:           entry.object,
		ProvenanceTag:    entry.provenanceTag,
		RequiresApproval: entry.requiresApproval,
		RiskLevel:        entry.riskLevel,
		Scopes:           append([]string(nil), entry.scopes...),
		Service:          entry.service,
		Source:           entry.source,
	}
}

func builtinToolCatalog() []toolCatalogEntry {
	return []toolCatalogEntry{
		{
			action:         "register",
			available:      true,
			costClass:      "control",
			description:    "Register this agent with EvalOps identity and agent-registry presence.",
			invocationMode: "hosted",
			mcpName:        "evalops_register",
			name:           "evalops.session.register",
			object:         "session",
			provenanceTag:  "agent-mcp:platform:session.register",
			riskLevel:      "low",
			scopes:         []string{"agent:register"},
			service:        "evalops",
			source:         "platform",
		},
		{
			action:         "heartbeat",
			available:      true,
			costClass:      "control",
			description:    "Renew the current EvalOps agent session and agent-registry presence.",
			invocationMode: "hosted",
			mcpName:        "evalops_heartbeat",
			name:           "evalops.session.heartbeat",
			object:         "session",
			provenanceTag:  "agent-mcp:platform:session.heartbeat",
			riskLevel:      "low",
			scopes:         []string{"agent:heartbeat"},
			service:        "evalops",
			source:         "platform",
		},
		{
			action:         "deregister",
			available:      true,
			costClass:      "control",
			description:    "Revoke the current EvalOps agent session and agent-registry presence.",
			invocationMode: "hosted",
			mcpName:        "evalops_deregister",
			name:           "evalops.session.deregister",
			object:         "session",
			provenanceTag:  "agent-mcp:platform:session.deregister",
			riskLevel:      "medium",
			scopes:         []string{"agent:register"},
			service:        "evalops",
			source:         "platform",
		},
		{
			action:         "evaluate",
			available:      true,
			costClass:      "control",
			description:    "Evaluate a proposed action against EvalOps governance policies.",
			invocationMode: "hosted",
			mcpName:        "evalops_check_action",
			name:           "evalops.governance.evaluate",
			object:         "governance",
			provenanceTag:  "agent-mcp:platform:governance.evaluate",
			riskLevel:      "low",
			scopes:         []string{"governance:evaluate"},
			service:        "evalops",
			source:         "platform",
		},
		{
			action:         "poll",
			available:      true,
			costClass:      "control",
			description:    "Check the state of an EvalOps approval request.",
			invocationMode: "hosted",
			mcpName:        "evalops_check_approval",
			name:           "evalops.approval.poll",
			object:         "approval",
			provenanceTag:  "agent-mcp:platform:approval.poll",
			riskLevel:      "low",
			scopes:         []string{"governance:evaluate"},
			service:        "evalops",
			source:         "platform",
		},
		{
			action:         "record",
			available:      true,
			costClass:      "metered",
			description:    "Record token usage and estimated cost for attribution.",
			invocationMode: "hosted",
			mcpName:        "evalops_report_usage",
			name:           "evalops.usage.record",
			object:         "usage",
			provenanceTag:  "agent-mcp:platform:usage.record",
			riskLevel:      "low",
			scopes:         []string{"meter:record"},
			service:        "evalops",
			source:         "platform",
		},
		{
			action:         "search",
			available:      true,
			costClass:      "read",
			description:    "Search EvalOps memory for prior facts and project context.",
			invocationMode: "hosted",
			mcpName:        "evalops_recall",
			name:           "evalops.memory.search",
			object:         "memory",
			provenanceTag:  "agent-mcp:platform:memory.search",
			riskLevel:      "medium",
			service:        "evalops",
			source:         "platform",
		},
		{
			action:         "store",
			available:      true,
			costClass:      "write",
			description:    "Store durable facts, decisions, and project context in EvalOps memory.",
			invocationMode: "hosted",
			mcpName:        "evalops_store_memory",
			name:           "evalops.memory.store",
			object:         "memory",
			provenanceTag:  "agent-mcp:platform:memory.store",
			riskLevel:      "medium",
			service:        "evalops",
			source:         "platform",
		},
		{
			action:           "create",
			available:        true,
			costClass:        "control",
			description:      "Create a headless EvalOps API key for CI/CD or automation.",
			invocationMode:   "hosted",
			mcpName:          "evalops_create_api_key",
			name:             "evalops.api_key.create",
			object:           "api_key",
			provenanceTag:    "agent-mcp:platform:api_key.create",
			requiresApproval: true,
			riskLevel:        "high",
			service:          "evalops",
			source:           "platform",
		},
		{
			action:         "list",
			available:      true,
			costClass:      "read",
			description:    "List EvalOps API keys visible to the current token.",
			invocationMode: "hosted",
			mcpName:        "evalops_list_api_keys",
			name:           "evalops.api_key.list",
			object:         "api_key",
			provenanceTag:  "agent-mcp:platform:api_key.list",
			riskLevel:      "medium",
			service:        "evalops",
			source:         "platform",
		},
		{
			action:         "list",
			available:      true,
			costClass:      "read",
			description:    "List EvalOps and integration tool capabilities available through agent-mcp.",
			invocationMode: "hosted",
			mcpName:        "evalops_list_tools",
			name:           "evalops.tool.list",
			object:         "tool",
			provenanceTag:  "agent-mcp:platform:tool.list",
			riskLevel:      "low",
			service:        "evalops",
			source:         "platform",
		},
	}
}

func sessionCapabilityCatalog(state *SessionState) []toolCatalogEntry {
	if state == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(state.Capabilities))
	entries := make([]toolCatalogEntry, 0, len(state.Capabilities))
	for _, capability := range state.Capabilities {
		name := normalizeCapabilityToolName(capability)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		service, object, action, ok := splitToolNamespace(name)
		if !ok {
			continue
		}
		entries = append(entries, toolCatalogEntry{
			action:         action,
			available:      false,
			costClass:      "unknown",
			description:    "Declared by the current agent session; requires an agent-mcp proxy handler before invocation.",
			invocationMode: "declared_only",
			mcpName:        "",
			name:           name,
			object:         object,
			provenanceTag:  "agent-mcp:session-capability:" + name,
			riskLevel:      "unknown",
			service:        service,
			source:         "session_capability",
		})
	}
	return entries
}

func normalizeCapabilityToolName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "tool:")
	value = strings.TrimPrefix(value, "mcp:")
	if strings.Count(value, ".") < 2 {
		return ""
	}
	return value
}

func splitToolNamespace(name string) (string, string, string, bool) {
	parts := strings.Split(name, ".")
	if len(parts) < 3 {
		return "", "", "", false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", "", "", false
		}
	}
	service := parts[0]
	action := parts[len(parts)-1]
	object := strings.Join(parts[1:len(parts)-1], ".")
	return service, object, action, true
}
