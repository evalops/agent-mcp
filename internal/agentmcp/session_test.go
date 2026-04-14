package agentmcp

import (
	"testing"
	"time"
)

func TestSessionStore(t *testing.T) {
	store := NewSessionStore()

	// Get from empty store.
	_, ok := store.Get("missing")
	if ok {
		t.Fatal("expected false for missing session")
	}

	// Set and get.
	state := &SessionState{
		AgentID:    "agent_abc",
		AgentToken: "tok_123",
		AgentType:  "claude-code",
		ExpiresAt:  time.Now().Add(time.Hour),
		RunID:      "run_xyz",
		Surface:    "cli",
	}
	store.Set("sess_1", state)

	got, ok := store.Get("sess_1")
	if !ok {
		t.Fatal("expected session to exist")
	}
	if got.AgentID != "agent_abc" {
		t.Fatalf("expected agent_abc, got %s", got.AgentID)
	}
	if got.AgentType != "claude-code" {
		t.Fatalf("expected claude-code, got %s", got.AgentType)
	}

	// Update token (simulating heartbeat rotation).
	got.AgentToken = "tok_456"
	store.Set("sess_1", got)
	updated, _ := store.Get("sess_1")
	if updated.AgentToken != "tok_456" {
		t.Fatalf("expected tok_456, got %s", updated.AgentToken)
	}

	// Delete.
	store.Delete("sess_1")
	_, ok = store.Get("sess_1")
	if ok {
		t.Fatal("expected session to be deleted")
	}
}
