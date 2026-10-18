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

// callOutcome is how a finished call affects the breaker.
type callOutcome int

const (
	// outcomeSuccess: the service handled the call; closes the breaker and resets the count.
	outcomeSuccess callOutcome = iota
	// outcomeFailure: the service looks unhealthy (transport error or server error).
	outcomeFailure
	// outcomeNeutral: the service answered, but the result says nothing about its health
	// (e.g. 404 for a missing cached model, or a training error on bad data).
	outcomeNeutral
)

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
// a success (or neutral answer) closes the breaker, a failure opens it for another cooldown.
//
// Every state change starts a new generation. allow returns the current generation and
// record ignores results from older ones, so calls that started before a state change
// (and may finish long after it) cannot close the breaker early or free the probe slot.
type circuitBreaker struct {
	mu         sync.Mutex
	threshold  int
	cooldown   time.Duration
	now        func() time.Time
	state      breakerState
	generation uint64
	failures   int
	openedAt   time.Time
	probing    bool
}

func newCircuitBreaker(threshold int, cooldown time.Duration) *circuitBreaker {
	return &circuitBreaker{threshold: threshold, cooldown: cooldown, now: time.Now}
}

// allow returns the generation to pass to record when a call may proceed, or a
// *circuitOpenError when it must not.
func (b *circuitBreaker) allow() (uint64, error) {
	if b == nil {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case breakerOpen:
		if wait := b.cooldown - b.now().Sub(b.openedAt); wait > 0 {
			return 0, &circuitOpenError{retryAfter: wait}
		}
		b.setState(breakerHalfOpen)
		b.probing = true
	case breakerHalfOpen:
		if b.probing {
			// A probe is in flight; it may take up to the AI timeout, so don't invite
			// clients to retry every second.
			return 0, &circuitOpenError{retryAfter: b.cooldown}
		}
		b.probing = true
	}
	return b.generation, nil
}

// record reports the outcome of a call that allow let through in generation gen.
func (b *circuitBreaker) record(gen uint64, outcome callOutcome) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if gen != b.generation {
		return // the call started before the last state change
	}

	if b.state == breakerHalfOpen {
		b.probing = false
		if outcome == outcomeFailure {
			b.open()
		} else {
			b.failures = 0
			b.setState(breakerClosed)
		}
		return
	}

	switch outcome {
	case outcomeSuccess:
		b.failures = 0
	case outcomeFailure:
		b.failures++
		if b.failures >= b.threshold {
			b.open()
		}
	}
}

func (b *circuitBreaker) open() {
	b.openedAt = b.now()
	b.setState(breakerOpen)
}

func (b *circuitBreaker) setState(s breakerState) {
	if b.state != s {
		log.Printf("AI circuit breaker: %s -> %s", b.state, s)
		b.state = s
		b.generation++
		breakerStateGauge.Set(float64(s))
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
