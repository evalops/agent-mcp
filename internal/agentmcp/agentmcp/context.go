package agentmcp

import (
	"context"
	"time"
)

// defaultBackgroundTimeout is the safety-net timeout applied to background
// goroutines when no explicit RequestTimeout is configured and the parent
// context carries no deadline.  It prevents fire-and-forget RPCs from hanging
// indefinitely.
const defaultBackgroundTimeout = 10 * time.Second

// detachedContextWithTimeout lets background work outlive the request while
// retaining a bounded lifetime when a timeout is configured.
func detachedContextWithTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	bgCtx := context.WithoutCancel(ctx)
	if timeout > 0 {
		return context.WithTimeout(bgCtx, timeout)
	}
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(bgCtx, deadline)
	}
	// No configured timeout and no parent deadline — apply a safety-net
	// timeout so background work cannot hang forever.
	return context.WithTimeout(bgCtx, defaultBackgroundTimeout)
}
