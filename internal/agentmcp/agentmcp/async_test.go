package agentmcp

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestAsyncRunnerRejectsWhenFull(t *testing.T) {
	runner := NewAsyncRunner(1, testLogger)
	release := make(chan struct{})
	started := make(chan struct{})

	if !runner.TryGo(context.Background(), "first", func(context.Context) error {
		close(started)
		<-release
		return nil
	}) {
		t.Fatal("expected first background task to be accepted")
	}
	<-started

	var ran atomic.Bool
	if runner.TryGo(context.Background(), "second", func(context.Context) error {
		ran.Store(true)
		return nil
	}) {
		t.Fatal("expected second background task to be rejected while capacity is exhausted")
	}
	if ran.Load() {
		t.Fatal("rejected task should not run")
	}
	close(release)
}
