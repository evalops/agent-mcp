package agentmcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

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
