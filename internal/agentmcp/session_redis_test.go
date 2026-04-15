package agentmcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type existsErrorHook struct{}

func (existsErrorHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (existsErrorHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "exists" {
			return errors.New("exists failed")
		}
		return next(ctx, cmd)
	}
}

func (existsErrorHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func newTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return client, mr
}

func TestRedisSessionStoreGetSet(t *testing.T) {
	client, _ := newTestRedis(t)

	store := NewRedisSessionStore(client, time.Hour)

	_, ok := store.Get("missing")
	if ok {
		t.Fatal("expected false for missing session")
	}

	state := &SessionState{
		AgentID:    "agent_redis",
		AgentToken: "tok_redis",
		AgentType:  "claude-code",
		ExpiresAt:  time.Now().Add(time.Hour),
		RunID:      "run_redis",
		Surface:    "cli",
	}
	store.Set("sess_r1", state)

	got, ok := store.Get("sess_r1")
	if !ok {
		t.Fatal("expected session to exist")
	}
	if got.AgentID != "agent_redis" {
		t.Fatalf("expected agent_redis, got %s", got.AgentID)
	}
	if got.AgentToken != "tok_redis" {
		t.Fatalf("expected tok_redis, got %s", got.AgentToken)
	}
}

func TestRedisSessionStoreDelete(t *testing.T) {
	client, _ := newTestRedis(t)

	store := NewRedisSessionStore(client, time.Hour)
	store.Set("sess_del", &SessionState{AgentID: "a1"})
	store.Delete("sess_del")

	_, ok := store.Get("sess_del")
	if ok {
		t.Fatal("expected session to be deleted")
	}
}

func TestRedisSessionStoreActiveCount(t *testing.T) {
	client, _ := newTestRedis(t)

	store := NewRedisSessionStore(client, time.Hour)
	store.Set("s1", &SessionState{AgentID: "a1"})
	store.Set("s2", &SessionState{AgentID: "a2"})
	store.Set("s3", &SessionState{AgentID: "a3"})

	if store.ActiveCount() != 3 {
		t.Fatalf("expected 3, got %d", store.ActiveCount())
	}
}

func TestRedisSessionStoreAll(t *testing.T) {
	client, _ := newTestRedis(t)

	store := NewRedisSessionStore(client, time.Hour)
	store.Set("s1", &SessionState{AgentID: "a1"})
	store.Set("s2", &SessionState{AgentID: "a2"})

	all := store.All()
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
}

func TestRedisSessionStoreTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	ttlClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer ttlClient.Close()

	store := NewRedisSessionStore(ttlClient, time.Hour)

	store.Set("short", &SessionState{
		AgentID:   "a1",
		ExpiresAt: time.Now().Add(2 * time.Second),
	})

	got, ok := store.Get("short")
	if !ok {
		t.Fatal("expected session to exist immediately")
	}
	if got.AgentID != "a1" {
		t.Fatalf("expected a1, got %s", got.AgentID)
	}

	mr.FastForward(3 * time.Second)

	_, ok = store.Get("short")
	if ok {
		t.Fatal("expected session to be expired by Redis TTL")
	}
}

func TestRedisSessionStoreSweepIsNoop(t *testing.T) {
	client, _ := newTestRedis(t)

	store := NewRedisSessionStore(client, time.Hour)
	removed := store.SweepExpired(time.Now())
	if removed != 0 {
		t.Fatal("sweep should be noop for redis store")
	}
}

