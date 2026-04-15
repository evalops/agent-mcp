package agentmcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/evalops/agent-mcp/internal/config"
	approvalsv1 "github.com/evalops/proto/gen/go/approvals/v1"
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
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &data); err != nil {
		t.Fatalf("decode resource payload: %v", err)
	}
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
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &data); err != nil {
		t.Fatalf("decode resource payload: %v", err)
	}
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

func TestReadHookRequirementsIncludesMinimumHookVersion(t *testing.T) {
	deps := &Deps{Sessions: NewSessionStore(), Config: config.Config{}}
	deps.Sessions.Set("sess_1", &SessionState{
		AgentID:        "agent_abc",
		Surface:        "cli",
		WorkspaceID:    "ws_1",
		OrganizationID: "org_1",
	})

	result, err := readHookRequirements(deps, "sess_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Contents) != 1 {
		t.Fatal("expected 1 content block")
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &data); err != nil {
		t.Fatalf("unmarshal instructions: %v", err)
	}
	if got := data["minimum_hook_version"]; got != minimumSupportedHookVersion {
		t.Fatalf("minimum_hook_version = %v, want %s", got, minimumSupportedHookVersion)
	}
	if got := data["hook_release_download_url"]; got != serviceRuntimeReleasesDownload {
		t.Fatalf("hook_release_download_url = %v, want %s", got, serviceRuntimeReleasesDownload)
	}
	if got := data["auto_approved_decisions_require_update"]; got != true {
		t.Fatalf("auto_approved_decisions_require_update = %v, want true", got)
	}
	if got := data["workspace_id"]; got != "ws_1" {
		t.Fatalf("workspace_id = %v, want ws_1", got)
	}
}

func TestReadApprovalHabitsNoService(t *testing.T) {
	deps := &Deps{Sessions: NewSessionStore(), Config: config.Config{}}

	result, err := readApprovalHabits(context.TODO(), deps, "sess_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &data); err != nil {
		t.Fatalf("decode resource payload: %v", err)
	}
	if data["available"] != false {
		t.Fatal("expected available=false")
	}
}

func TestReadApprovalHabitsUsesCache(t *testing.T) {
	deps := &Deps{
		Sessions:   NewSessionStore(),
		HabitCache: NewApprovalHabitsCache(),
		Config: config.Config{
			Approvals: config.ApprovalsConfig{BaseURL: "http://approvals.example.com"},
		},
	}
	deps.Sessions.Set("sess_1", &SessionState{WorkspaceID: "ws_1"})
	deps.HabitCache.Store("ws_1", []*approvalsv1.ApprovalHabit{{
		Pattern:               "chat:crm_update",
		ObservationCount:      5,
		ApprovedCount:         4,
		AutoApproveConfidence: 0.8,
	}})

	result, err := readApprovalHabits(context.TODO(), deps, "sess_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &data); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if data["available"] != true {
		t.Fatal("expected available=true")
	}
	habits, ok := data["habits"].([]any)
	if !ok || len(habits) != 1 {
		t.Fatalf("expected one cached habit, got %#v", data["habits"])
	}
	item, ok := habits[0].(map[string]any)
	if !ok {
		t.Fatalf("expected habit object, got %#v", habits[0])
	}
	if item["approved_count"] != float64(4) {
		t.Fatalf("expected approved_count 4, got %#v", item["approved_count"])
	}
}

func TestReadOperatingRulesNoService(t *testing.T) {
	deps := &Deps{Sessions: NewSessionStore(), Config: config.Config{}}

	result, err := readOperatingRules(context.TODO(), deps, "sess_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &data); err != nil {
		t.Fatalf("decode resource payload: %v", err)
	}
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
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &data); err != nil {
		t.Fatalf("decode resource payload: %v", err)
	}
	if data["key"] != "value" {
		t.Fatalf("expected value, got %s", data["key"])
	}
}

// Suppress unused import.
var _ = (*mcpsdk.Server)(nil)
