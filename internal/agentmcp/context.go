package agentmcp

import (
	"context"
	"time"
)

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
	return context.WithCancel(bgCtx)
}
