package agentmcp

import (
	"log/slog"
	"sync"
	"time"
)

// SessionState holds per-MCP-session agent state. Token rotation on heartbeat
// is managed internally so the external agent never sees rotated tokens.
type SessionState struct {
	AgentID        string
	AgentToken     string
	AgentType      string
	Capabilities   []string
	ExpiresAt      time.Time
	OrganizationID string
	RunID          string
	Surface        string
	WorkspaceID    string
}

// SessionStore manages per-MCP-session state keyed by the Mcp-Session-Id header.
type SessionStore struct {
	mu    sync.RWMutex
	store map[string]*SessionState
}

func NewSessionStore() *SessionStore {
	return &SessionStore{store: make(map[string]*SessionState)}
}

func (s *SessionStore) Get(sessionID string) (*SessionState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.store[sessionID]
	return state, ok
}

func (s *SessionStore) Set(sessionID string, state *SessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[sessionID] = state
}

func (s *SessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.store, sessionID)
}

// ActiveCount returns the number of active sessions.
func (s *SessionStore) ActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.store)
}

// All returns a snapshot of all sessions. Used for graceful shutdown.
func (s *SessionStore) All() map[string]*SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := make(map[string]*SessionState, len(s.store))
	for k, v := range s.store {
		snapshot[k] = v
	}
	return snapshot
}

// SweepExpired removes sessions that have passed their ExpiresAt time.
// Returns the number of sessions removed.
func (s *SessionStore) SweepExpired(now time.Time) int {
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

// RunExpiryReaper starts a background goroutine that sweeps expired sessions.
// Returns a stop function.
func RunExpiryReaper(store *SessionStore, interval time.Duration, logger *slog.Logger) func() {
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
