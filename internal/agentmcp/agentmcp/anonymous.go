package agentmcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/evalops/agent-mcp/internal/agentmcp/config"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	AnonymousSandboxHeader = "X-EvalOps-Anonymous-Sandbox"

	anonymousAuthMessage = "This action requires authentication. Set ANTHROPIC_API_KEY, OPENAI_API_KEY, GITHUB_TOKEN, or GH_TOKEN in your environment, or call evalops_register with a user_token."
)

func (rc *requestContext) currentSession() (*SessionState, bool) {
	sid := rc.mcpSessionID()
	if sid == "" {
		return nil, false
	}
	return rc.deps.Sessions.Get(sid)
}

func (rc *requestContext) isAnonymousRequest() bool {
	if rc.request == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(rc.request.Header.Get(AnonymousSandboxHeader)), "true") {
		return true
	}
	state, ok := rc.currentSession()
	return ok && state.IsAnonymous()
}

func (rc *requestContext) authenticationRequiredResult(action string) *mcpsdk.CallToolResult {
	message := anonymousAuthMessage
	if strings.TrimSpace(action) != "" {
		message = fmt.Sprintf("%s Authenticate to %s.", anonymousAuthMessage, strings.TrimSpace(action))
	}
	payload := map[string]any{
		"error":           "authentication_required",
		"message":         message,
		"upgrade_options": upgradeOptions(rc.deps.Config),
	}
	return structuredToolError(payload)
}

func (rc *requestContext) anonymousCheckActionResult(input checkActionInput) checkActionOutput {
	riskLevel := anonymousRiskLevel(input)
	return checkActionOutput{
		Decision:       "allow",
		DryRun:         true,
		Message:        "Anonymous sandbox dry-run only. Authenticate to enforce policies, create approvals, and persist audit history.",
		Reasons:        []string{"anonymous sandbox mode does not enforce governance decisions"},
		RiskLevel:      riskLevel,
		UpgradeOptions: upgradeOptions(rc.deps.Config),
	}
}

func anonymousRiskLevel(input checkActionInput) string {
	text := strings.ToLower(strings.TrimSpace(input.ActionType + " " + input.ActionPayload))
	switch {
	case containsAny(text,
		"bash", "shell", "exec", "sudo", "rm ", " delete", "drop ", "truncate", "deploy", "prod",
		"terraform apply", "kubectl", "curl ", "http", "api", "payment", "message", "email", "slack",
		"git push", "write_file", "database"):
		return "high"
	case containsAny(text, "edit", "write", "create", "install", "commit", "run", "patch", "apply"):
		return "medium"
	default:
		return "low"
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func upgradeOptions(cfg config.Config) []map[string]any {
	options := []map[string]any{
		{
			"env_vars": []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GITHUB_TOKEN", "GH_TOKEN", "GOOGLE_OAUTH_ACCESS_TOKEN"},
			"method":   "federation",
		},
	}
	if resourceURL := strings.TrimRight(strings.TrimSpace(cfg.ResourceURL), "/"); resourceURL != "" {
		options = append(options, map[string]any{
			"method": "oauth",
			"url":    resourceURL + "/.well-known/oauth-protected-resource",
		})
	}
	options = append(options, map[string]any{
		"header": "Authorization: Bearer pk_...",
		"method": "api_key",
	})
	return options
}

func structuredToolError(payload map[string]any) *mcpsdk.CallToolResult {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{"error":"internal_error","message":"failed to marshal tool error payload"}`)
	}
	return &mcpsdk.CallToolResult{
		Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: string(body)}},
		IsError:           true,
		StructuredContent: payload,
	}
}
