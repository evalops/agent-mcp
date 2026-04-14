package agentmcp

import (
	"context"
	"testing"

	"github.com/evalops/agent-mcp/internal/config"
)

func TestReportUsageNoMeter(t *testing.T) {
	deps := &Deps{
		Config:   config.Config{ServiceName: "test", Version: "test"},
		Sessions: NewSessionStore(),
	}
	rc := &requestContext{deps: deps, request: nil}

	_, out, err := rc.toolReportUsage(context.Background(), nil, reportUsageInput{
		Model:        "claude-sonnet-4-6",
		InputTokens:  1000,
		OutputTokens: 500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Recorded {
		t.Fatal("expected recorded=false when meter not configured")
	}
}
