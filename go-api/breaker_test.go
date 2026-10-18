package main

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// testBreaker returns a breaker with a controllable clock.
func testBreaker(threshold int, cooldown time.Duration) (*circuitBreaker, *time.Time) {
	now := time.Unix(1700000000, 0)
	b := newCircuitBreaker(threshold, cooldown)
	b.now = func() time.Time { return now }
	return b, &now
}

// openWait returns the retry-after of an open-circuit error, or fails the test.
func openWait(t *testing.T, err error) time.Duration {
	t.Helper()
	var openErr *circuitOpenError
	if !errors.As(err, &openErr) {
		t.Fatalf("allow() = %v, want a circuit-open error", err)
	}
	return openErr.retryAfter
}

func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	b, now := testBreaker(3, 10*time.Second)

	for i := 0; i < 2; i++ {
		if err := b.allow(); err != nil {
			t.Fatalf("allow() before threshold = %v, want nil", err)
		}
		b.record(true)
	}
	if err := b.allow(); err != nil {
		t.Fatalf("allow() = %v, want nil", err)
	}
	b.record(true)

	if wait := openWait(t, b.allow()); wait != 10*time.Second {
		t.Errorf("retryAfter = %s, want 10s", wait)
	}
	*now = now.Add(4 * time.Second)
	if wait := openWait(t, b.allow()); wait != 6*time.Second {
		t.Errorf("retryAfter after 4s = %s, want 6s", wait)
	}
}

func TestCircuitBreakerSuccessResetsFailures(t *testing.T) {
	b, _ := testBreaker(3, 10*time.Second)

	for _, failed := range []bool{true, true, false, true, true} {
		if err := b.allow(); err != nil {
			t.Fatalf("allow() = %v, want nil", err)
		}
		b.record(failed)
	}

	if err := b.allow(); err != nil {
		t.Errorf("allow() = %v, want nil: failures are only counted consecutively", err)
	}
}

func TestCircuitBreakerHalfOpenLetsOneProbeThrough(t *testing.T) {
	b, now := testBreaker(1, 10*time.Second)
	b.allow()
	b.record(true)

	*now = now.Add(10 * time.Second)
	if err := b.allow(); err != nil {
		t.Fatalf("probe allow() = %v, want nil after the cooldown", err)
	}
	openWait(t, b.allow()) // a second caller while the probe is in flight is rejected

	b.record(false)
	if err := b.allow(); err != nil {
		t.Errorf("allow() after a successful probe = %v, want nil (closed)", err)
	}
}

func TestCircuitBreakerFailedProbeReopens(t *testing.T) {
	b, now := testBreaker(1, 10*time.Second)
	b.allow()
	b.record(true)
	*now = now.Add(10 * time.Second)
	b.allow()

	b.record(true)

	if wait := openWait(t, b.allow()); wait != 10*time.Second {
		t.Errorf("retryAfter = %s, want a fresh 10s cooldown", wait)
	}
}

func TestNilCircuitBreakerAllowsEverything(t *testing.T) {
	var b *circuitBreaker

	b.record(true)
	if err := b.allow(); err != nil {
		t.Errorf("nil breaker allow() = %v, want nil", err)
	}
}

func TestBreakerFromEnv(t *testing.T) {
	tests := []struct {
		name, threshold, cooldown string
		wantNil                   bool
		wantThreshold             int
		wantCooldown              time.Duration
	}{
		{"defaults", "", "", false, 5, 30 * time.Second},
		{"custom", "3", "1m", false, 3, time.Minute},
		{"zero disables", "0", "", true, 0, 0},
		{"garbage uses defaults", "lots", "later", false, 5, 30 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AI_BREAKER_THRESHOLD", tc.threshold)
			t.Setenv("AI_BREAKER_COOLDOWN", tc.cooldown)

			b := breakerFromEnv()

			if tc.wantNil {
				if b != nil {
					t.Errorf("got %+v, want nil", b)
				}
				return
			}
			if b == nil || b.threshold != tc.wantThreshold || b.cooldown != tc.wantCooldown {
				t.Errorf("got %+v, want threshold=%d cooldown=%s", b, tc.wantThreshold, tc.wantCooldown)
			}
		})
	}
}

func TestForecastFailsFastWhileBreakerIsOpen(t *testing.T) {
	useBreaker(t, newCircuitBreaker(2, time.Minute))
	fakePrometheus(t, promRange(1, 2), promInstant(1))
	ai := fakeAIRoutes(t, map[string]aiReply{
		"/forecast": {http.StatusInternalServerError, `{}`},
		"/predict":  {http.StatusInternalServerError, `{}`},
	})

	first := getForecast("")
	second := getForecast("")

	if first.Code != http.StatusBadGateway {
		t.Errorf("first status = %d, want 502", first.Code)
	}
	if second.Code != http.StatusServiceUnavailable || second.Header().Get("Retry-After") != "60" {
		t.Errorf("second = %d (Retry-After %q), want 503 with Retry-After 60", second.Code, second.Header().Get("Retry-After"))
	}
	if n := ai.count(); n != 2 {
		t.Errorf("AI calls = %d, want 2 (the open breaker must not call the AI service)", n)
	}
}

func TestBreakerIgnoresMissingCachedModel(t *testing.T) {
	useBreaker(t, newCircuitBreaker(1, time.Minute))
	fakePrometheus(t, promRange(1, 2), promInstant(1))
	fakeAIRoutes(t, map[string]aiReply{"/predict": {http.StatusOK, `{}`}}) // /forecast answers 404

	for i := 0; i < 3; i++ {
		if rec := getForecast(""); rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200: a 404 is not an AI failure", i, rec.Code)
		}
	}
}