func TestRedisSessionStoreClose(t *testing.T) {
	client, _ := newTestRedis(t)

	store := NewRedisSessionStore(client, time.Hour)
	if err := store.Close(); err != nil {
		t.Fatalf("close returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("expected closed client error, got %v", err)
	}
}

func TestRedisSessionStoreLocalSessions(t *testing.T) {
	client, _ := newTestRedis(t)

	store := NewRedisSessionStore(client, time.Hour)

	// Simulate sessions from another replica by writing directly to Redis,
	// bypassing the store's Set method so they are not tracked locally.
	ctx := context.Background()
	otherData := []byte(`{"agent_id":"other_agent","agent_token":"tok_other","agent_type":"claude-code","run_id":"run_other","surface":"cli"}`)
	client.Set(ctx, sessionKeyPrefix+"sess_other", otherData, time.Hour)

	// Create a session through this store instance — it should be tracked locally.
	store.Set("sess_local", &SessionState{
		AgentID:    "local_agent",
		AgentToken: "tok_local",
		AgentType:  "claude-code",
		RunID:      "run_local",
		Surface:    "cli",
	})

	// All() should return both sessions (all replicas).
	all := store.All()
	if len(all) != 2 {
		t.Fatalf("expected All() to return 2 sessions, got %d", len(all))
	}

	// LocalSessions() should return only the session created by this instance.
	local := store.LocalSessions()
	if len(local) != 1 {
		t.Fatalf("expected LocalSessions() to return 1 session, got %d", len(local))
	}
	if local["sess_local"] == nil {
		t.Fatal("expected sess_local in local sessions")
	}
	if local["sess_local"].AgentID != "local_agent" {
		t.Fatalf("expected local_agent, got %s", local["sess_local"].AgentID)
	}
}

func TestRedisSessionStoreDeleteRemovesLocal(t *testing.T) {
	client, _ := newTestRedis(t)

	store := NewRedisSessionStore(client, time.Hour)
	store.Set("sess_1", &SessionState{AgentID: "a1"})
	store.Set("sess_2", &SessionState{AgentID: "a2"})

	// Both should be tracked locally.
	if len(store.LocalSessions()) != 2 {
		t.Fatalf("expected 2 local sessions, got %d", len(store.LocalSessions()))
	}

	// Delete one — it should be removed from local tracking too.
	store.Delete("sess_1")
	local := store.LocalSessions()
	if len(local) != 1 {
		t.Fatalf("expected 1 local session after delete, got %d", len(local))
	}
	if local["sess_2"] == nil {
		t.Fatal("expected sess_2 to remain in local sessions")
	}
}

func TestRedisSessionStoreUpdateDoesNotClaimLocalOwnership(t *testing.T) {
	client, _ := newTestRedis(t)

	storeA := NewRedisSessionStore(client, time.Hour)
	storeB := NewRedisSessionStore(client, time.Hour)

	storeA.Set("sess_shared", &SessionState{
		AgentID:    "agent_shared",
		AgentToken: "tok_initial",
		AgentType:  "claude-code",
		RunID:      "run_shared",
		Surface:    "cli",
	})

	storeB.Set("sess_shared", &SessionState{
		AgentID:    "agent_shared",
		AgentToken: "tok_rotated",
		AgentType:  "claude-code",
		RunID:      "run_shared",
		Surface:    "cli",
	})

	if len(storeA.LocalSessions()) != 1 {
		t.Fatalf("expected storeA to keep 1 local session, got %d", len(storeA.LocalSessions()))
	}
	if len(storeB.LocalSessions()) != 0 {
		t.Fatalf("expected storeB to keep 0 local sessions after update, got %d", len(storeB.LocalSessions()))
	}
}

func TestRedisSessionStoreExistsErrorDoesNotClaimLocalOwnership(t *testing.T) {
	client, _ := newTestRedis(t)
	client.AddHook(existsErrorHook{})

	store := NewRedisSessionStore(client, time.Hour)
	store.Set("sess_exists_err", &SessionState{AgentID: "a1"})

	if got := countLocalTrackedSessions(store); got != 0 {
		t.Fatalf("expected 0 locally tracked sessions when EXISTS fails, got %d", got)
	}
}

func countLocalTrackedSessions(store *RedisSessionStore) int {
	count := 0
	store.local.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}
