package agentmcp

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestSessionStore(t *testing.T) {
	store := NewSessionStore()

	_, ok := store.Get("missing")
	if ok {
		t.Fatal("expected false for missing session")
	}

	state := &SessionState{
		AgentID: "agent_abc", AgentToken: "tok_123", AgentType: "claude-code",
		ExpiresAt: time.Now().Add(time.Hour), RunID: "run_xyz", Surface: "cli",
	}
	store.Set("sess_1", state)

	got, ok := store.Get("sess_1")
	if !ok {
		t.Fatal("expected session to exist")
	}
	if got.AgentID != "agent_abc" {
		t.Fatalf("expected agent_abc, got %s", got.AgentID)
	}

	got.AgentToken = "tok_456"
	store.Set("sess_1", got)
	updated, _ := store.Get("sess_1")
	if updated.AgentToken != "tok_456" {
		t.Fatalf("expected tok_456, got %s", updated.AgentToken)
	}

	store.Delete("sess_1")
	if _, ok := store.Get("sess_1"); ok {
		t.Fatal("expected session to be deleted")
	}
}

func TestSessionStoreCopiesValues(t *testing.T) {
	store := NewSessionStore()
	original := &SessionState{
		AgentID:      "agent_abc",
		AgentToken:   "tok_123",
		Capabilities: []string{"read", "write"},
	}
	store.Set("sess_1", original)

	original.AgentToken = "mutated_before_get"
	original.Capabilities[0] = "mutated_capability"

	got, ok := store.Get("sess_1")
	if !ok {
		t.Fatal("expected session to exist")
	}
	if got.AgentToken != "tok_123" {
		t.Fatalf("expected stored token copy, got %q", got.AgentToken)
	}
	if got.Capabilities[0] != "read" {
		t.Fatalf("expected stored capability copy, got %#v", got.Capabilities)
	}

	got.AgentToken = "mutated_after_get"
	got.Capabilities[1] = "mutated_after_get"
	again, _ := store.Get("sess_1")
	if again.AgentToken != "tok_123" {
		t.Fatalf("expected get to return copy, got token %q", again.AgentToken)
	}
	if again.Capabilities[1] != "write" {
		t.Fatalf("expected capabilities copy, got %#v", again.Capabilities)
	}

	all := store.All()
	all["sess_1"].AgentToken = "mutated_from_all"
	final, _ := store.Get("sess_1")
	if final.AgentToken != "tok_123" {
		t.Fatalf("expected All to return copy, got token %q", final.AgentToken)
	}
}

func TestMemorySessionStoreSetIfUnderLimit(t *testing.T) {
	store := NewSessionStore()
	if !store.SetIfUnderLimit("sess_1", &SessionState{AgentID: "a1"}, 1) {
		t.Fatal("expected first session to fit within limit")
	}
	if store.SetIfUnderLimit("sess_2", &SessionState{AgentID: "a2"}, 1) {
		t.Fatal("expected second session to be rejected at limit")
	}
	if !store.SetIfUnderLimit("sess_1", &SessionState{AgentID: "a1_updated"}, 1) {
		t.Fatal("expected existing session update to be allowed at limit")
	}
	state, _ := store.Get("sess_1")
	if state.AgentID != "a1_updated" {
		t.Fatalf("expected updated existing session, got %q", state.AgentID)
	}
}

func TestSessionStoreActiveCount(t *testing.T) {
	store := NewSessionStore()
	if store.ActiveCount() != 0 {
		t.Fatalf("expected 0, got %d", store.ActiveCount())
	}

	store.Set("s1", &SessionState{AgentID: "a1"})
	store.Set("s2", &SessionState{AgentID: "a2"})
	if store.ActiveCount() != 2 {
		t.Fatalf("expected 2, got %d", store.ActiveCount())
	}

	store.Delete("s1")
	if store.ActiveCount() != 1 {
		t.Fatalf("expected 1, got %d", store.ActiveCount())
	}
}

func TestSessionStoreAll(t *testing.T) {
	store := NewSessionStore()
	store.Set("s1", &SessionState{AgentID: "a1"})
	store.Set("s2", &SessionState{AgentID: "a2"})

	all := store.All()
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
	if all["s1"].AgentID != "a1" || all["s2"].AgentID != "a2" {
		t.Fatal("unexpected snapshot contents")
	}
}

func TestSweepExpired(t *testing.T) {
	store := NewSessionStore()
	now := time.Now()

	store.Set("expired1", &SessionState{AgentID: "a1", ExpiresAt: now.Add(-time.Hour)})
	store.Set("expired2", &SessionState{AgentID: "a2", ExpiresAt: now.Add(-time.Minute)})
	store.Set("alive", &SessionState{AgentID: "a3", ExpiresAt: now.Add(time.Hour)})
	store.Set("no_expiry", &SessionState{AgentID: "a4"}) // zero ExpiresAt — never expires.

	removed := store.SweepExpired(now)
	if removed != 2 {
		t.Fatalf("expected 2 removed, got %d", removed)
	}
	if store.ActiveCount() != 2 {
		t.Fatalf("expected 2 remaining, got %d", store.ActiveCount())
	}
	if _, ok := store.Get("alive"); !ok {
		t.Fatal("expected alive session to remain")
	}
	if _, ok := store.Get("no_expiry"); !ok {
		t.Fatal("expected no_expiry session to remain")
	}
}

func TestRunExpiryReaper(t *testing.T) {
	store := NewSessionStore()
	store.Set("expired", &SessionState{AgentID: "a1", ExpiresAt: time.Now().Add(-time.Second)})
	store.Set("alive", &SessionState{AgentID: "a2", ExpiresAt: time.Now().Add(time.Hour)})

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	stop := RunExpiryReaper(store, 10*time.Millisecond, logger)
	defer stop()

	// Wait for at least one sweep.
	time.Sleep(50 * time.Millisecond)

	if store.ActiveCount() != 1 {
		t.Fatalf("expected 1 remaining after reaper, got %d", store.ActiveCount())
	}
	if _, ok := store.Get("alive"); !ok {
		t.Fatal("expected alive session to remain")
	}
}
