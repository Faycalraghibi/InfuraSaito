package main

import (
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

// aiBreaker guards every call to the AI service. nil disables it.
var aiBreaker = breakerFromEnv()

// circuitOpenError is returned instead of calling the AI service while the breaker is open.
type circuitOpenError struct {
	retryAfter time.Duration
}

func (e *circuitOpenError) Error() string {
	return "AI service unavailable (circuit breaker open)"
}

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

func (s breakerState) String() string {
	return [...]string{"closed", "open", "half-open"}[s]
}

// circuitBreaker stops calling the AI service after threshold consecutive failures.
// While open, calls fail fast for cooldown; then a single probe call is let through:
// success closes the breaker, failure opens it for another cooldown.
type circuitBreaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	now       func() time.Time
	state     breakerState
	failures  int
	openedAt  time.Time
	probing   bool
}

func newCircuitBreaker(threshold int, cooldown time.Duration) *circuitBreaker {
	return &circuitBreaker{threshold: threshold, cooldown: cooldown, now: time.Now}
}

// allow returns nil when a call may proceed, or a *circuitOpenError when it must not.
func (b *circuitBreaker) allow() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case breakerOpen:
		if wait := b.cooldown - b.now().Sub(b.openedAt); wait > 0 {
			return &circuitOpenError{retryAfter: wait}
		}
		b.setState(breakerHalfOpen)
		b.probing = true
	case breakerHalfOpen:
		if b.probing {
			return &circuitOpenError{retryAfter: time.Second}
		}
		b.probing = true
	}
	return nil
}

// record reports the outcome of a call that allow let through.
func (b *circuitBreaker) record(failed bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.probing = false
	if !failed {
		b.failures = 0
		b.setState(breakerClosed)
		return
	}
	b.failures++
	if b.state == breakerHalfOpen || b.failures >= b.threshold {
		b.openedAt = b.now()
		b.setState(breakerOpen)
	}
}

func (b *circuitBreaker) setState(s breakerState) {
	if b.state != s {
		log.Printf("AI circuit breaker: %s -> %s", b.state, s)
		b.state = s
	}
}

// breakerFromEnv builds the breaker from AI_BREAKER_THRESHOLD (consecutive failures before
// opening, default 5, 0 disables) and AI_BREAKER_COOLDOWN (default 30s).
func breakerFromEnv() *circuitBreaker {
	threshold := 5
	if v := os.Getenv("AI_BREAKER_THRESHOLD"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			threshold = parsed
		} else {
			log.Printf("invalid AI_BREAKER_THRESHOLD=%q, using %d", v, threshold)
		}
	}
	if threshold == 0 {
		return nil
	}
	return newCircuitBreaker(threshold, durationFromEnv("AI_BREAKER_COOLDOWN", 30*time.Second))
}
