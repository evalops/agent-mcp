package agentmcp

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const sessionKeyPrefix = "agent-mcp:session:"

// RedisSessionStore persists session state in Redis for horizontal scaling
// and restart resilience. Keys auto-expire based on the session's ExpiresAt.
//
// The local sync.Map tracks which sessions were created by this process so
// that shutdown cleanup only deregisters its own sessions instead of every
// session across all replicas.
type RedisSessionStore struct {
	client     *redis.Client
	defaultTTL time.Duration
	local      sync.Map // sessionID -> struct{} for sessions owned by this instance
}

func NewRedisSessionStore(client *redis.Client, defaultTTL time.Duration) *RedisSessionStore {
	if defaultTTL <= 0 {
		defaultTTL = time.Hour
	}
	return &RedisSessionStore{client: client, defaultTTL: defaultTTL}
}

func (s *RedisSessionStore) Get(sessionID string) (*SessionState, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	data, err := s.client.Get(ctx, sessionKeyPrefix+sessionID).Bytes()
	if err != nil {
		return nil, false
	}
	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, false
	}
	return &state, true
}

func (s *RedisSessionStore) Set(sessionID string, state *SessionState) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	data, err := json.Marshal(state)
	if err != nil {
		return
	}

	ttl := s.defaultTTL
	if !state.ExpiresAt.IsZero() {
		remaining := time.Until(state.ExpiresAt)
		if remaining > 0 {
			ttl = remaining
		}
	}

	existed := s.client.Exists(ctx, sessionKeyPrefix+sessionID).Val() > 0
	s.client.Set(ctx, sessionKeyPrefix+sessionID, data, ttl)
	// Only claim local ownership for new sessions. Heartbeats routed
	// to a different replica must not claim the session.
	if !existed {
		s.local.Store(sessionID, struct{}{})
	}
}

func (s *RedisSessionStore) Delete(sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.client.Del(ctx, sessionKeyPrefix+sessionID)
	s.local.Delete(sessionID)
}

func (s *RedisSessionStore) ActiveCount() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var count int
	iter := s.client.Scan(ctx, 0, sessionKeyPrefix+"*", 0).Iterator()
	for iter.Next(ctx) {
		count++
	}
	return count
}

func (s *RedisSessionStore) All() map[string]*SessionState {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	snapshot := make(map[string]*SessionState)
	iter := s.client.Scan(ctx, 0, sessionKeyPrefix+"*", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		sessionID := key[len(sessionKeyPrefix):]
		data, err := s.client.Get(ctx, key).Bytes()
		if err != nil {
			continue
		}
		var state SessionState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		snapshot[sessionID] = &state
	}
	return snapshot
}

// LocalSessions returns only sessions created by this process instance.
// Use this during shutdown to deregister only this replica's sessions
// instead of all sessions in Redis.
func (s *RedisSessionStore) LocalSessions() map[string]*SessionState {
	snapshot := make(map[string]*SessionState)
	s.local.Range(func(key, _ any) bool {
		sessionID, ok := key.(string)
		if !ok {
			return true
		}
		if state, found := s.Get(sessionID); found {
			snapshot[sessionID] = state
		}
		return true
	})
	return snapshot
}

// SweepExpired is a no-op for Redis — TTL-based expiry handles this automatically.
func (s *RedisSessionStore) SweepExpired(_ time.Time) int {
	return 0
}

func (s *RedisSessionStore) Close() error {
	return s.client.Close()
}
