package agentmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evalops/agent-mcp/internal/config"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func callResultMap(t *testing.T, result *mcpsdk.CallToolResult) map[string]any {
	t.Helper()
	if result == nil {
		t.Fatal("expected call tool result")
	}
	if mapped, ok := result.StructuredContent.(map[string]any); ok {
		return mapped
	}
	for _, content := range result.Content {
		text, ok := content.(*mcpsdk.TextContent)
		if !ok {
			continue
		}
		var mapped map[string]any
		if err := json.Unmarshal([]byte(text.Text), &mapped); err == nil {
			return mapped
		}
	}
	t.Fatalf("expected structured content map, got %#v", result)
	return nil
}

func TestAnonymousRegisterReturnsAuthenticationRequiredResult(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ResourceURL: "https://mcp.evalops.dev"},
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	deps.Sessions.Set("sess_anon", &SessionState{
		SessionType: SessionTypeAnonymous,
		ExpiresAt:   time.Now().Add(time.Hour),
		Surface:     "mcp",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_anon")
	req.Header.Set(AnonymousSandboxHeader, "true")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	result, out, err := rc.toolRegister(context.Background(), nil, registerInput{AgentType: "codex", Surface: "cli"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Registered {
		t.Fatalf("expected register output to remain false, got %#v", out)
	}
	if !result.IsError {
		t.Fatalf("expected tool error result, got %#v", result)
	}
	payload := callResultMap(t, result)
	if payload["error"] != "authentication_required" {
		t.Fatalf("expected authentication_required, got %#v", payload["error"])
	}
}

func TestAnonymousCheckActionReturnsDryRun(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ResourceURL: "https://mcp.evalops.dev"},
		Sessions: NewSessionStore(),
		Metrics:  NewTestMetrics(),
		Events:   NoopEventPublisher{},
		Breakers: NewBreakers(config.BreakerConfig{FailureThreshold: 5}),
		Logger:   testLogger,
	}
	deps.Sessions.Set("sess_anon", &SessionState{
		SessionType: SessionTypeAnonymous,
		ExpiresAt:   time.Now().Add(time.Hour),
		Surface:     "mcp",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "sess_anon")
	req.Header.Set(AnonymousSandboxHeader, "true")
	rc := &requestContext{deps: deps, request: req, logger: testLogger}

	_, out, err := rc.toolCheckAction(context.Background(), nil, checkActionInput{
		ActionType:    "Bash",
		ActionPayload: "rm -rf /tmp/work",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.DryRun {
		t.Fatalf("expected dry-run output, got %#v", out)
	}
	if out.Decision != "allow" {
		t.Fatalf("expected allow in dry-run mode, got %#v", out.Decision)
	}
	if out.RiskLevel != "high" {
		t.Fatalf("expected high dry-run risk, got %#v", out.RiskLevel)
	}
	if len(out.UpgradeOptions) == 0 {
		t.Fatalf("expected upgrade options, got %#v", out)
	}
}

func TestReadAgentStatusAnonymousSession(t *testing.T) {
	deps := &Deps{Sessions: NewSessionStore(), Config: config.Config{}}
	deps.Sessions.Set("sess_anon", &SessionState{
		SessionType: SessionTypeAnonymous,
		ExpiresAt:   time.Now().Add(time.Hour),
		Surface:     "mcp",
	})

	result, err := readAgentStatus(deps, "sess_anon")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &data); err != nil {
		t.Fatalf("decode resource payload: %v", err)
	}
	if data["anonymous"] != true {
		t.Fatalf("expected anonymous=true, got %#v", data)
	}
	if data["registered"] != false {
		t.Fatalf("expected registered=false, got %#v", data)
	}
}
