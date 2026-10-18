package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okHandler records whether it was reached and answers 200.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestAPIKeyMiddleware(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		header   string
		wantCode int
	}{
		{"auth disabled", "", "", http.StatusOK},
		{"auth disabled ignores header", "", "anything", http.StatusOK},
		{"missing key", "s3cret", "", http.StatusUnauthorized},
		{"wrong key", "s3cret", "nope", http.StatusUnauthorized},
		{"prefix of key", "s3cret", "s3c", http.StatusUnauthorized},
		{"correct key", "s3cret", "s3cret", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
			if tc.header != "" {
				req.Header.Set("X-API-Key", tc.header)
			}
			rec := httptest.NewRecorder()

			withAPIKey(tc.key, okHandler(&reached)).ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if reached != (tc.wantCode == http.StatusOK) {
				t.Errorf("handler reached = %v, want %v", reached, tc.wantCode == http.StatusOK)
			}
		})
	}
}

func TestTokenBucket(t *testing.T) {
	now := time.Unix(1700000000, 0)
	b := newTokenBucket(1, 2)
	b.now = func() time.Time { return now }
	b.last = now

	steps := []struct {
		advance time.Duration
		want    bool
	}{
		{0, true},  // burst token 1
		{0, true},  // burst token 2
		{0, false}, // empty
		{time.Second, true},
		{0, false},
		{10 * time.Second, true}, // refilled, but capped at burst 2
		{0, true},
		{0, false},
	}
	for i, s := range steps {
		now = now.Add(s.advance)
		if got := b.allow(); got != s.want {
			t.Fatalf("step %d (advance %s): allow() = %v, want %v", i, s.advance, got, s.want)
		}
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	b := newTokenBucket(0.001, 1)
	reached := false
	h := withRateLimit(b, okHandler(&reached))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil))
	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil))

	if first.Code != http.StatusOK {
		t.Errorf("first status = %d, want 200", first.Code)
	}
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Errorf("second = %d (Retry-After %q), want 429 with Retry-After", second.Code, second.Header().Get("Retry-After"))
	}
}

func TestRateLimitMiddlewareDisabled(t *testing.T) {
	reached := false
	rec := httptest.NewRecorder()

	withRateLimit(nil, okHandler(&reached)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK || !reached {
		t.Errorf("got %d (reached %v), want 200 through a nil limiter", rec.Code, reached)
	}
}

func TestRateLimiterFromEnv(t *testing.T) {
	tests := []struct {
		name, rps, burst string
		wantNil          bool
		wantRPS          float64
		wantBurst        float64
	}{
		{"defaults", "", "", false, 10, 20},
		{"custom", "2.5", "5", false, 2.5, 5},
		{"zero disables", "0", "", true, 0, 0},
		{"garbage uses defaults", "fast", "many", false, 10, 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RATE_LIMIT_RPS", tc.rps)
			t.Setenv("RATE_LIMIT_BURST", tc.burst)

			b := rateLimiterFromEnv()

			if tc.wantNil {
				if b != nil {
					t.Errorf("got limiter %+v, want nil", b)
				}
				return
			}
			if b == nil || b.rps != tc.wantRPS || b.burst != tc.wantBurst {
				t.Errorf("got %+v, want rps=%v burst=%v", b, tc.wantRPS, tc.wantBurst)
			}
		})
	}
}

func TestRouterKeepsHealthzOpen(t *testing.T) {
	router := newRouter("s3cret", nil)

	health := httptest.NewRecorder()
	router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	api := httptest.NewRecorder()
	router.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil))

	if health.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200 without a key", health.Code)
	}
	if api.Code != http.StatusUnauthorized {
		t.Errorf("/api/v1/instances status = %d, want 401 without a key", api.Code)
	}
}

func TestRouterRoutesAPIWithKey(t *testing.T) {
	fakePrometheus(t, "", promEmptyVector)
	router := newRouter("s3cret", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set("X-API-Key", "s3cret")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
}
