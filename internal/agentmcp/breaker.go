package agentmcp

import (
	"errors"
	"sync"
	"time"
)

// BreakerState represents the circuit breaker state.
type BreakerState int

const (
	BreakerClosed   BreakerState = iota // normal — requests flow through
	BreakerOpen                         // tripped — requests fast-fail
	BreakerHalfOpen                     // probing — one request allowed through
)

// ErrBreakerOpen is returned when the circuit breaker is open.
var ErrBreakerOpen = errors.New("circuit breaker open")

// BreakerConfig configures a circuit breaker.
type BreakerConfig struct {
	FailureThreshold int           // consecutive failures before opening (default 5)
	ResetTimeout     time.Duration // time in open state before probing (default 30s)
}

// Breaker implements a three-state circuit breaker.
type Breaker struct {
	mu               sync.Mutex
	state            BreakerState
	failures         int
	lastFailure      time.Time
	failureThreshold int
	resetTimeout     time.Duration
}

func NewBreaker(cfg BreakerConfig) *Breaker {
	threshold := cfg.FailureThreshold
	if threshold <= 0 {
		threshold = 5
	}
	timeout := cfg.ResetTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Breaker{
		failureThreshold: threshold,
		resetTimeout:     timeout,
	}
}

// Allow checks whether a request should be allowed through.
// Returns true if the request can proceed.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case BreakerClosed:
		return true
	case BreakerOpen:
		if time.Since(b.lastFailure) > b.resetTimeout {
			b.state = BreakerHalfOpen
			return true
		}
		return false
	case BreakerHalfOpen:
		return false // only one probe at a time
	}
	return true
}

// RecordSuccess records a successful request.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.state = BreakerClosed
}

// RecordFailure records a failed request.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	b.lastFailure = time.Now()
	if b.failures >= b.failureThreshold {
		b.state = BreakerOpen
	}
}

// State returns the current breaker state.
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}
