package agentmcp

import (
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
