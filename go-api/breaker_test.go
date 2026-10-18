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

// call runs one allowed call through b with the given outcome, failing the test if refused.
func call(t *testing.T, b *circuitBreaker, outcome callOutcome) {
	t.Helper()
	gen, err := b.allow()
	if err != nil {
		t.Fatalf("allow() = %v, want nil", err)
	}
	b.record(gen, outcome)
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

func allowErr(b *circuitBreaker) error {
	_, err := b.allow()
	return err
}

func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	b, now := testBreaker(3, 10*time.Second)

	call(t, b, outcomeFailure)
	call(t, b, outcomeFailure)
	call(t, b, outcomeFailure)

	if wait := openWait(t, allowErr(b)); wait != 10*time.Second {
		t.Errorf("retryAfter = %s, want 10s", wait)
	}
	*now = now.Add(4 * time.Second)
	if wait := openWait(t, allowErr(b)); wait != 6*time.Second {
		t.Errorf("retryAfter after 4s = %s, want 6s", wait)
	}
}

func TestCircuitBreakerSuccessResetsFailures(t *testing.T) {
	b, _ := testBreaker(3, 10*time.Second)

	for _, o := range []callOutcome{outcomeFailure, outcomeFailure, outcomeSuccess, outcomeFailure, outcomeFailure} {
		call(t, b, o)
	}

	if err := allowErr(b); err != nil {
		t.Errorf("allow() = %v, want nil: failures are only counted consecutively", err)
	}
}

func TestCircuitBreakerNeutralOutcomeKeepsTheCount(t *testing.T) {
	b, _ := testBreaker(3, 10*time.Second)

	for _, o := range []callOutcome{outcomeFailure, outcomeNeutral, outcomeFailure, outcomeNeutral, outcomeFailure} {
		call(t, b, o)
	}

	openWait(t, allowErr(b)) // neutral answers neither reset nor add to the count
}

func TestCircuitBreakerHalfOpenLetsOneProbeThrough(t *testing.T) {
	b, now := testBreaker(1, 10*time.Second)
	call(t, b, outcomeFailure)

	*now = now.Add(10 * time.Second)
	gen, err := b.allow()
	if err != nil {
		t.Fatalf("probe allow() = %v, want nil after the cooldown", err)
	}
	if wait := openWait(t, allowErr(b)); wait != 10*time.Second {
		t.Errorf("retryAfter while probing = %s, want the 10s cooldown", wait)
	}

	b.record(gen, outcomeSuccess)
	if err := allowErr(b); err != nil {
		t.Errorf("allow() after a successful probe = %v, want nil (closed)", err)
	}
}

func TestCircuitBreakerNeutralProbeCloses(t *testing.T) {
	b, now := testBreaker(1, 10*time.Second)
	call(t, b, outcomeFailure)
	*now = now.Add(10 * time.Second)

	call(t, b, outcomeNeutral)

	if err := allowErr(b); err != nil {
		t.Errorf("allow() = %v, want nil: the service answered the probe", err)
	}
}

func TestCircuitBreakerFailedProbeReopens(t *testing.T) {
	b, now := testBreaker(1, 10*time.Second)
	call(t, b, outcomeFailure)
	*now = now.Add(10 * time.Second)

	call(t, b, outcomeFailure)

	if wait := openWait(t, allowErr(b)); wait != 10*time.Second {
		t.Errorf("retryAfter = %s, want a fresh 10s cooldown", wait)
	}
}

func TestCircuitBreakerIgnoresResultsFromBeforeAStateChange(t *testing.T) {
	b, now := testBreaker(1, 10*time.Second)
	slowGen, _ := b.allow() // a slow call starts while closed
	call(t, b, outcomeFailure)

	b.record(slowGen, outcomeSuccess) // ...and finishes after the breaker opened
	openWait(t, allowErr(b))

	*now = now.Add(10 * time.Second)
	probeGen, _ := b.allow()
	b.record(slowGen, outcomeSuccess) // a stale result must not free the probe slot
	openWait(t, allowErr(b))

	b.record(probeGen, outcomeSuccess)
	if err := allowErr(b); err != nil {
		t.Errorf("allow() after the real probe succeeded = %v, want nil", err)
	}
}

func TestNilCircuitBreakerAllowsEverything(t *testing.T) {
	var b *circuitBreaker

	b.record(0, outcomeFailure)
	if err := allowErr(b); err != nil {
		t.Errorf("nil breaker allow() = %v, want nil", err)
	}
}

func TestClassifyAIStatus(t *testing.T) {
	tests := []struct {
		path   string
		status int
		want   callOutcome
	}{
		{"/forecast", 200, outcomeSuccess},
		{"/forecast", 404, outcomeNeutral},
		{"/predict", 400, outcomeNeutral},
		{"/predict", 422, outcomeNeutral},
		{"/predict", 500, outcomeFailure},
		{"/forecast", 503, outcomeFailure},
		{"/train", 200, outcomeSuccess},
		{"/train", 500, outcomeNeutral},
	}
	for _, tc := range tests {
		if got := classifyAIStatus(tc.path, tc.status); got != tc.want {
			t.Errorf("classifyAIStatus(%s, %d) = %d, want %d", tc.path, tc.status, got, tc.want)
		}
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

func TestBreakerOpensWhenTrainingOnRequestKeepsFailing(t *testing.T) {
	// No cached model (/forecast 404) and a broken fit path (/predict 500): the 404s
	// must not reset the count, or the breaker would never open.
	useBreaker(t, newCircuitBreaker(2, time.Minute))
	fakePrometheus(t, promRange(1, 2), promInstant(1))
	fakeAIRoutes(t, map[string]aiReply{"/predict": {http.StatusInternalServerError, `{}`}})

	getForecast("")
	getForecast("")

	if rec := getForecast(""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("third status = %d, want 503 after two failed fits", rec.Code)
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

func TestTrainingErrorsDoNotOpenTheBreaker(t *testing.T) {
	b := newCircuitBreaker(1, time.Minute)
	useBreaker(t, b)
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1, 2)), "")
	fakeAIRoutes(t, map[string]aiReply{"/train": {http.StatusInternalServerError, `{}`}})

	trainAll()

	if err := allowErr(b); err != nil {
		t.Errorf("allow() = %v, want nil: a fit error on bad data is not an outage", err)
	}
}

func TestTrainAllStopsWhenTheBreakerIsOpen(t *testing.T) {
	b := newCircuitBreaker(1, time.Minute)
	call(t, b, outcomeFailure)
	useBreaker(t, b)
	calls := fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1, 2)), "")
	ai := fakeAIRoutes(t, map[string]aiReply{"/train": {http.StatusOK, `{}`}})

	if trained := trainAll(); trained != 0 {
		t.Errorf("trained = %d, want 0", trained)
	}
	if ai.count() != 0 || calls.count() != 1 {
		t.Errorf("AI calls = %d, Prometheus queries = %d; want 0 and 1 (stop after the first refusal)", ai.count(), calls.count())
	}
}
