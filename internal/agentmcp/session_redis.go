package agentmcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

const sessionKeyPrefix = "agent-mcp:session:"

// RedisSessionStore persists session state in Redis for horizontal scaling
// and restart resilience. Keys auto-expire based on the session's ExpiresAt.
type RedisSessionStore struct {
	client     *redis.Client
	defaultTTL time.Duration
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

	s.client.Set(ctx, sessionKeyPrefix+sessionID, data, ttl)
}

func (s *RedisSessionStore) Delete(sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.client.Del(ctx, sessionKeyPrefix+sessionID)
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

// SweepExpired is a no-op for Redis — TTL-based expiry handles this automatically.
func (s *RedisSessionStore) SweepExpired(_ time.Time) int {
	return 0
}

func (s *RedisSessionStore) Close() error {
	return s.client.Close()
}
