package agentmcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/evalops/agent-mcp/internal/config"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReadAgentStatusNoSession(t *testing.T) {
	deps := &Deps{Sessions: NewSessionStore(), Config: config.Config{}}

	result, err := readAgentStatus(deps, "missing_session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Contents) != 1 {
		t.Fatal("expected 1 content block")
	}

	var data map[string]any
	json.Unmarshal([]byte(result.Contents[0].Text), &data)
	if data["registered"] != false {
		t.Fatal("expected registered=false")
	}
}

func TestReadAgentStatusWithSession(t *testing.T) {
	deps := &Deps{Sessions: NewSessionStore(), Config: config.Config{}}
	deps.Sessions.Set("sess_1", &SessionState{
		AgentID: "agent_abc", AgentType: "claude-code", Surface: "cli",
		Capabilities: []string{"shell", "git"}, WorkspaceID: "ws_1",
		RunID: "run_1", ExpiresAt: time.Now().Add(time.Hour),
	})

	result, err := readAgentStatus(deps, "sess_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]any
	json.Unmarshal([]byte(result.Contents[0].Text), &data)
	if data["registered"] != true {
		t.Fatal("expected registered=true")
	}
	if data["agent_id"] != "agent_abc" {
		t.Fatalf("expected agent_abc, got %v", data["agent_id"])
	}
	if data["agent_type"] != "claude-code" {
		t.Fatalf("expected claude-code, got %v", data["agent_type"])
	}
}

func TestReadApprovalHabitsNoService(t *testing.T) {
	deps := &Deps{Sessions: NewSessionStore(), Config: config.Config{}}

	result, err := readApprovalHabits(nil, deps, "sess_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]any
	json.Unmarshal([]byte(result.Contents[0].Text), &data)
	if data["available"] != false {
		t.Fatal("expected available=false")
	}
}

func TestReadOperatingRulesNoService(t *testing.T) {
	deps := &Deps{Sessions: NewSessionStore(), Config: config.Config{}}

	result, err := readOperatingRules(nil, deps, "sess_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]any
	json.Unmarshal([]byte(result.Contents[0].Text), &data)
	if data["available"] != false {
		t.Fatal("expected available=false")
	}
}

// Verify jsonResource produces valid output.
func TestJsonResource(t *testing.T) {
	result, err := jsonResource("test://uri", map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Contents) != 1 {
		t.Fatal("expected 1 content block")
	}
	if result.Contents[0].URI != "test://uri" {
		t.Fatalf("expected test://uri, got %s", result.Contents[0].URI)
	}
	if result.Contents[0].MIMEType != "application/json" {
		t.Fatalf("expected application/json, got %s", result.Contents[0].MIMEType)
	}

	var data map[string]string
	json.Unmarshal([]byte(result.Contents[0].Text), &data)
	if data["key"] != "value" {
		t.Fatalf("expected value, got %s", data["key"])
	}
}

// Suppress unused import.
var _ = (*mcpsdk.Server)(nil)
