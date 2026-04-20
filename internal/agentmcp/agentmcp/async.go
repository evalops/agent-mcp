package agentmcp

import (
	"context"
	"log/slog"

	"github.com/evalops/service-runtime/async"
)

type AsyncRunner = async.Runner

func NewAsyncRunner(maxInFlight int, logger *slog.Logger) *AsyncRunner {
	return async.NewRunner(maxInFlight, logger)
}

func (d *Deps) RunBackground(name string, fn func()) bool {
	if d == nil || fn == nil {
		return false
	}
	if d.Async == nil {
		go fn()
		return true
	}
	return d.Async.TryGo(context.Background(), name, func(context.Context) error {
		fn()
		return nil
	})
}
