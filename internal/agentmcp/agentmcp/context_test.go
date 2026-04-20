package agentmcp

import (
	"context"
	"testing"
	"time"
)

func TestDetachedContextWithTimeout_ExplicitTimeout(t *testing.T) {
	ctx := context.Background()
	bgCtx, cancel := detachedContextWithTimeout(ctx, 42*time.Second)
	defer cancel()

	deadline, ok := bgCtx.Deadline()
	if !ok {
		t.Fatal("expected background context to have a deadline when explicit timeout is set")
	}
	remaining := time.Until(deadline)
	if remaining < 40*time.Second || remaining > 43*time.Second {
		t.Fatalf("expected ~42s remaining, got %v", remaining)
	}
}

func TestDetachedContextWithTimeout_InheritsParentDeadline(t *testing.T) {
	parentDeadline := time.Now().Add(7 * time.Second)
	ctx, parentCancel := context.WithDeadline(context.Background(), parentDeadline)
	defer parentCancel()

	bgCtx, cancel := detachedContextWithTimeout(ctx, 0)
	defer cancel()

	deadline, ok := bgCtx.Deadline()
	if !ok {
		t.Fatal("expected background context to inherit parent deadline")
	}
	// Should be within 1s of the parent deadline.
	if diff := deadline.Sub(parentDeadline).Abs(); diff > time.Second {
		t.Fatalf("expected deadline close to parent, got diff=%v", diff)
	}
}

func TestDetachedContextWithTimeout_DefaultFallback(t *testing.T) {
	// No configured timeout, no parent deadline.
	ctx := context.Background()
	bgCtx, cancel := detachedContextWithTimeout(ctx, 0)
	defer cancel()

	deadline, ok := bgCtx.Deadline()
	if !ok {
		t.Fatal("expected background context to have a deadline even without configured timeout or parent deadline")
	}
	remaining := time.Until(deadline)
	if remaining < 8*time.Second || remaining > 11*time.Second {
		t.Fatalf("expected ~%v remaining (defaultBackgroundTimeout), got %v", defaultBackgroundTimeout, remaining)
	}
}

func TestDetachedContextWithTimeout_DetachedFromParentCancellation(t *testing.T) {
	ctx, parentCancel := context.WithCancel(context.Background())
	bgCtx, cancel := detachedContextWithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Cancel the parent — background context should NOT be canceled.
	parentCancel()

	select {
	case <-bgCtx.Done():
		t.Fatal("expected background context to survive parent cancellation")
	default:
		// OK: background context is still alive.
	}
}
