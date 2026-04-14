package agentmcp

import (
	"testing"
	"time"
)

func TestBreakerStartsClosed(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 3, ResetTimeout: 100 * time.Millisecond})
	if b.State() != BreakerClosed {
		t.Fatalf("expected closed, got %s", b.State())
	}
	if !b.Allow() {
		t.Fatal("expected allow when closed")
	}
}

func TestBreakerOpensAfterThreshold(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 3, ResetTimeout: 100 * time.Millisecond})

	b.RecordFailure()
	b.RecordFailure()
	if b.State() != BreakerClosed {
		t.Fatal("should still be closed after 2 failures")
	}

	b.RecordFailure()
	if b.State() != BreakerOpen {
		t.Fatalf("expected open after 3 failures, got %s", b.State())
	}
	if b.Allow() {
		t.Fatal("expected deny when open")
	}
}

func TestBreakerResetsOnSuccess(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 2, ResetTimeout: 100 * time.Millisecond})

	b.RecordFailure()
	b.RecordSuccess()
	if b.State() != BreakerClosed {
		t.Fatal("expected closed after success")
	}

	// Should need full threshold again.
	b.RecordFailure()
	if b.State() != BreakerClosed {
		t.Fatal("expected closed after 1 failure (counter reset)")
	}
}

func TestBreakerTransitionsToHalfOpen(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1, ResetTimeout: 10 * time.Millisecond})

	b.RecordFailure()
	if !b.Allow() {
		// Still in open state, should not allow yet.
	}

	time.Sleep(20 * time.Millisecond)

	// After reset timeout, Allow() transitions to half-open and permits the probe.
	if !b.Allow() {
		t.Fatal("expected allow on half-open probe after reset timeout")
	}
	if b.State() != BreakerHalfOpen {
		t.Fatalf("expected half_open, got %s", b.State())
	}
}

func TestBreakerStateDoesNotConsumeHalfOpenProbe(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1, ResetTimeout: 10 * time.Millisecond})

	b.RecordFailure()
	time.Sleep(20 * time.Millisecond)

	if b.State() != BreakerOpen {
		t.Fatalf("expected open before probe, got %s", b.State())
	}
	if !b.Allow() {
		t.Fatal("expected first probe after timeout to be allowed")
	}
	if b.State() != BreakerHalfOpen {
		t.Fatalf("expected half_open after probe, got %s", b.State())
	}
}

func TestBreakerHalfOpenSuccess(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1, ResetTimeout: 10 * time.Millisecond})

	b.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	b.Allow() // transition to half-open

	b.RecordSuccess()
	if b.State() != BreakerClosed {
		t.Fatalf("expected closed after half-open success, got %s", b.State())
	}
}

func TestBreakerHalfOpenFailure(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1, ResetTimeout: 10 * time.Millisecond})

	b.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	b.Allow() // transition to half-open

	b.RecordFailure()
	if b.State() != BreakerOpen {
		t.Fatalf("expected open after half-open failure, got %s", b.State())
	}
}

func TestBreakerStateString(t *testing.T) {
	if BreakerClosed.String() != "closed" {
		t.Fatal("wrong string for closed")
	}
	if BreakerOpen.String() != "open" {
		t.Fatal("wrong string for open")
	}
	if BreakerHalfOpen.String() != "half_open" {
		t.Fatal("wrong string for half_open")
	}
}

func TestBreakerDefaults(t *testing.T) {
	b := NewBreaker(BreakerConfig{})
	// Default threshold is 5.
	for i := 0; i < 4; i++ {
		b.RecordFailure()
	}
	if b.State() != BreakerClosed {
		t.Fatal("expected closed with 4 failures (default threshold 5)")
	}
	b.RecordFailure()
	if b.State() != BreakerOpen {
		t.Fatal("expected open after 5 failures")
	}
}
