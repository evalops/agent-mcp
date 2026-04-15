package agentmcp

import (
	"log/slog"
	"sync"
	"time"
)

// SessionState holds per-MCP-session agent state.
type SessionState struct {
	SessionType    string    `json:"session_type,omitempty"`
	AgentID        string    `json:"agent_id"`
	AgentToken     string    `json:"agent_token"`
	AgentType      string    `json:"agent_type"`
	Capabilities   []string  `json:"capabilities,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
	OrganizationID string    `json:"organization_id,omitempty"`
	RunID          string    `json:"run_id"`
	Surface        string    `json:"surface"`
	WorkspaceID    string    `json:"workspace_id,omitempty"`
}

const (
	SessionTypeAgent     = "agent"
	SessionTypeAnonymous = "anonymous"
)

func (s *SessionState) IsAnonymous() bool {
	return s != nil && s.SessionType == SessionTypeAnonymous
}

// SessionBackend is the interface for session persistence.
// Implemented by MemorySessionStore and RedisSessionStore.
type SessionBackend interface {
	Get(sessionID string) (*SessionState, bool)
	Set(sessionID string, state *SessionState)
	Delete(sessionID string)
	ActiveCount() int
	All() map[string]*SessionState
	SweepExpired(now time.Time) int
	Close() error
}

// MemorySessionStore is an in-memory session store for local development.
type MemorySessionStore struct {
	mu    sync.RWMutex
	store map[string]*SessionState
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{store: make(map[string]*SessionState)}
}

// NewSessionStore returns a MemorySessionStore. Kept for backward compatibility.
func NewSessionStore() *MemorySessionStore {
	return NewMemorySessionStore()
}

func (s *MemorySessionStore) Get(sessionID string) (*SessionState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.store[sessionID]
	return state, ok
}

func (s *MemorySessionStore) Set(sessionID string, state *SessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[sessionID] = state
}

func (s *MemorySessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.store, sessionID)
}

func (s *MemorySessionStore) ActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.store)
}

func (s *MemorySessionStore) All() map[string]*SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := make(map[string]*SessionState, len(s.store))
	for k, v := range s.store {
		snapshot[k] = v
	}
	return snapshot
}

func (s *MemorySessionStore) SweepExpired(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id, state := range s.store {
		if !state.ExpiresAt.IsZero() && state.ExpiresAt.Before(now) {
			delete(s.store, id)
			removed++
		}
	}
	return removed
}

// Close is a no-op for the in-memory store.
func (s *MemorySessionStore) Close() error {
	return nil
}

// RunExpiryReaper starts a background goroutine that sweeps expired sessions.
func RunExpiryReaper(store SessionBackend, interval time.Duration, logger *slog.Logger) func() {
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				ticker.Stop()
				return
			case <-ticker.C:
				removed := store.SweepExpired(time.Now())
				if removed > 0 {
					logger.Info("swept expired sessions", "removed", removed, "remaining", store.ActiveCount())
				}
			}
		}
	}()
	return func() { close(done) }
}
