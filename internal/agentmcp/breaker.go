package agentmcp

import (
	"time"

	"github.com/evalops/service-runtime/resilience"
)

type Breaker = resilience.Breaker
type BreakerState = resilience.BreakerState

const (
	BreakerClosed   = resilience.StateClosed
	BreakerOpen     = resilience.StateOpen
	BreakerHalfOpen = resilience.StateHalfOpen
)

// BreakerConfig configures a circuit breaker.
type BreakerConfig struct {
	FailureThreshold int
	ResetTimeout     time.Duration
}

func NewBreaker(cfg BreakerConfig) *Breaker {
	return resilience.NewBreaker(resilience.BreakerConfig{
		FailureThreshold: cfg.FailureThreshold,
		ResetTimeout:     cfg.ResetTimeout,
		HalfOpenMax:      1,
	})
}
