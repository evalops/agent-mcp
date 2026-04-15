package agentmcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/evalops/service-runtime/resilience"
)

var errBreakerFailure = errors.New("breaker failure")

func TestBreakerStartsClosed(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 3, ResetTimeout: 100 * time.Millisecond})
	if b.State() != BreakerClosed {
		t.Fatalf("expected closed, got %s", b.State())
	}
}

func TestBreakerTripsAfterThreshold(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 3, ResetTimeout: 100 * time.Millisecond})

	for i := 0; i < 2; i++ {
		if err := b.Do(context.Background(), func(context.Context) error { return errBreakerFailure }); !errors.Is(err, errBreakerFailure) {
			t.Fatalf("unexpected error after failure %d: %v", i+1, err)
		}
	}
	if b.State() != BreakerClosed {
		t.Fatalf("expected closed, got %s", b.State())
	}

	if err := b.Do(context.Background(), func(context.Context) error { return errBreakerFailure }); !errors.Is(err, errBreakerFailure) {
		t.Fatalf("unexpected threshold failure: %v", err)
	}
	if b.State() != BreakerOpen {
		t.Fatalf("expected open after 3 failures, got %s", b.State())
	}
}

func TestBreakerTransitionsToHalfOpen(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1, ResetTimeout: 10 * time.Millisecond})

	_ = b.Do(context.Background(), func(context.Context) error { return errBreakerFailure })
	if b.State() != BreakerOpen {
		t.Fatalf("expected open, got %s", b.State())
	}

	time.Sleep(20 * time.Millisecond)

	if b.State() != BreakerHalfOpen {
		t.Fatalf("expected half-open after reset timeout, got %s", b.State())
	}
}

func TestBreakerHalfOpenSuccess(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1, ResetTimeout: 10 * time.Millisecond})

	_ = b.Do(context.Background(), func(context.Context) error { return errBreakerFailure })
	time.Sleep(20 * time.Millisecond)

	if err := b.Do(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("expected successful half-open probe, got %v", err)
	}
	if b.State() != BreakerClosed {
		t.Fatalf("expected closed after half-open success, got %s", b.State())
	}
}

func TestBreakerHalfOpenFailure(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1, ResetTimeout: 10 * time.Millisecond})

	_ = b.Do(context.Background(), func(context.Context) error { return errBreakerFailure })
	time.Sleep(20 * time.Millisecond)

	if err := b.Do(context.Background(), func(context.Context) error { return errBreakerFailure }); !errors.Is(err, errBreakerFailure) {
		t.Fatalf("expected failing half-open probe, got %v", err)
	}
	if b.State() != BreakerOpen {
		t.Fatalf("expected open after half-open failure, got %s", b.State())
	}
}

func TestBreakerStateString(t *testing.T) {
	tests := []struct {
		state BreakerState
		want  string
	}{
		{BreakerClosed, "closed"},
		{BreakerOpen, "open"},
		{BreakerHalfOpen, "half-open"},
		{BreakerState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("state.String() = %q, want %q", got, tt.want)
		}
	}
}

func TestBreakerRejectsWhenOpen(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1, ResetTimeout: time.Hour})

	_ = b.Do(context.Background(), func(context.Context) error { return errBreakerFailure })
	err := b.Do(context.Background(), func(context.Context) error {
		t.Fatal("expected open breaker to reject the call")
		return nil
	})
	if !errors.Is(err, resilience.ErrCircuitOpen) {
		t.Fatalf("expected circuit open error, got %v", err)
	}
}
