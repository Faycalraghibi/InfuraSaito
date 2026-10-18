package main

import (
	"crypto/subtle"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// withAPIKey requires the X-API-Key header to equal key. An empty key disables the check.
func withAPIKey(key string, next http.Handler) http.Handler {
	if key == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-API-Key")), []byte(key)) != 1 {
			http.Error(w, "Missing or invalid API key", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenBucket is a global rate limiter: up to burst requests at once, refilled at rps per second.
type tokenBucket struct {
	mu     sync.Mutex
	rps    float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newTokenBucket(rps float64, burst int) *tokenBucket {
	return &tokenBucket{
		rps:    rps,
		burst:  float64(burst),
		tokens: float64(burst),
		last:   time.Now(),
		now:    time.Now,
	}
}

// allow takes a token if one is available; otherwise it reports how long until one is.
func (b *tokenBucket) allow() (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rps)
	b.last = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / b.rps * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

// retryAfterSeconds formats a wait as a Retry-After value: whole seconds, rounded up, at least 1.
func retryAfterSeconds(wait time.Duration) string {
	return strconv.Itoa(max(1, int(math.Ceil(wait.Seconds()))))
}

// withRateLimit answers 429 when the bucket is empty. A nil bucket disables limiting.
func withRateLimit(b *tokenBucket, next http.Handler) http.Handler {
	if b == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := b.allow(); !ok {
			w.Header().Set("Retry-After", retryAfterSeconds(wait))
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimiterFromEnv builds the limiter from RATE_LIMIT_RPS (default 10, 0 disables)
// and RATE_LIMIT_BURST (default 20). Invalid values fall back to the defaults.
func rateLimiterFromEnv() *tokenBucket {
	rps := 10.0
	if v := os.Getenv("RATE_LIMIT_RPS"); v != "" {
		// Reject Inf: Inf*0 is NaN, which would make every request pass.
		if parsed, err := strconv.ParseFloat(v, 64); err == nil && parsed >= 0 && !math.IsInf(parsed, 0) {
			rps = parsed
		} else {
			log.Printf("invalid RATE_LIMIT_RPS=%q, using %v", v, rps)
		}
	}
	if rps == 0 {
		return nil
	}

	burst := 20
	if v := os.Getenv("RATE_LIMIT_BURST"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			burst = parsed
		} else {
			log.Printf("invalid RATE_LIMIT_BURST=%q, using %d", v, burst)
		}
	}
	return newTokenBucket(rps, burst)
}

// newRouter serves /healthz openly and every /api/ route behind rate limiting and the API key.
// Rate limiting runs first so it also slows down key guessing.
func newRouter(apiKey string, limiter *tokenBucket) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("/api/v1/metrics/current", currentMetricsHandler)
	api.HandleFunc("/api/v1/forecast", forecastHandler)
	api.HandleFunc("/api/v1/forecast/threshold", thresholdHandler)
	api.HandleFunc("/api/v1/forecast/accuracy", accuracyHandler)
	api.HandleFunc("/api/v1/instances", instancesHandler)
	api.HandleFunc("/api/v1/anomalies", anomaliesHandler)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthzHandler)
	mux.Handle("/api/", withRateLimit(limiter, withAPIKey(apiKey, api)))
	return mux
}
