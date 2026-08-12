// Package breaker implements a simple circuit breaker with half-open probes.
package breaker

import (
	"sync"
	"time"
)

// State is the breaker state.
type State int

// Breaker states.
const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

// Breaker allows calls through until consecutive failures reach the
// threshold, then trips open for a cooldown period.
type Breaker struct {
	mu                sync.Mutex
	threshold         int
	cooldown          time.Duration
	failures          int
	state             State
	openedAt          time.Time
	lastHalfOpenProbe time.Time
}

// New creates a breaker that trips after threshold consecutive failures and
// allows a probe after cooldown.
func New(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown}
}

// State returns the current state.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refresh()
	return b.state
}

// Allow reports whether a call may proceed. A half-open breaker only lets one
// probe through per cooldown window.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refresh()
	switch b.state {
	case StateClosed:
		return true
	case StateOpen:
		return false
	default: // half-open
		if b.lastHalfOpenProbe.IsZero() || time.Since(b.lastHalfOpenProbe) >= b.cooldown {
			b.lastHalfOpenProbe = time.Now()
			return true
		}
		return false
	}
}

// Success records a successful call, resetting failures.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	if b.state == StateHalfOpen {
		b.state = StateClosed
		b.openedAt = time.Time{}
		b.lastHalfOpenProbe = time.Time{}
	}
}

// Failure records a failed call, tripping the breaker when the threshold is
// reached.
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.state == StateHalfOpen || b.failures >= b.threshold {
		b.state = StateOpen
		b.openedAt = time.Now()
	}
}

// refresh moves the breaker from open to half-open after the cooldown.
func (b *Breaker) refresh() {
	if b.state == StateOpen && !b.openedAt.IsZero() && time.Since(b.openedAt) >= b.cooldown {
		b.state = StateHalfOpen
	}
}
