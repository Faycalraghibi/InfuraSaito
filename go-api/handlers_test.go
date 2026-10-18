package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestForecastRejectsUnsupportedMetric(t *testing.T) {
	failIfAIServiceCalled(t)

	// A quote-laden value must be rejected, not echoed into the response.
	rec := getForecast(`metric=bogus%22,%22x%22:%22y`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), supportedMetrics()) {
		t.Errorf("body = %q, want it to list the supported metrics", rec.Body)
	}
}

func TestForecastSendsHistoryToAIService(t *testing.T) {
	fakePrometheus(t, promRange(10, 20, 30), promInstant(1))
	aiBody := `{"metric_name":"memory","horizon_minutes":30,"predictions":[]}`
	got := fakeAIService(t, http.StatusOK, aiBody)

	rec := getForecast("metric=memory&horizon_minutes=30")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if rec.Body.String() != aiBody {
		t.Errorf("body = %s, want AI response passed through", rec.Body)
	}
	if got.MetricName != "memory" || got.HorizonMinutes != 30 {
		t.Errorf("AI request = %s/%d, want memory/30", got.MetricName, got.HorizonMinutes)
	}
	if len(got.History) != 3 || got.History[1].Y != 20 {
		t.Errorf("AI request history has %d points (%+v), want 3 with second y=20", len(got.History), got.History)
	}
}

func TestForecastDefaultsToCPUAnd60Minutes(t *testing.T) {
	fakePrometheus(t, promRange(1, 2), promInstant(1))
	got := fakeAIService(t, http.StatusOK, `{}`)

	rec := getForecast("")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	if got.MetricName != "cpu" || got.HorizonMinutes != 60 {
		t.Errorf("AI request = %s/%d, want cpu/60", got.MetricName, got.HorizonMinutes)
	}
}

func TestForecastHorizonParsing(t *testing.T) {
	tests := []struct {
		query string
		want  int
	}{
		{"horizon_minutes=abc", 60},
		{"horizon_minutes=0", 60},
		{"horizon_minutes=-5", 60},
		{"horizon_minutes=1", 1},
		{"horizon_minutes=10080", 10080},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			fakePrometheus(t, promRange(1, 2), promInstant(1))
			got := fakeAIService(t, http.StatusOK, `{}`)

			rec := getForecast(tc.query)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
			}
			if got.HorizonMinutes != tc.want {
				t.Errorf("horizon = %d, want %d", got.HorizonMinutes, tc.want)
			}
		})
	}
}

func TestForecastColdStartFallback(t *testing.T) {
	tests := []struct {
		name      string
		rangeBody string
	}{
		{"no points", promRange()},
		{"one point", promRange(42)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failIfAIServiceCalled(t)
			fakePrometheusByQuery(t, tc.rangeBody, map[string]string{
				exporterUpQuery:                    promInstant(1),
				metrics["network_rx"].instantQuery: promInstant(1234.5),
			})

			rec := getForecast("metric=network_rx&horizon_minutes=15")

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body fallbackResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if body.MetricName != "network_rx" || body.HorizonMinutes != 15 || len(body.Predictions) != 1 {
				t.Fatalf("fallback = %+v, want network_rx/15 with 1 prediction", body)
			}
			if p := body.Predictions[0]; p.Value != 1234.5 || p.Lower != 1234.5 || p.Upper != 1234.5 {
				t.Errorf("prediction = %+v, want the current value 1234.5 for value, lower and upper", p)
			}
		})
	}
}

func TestForecastColdStartWithoutCurrentValueReturns503(t *testing.T) {
	failIfAIServiceCalled(t)
	calls := fakePrometheusByQuery(t, promRange(), map[string]string{
		exporterUpQuery: promInstant(1),
		// no entry for the cpu instant query: Prometheus errors
	})

	if rec := getForecast(""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if n := len(calls.queries); n == 0 || calls.queries[n-1] != metrics["cpu"].instantQuery {
		t.Errorf("queries = %q, want the last to be the cpu instant query", calls.queries)
	}
}

func TestForecastColdStartWithExporterDown(t *testing.T) {
	failIfAIServiceCalled(t)
	fakePrometheus(t, promRange(), promInstant(0))

	if rec := getForecast(""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestForecastColdStartWithNoUpSeries(t *testing.T) {
	failIfAIServiceCalled(t)
	fakePrometheus(t, promRange(), promEmptyVector)

	if rec := getForecast(""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestForecastPrometheusError(t *testing.T) {
	failIfAIServiceCalled(t)
	fakePrometheus(t, "", "")

	if rec := getForecast(""); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestForecastAIServiceError(t *testing.T) {
	fakePrometheus(t, promRange(1, 2), promInstant(1))
	fakeAIService(t, http.StatusInternalServerError, `{"detail":"boom"}`)

	if rec := getForecast(""); rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestForecastAIServiceTimeoutReturns502(t *testing.T) {
	fakePrometheus(t, promRange(1, 2), promInstant(1))
	useClient(t, &aiClient, 50*time.Millisecond)
	old := aiServiceURL
	aiServiceURL = fakeSlowServer(t, 5*time.Second)
	t.Cleanup(func() { aiServiceURL = old })

	start := time.Now()
	rec := getForecast("")

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s, want the 50ms client timeout to apply", elapsed)
	}
}

func TestForecastUsesTheRequestedMetricsRangeQuery(t *testing.T) {
	calls := fakePrometheus(t, promRange(1, 2), promInstant(1))
	fakeAIService(t, http.StatusOK, `{}`)

	getForecast("metric=memory")

	if len(calls.queries) == 0 || calls.queries[0] != metrics["memory"].rangeQuery {
		t.Errorf("queries = %q, want first to be the memory range query", calls.queries)
	}
}

func TestCurrentMetricUsesInstantQuery(t *testing.T) {
	tests := []struct {
		query, metric string
	}{
		{"", "cpu"},
		{"metric=cpu", "cpu"},
		{"metric=memory", "memory"},
	}
	for _, tc := range tests {
		t.Run(tc.metric+"/"+tc.query, func(t *testing.T) {
			calls := fakePrometheus(t, "", promInstant(42.5))

			rec := getCurrent(tc.query)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
			}
			if len(calls.queries) != 1 || calls.queries[0] != metrics[tc.metric].instantQuery {
				t.Errorf("queries = %q, want the %s instant query", calls.queries, tc.metric)
			}
			var body struct {
				Metric string  `json:"metric"`
				Value  float64 `json:"value"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if body.Metric != tc.metric || body.Value != 42.5 {
				t.Errorf("body = %+v, want %s/42.5", body, tc.metric)
			}
		})
	}
}

func TestCurrentMetricRejectsUnsupportedMetric(t *testing.T) {
	calls := fakePrometheus(t, "", promInstant(1))

	rec := getCurrent("metric=bogus")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if len(calls.queries) != 0 {
		t.Errorf("Prometheus was queried %d times, want 0", len(calls.queries))
	}
}

func TestForecastRejectsHorizonAboveSevenDays(t *testing.T) {
	failIfAIServiceCalled(t)
	calls := fakePrometheus(t, promRange(1, 2), promInstant(1))

	rec := getForecast("horizon_minutes=10081")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "10080") {
		t.Errorf("body = %q, want it to state the 10080 limit", rec.Body)
	}
	if len(calls.queries) != 0 {
		t.Errorf("Prometheus was queried %d times, want 0", len(calls.queries))
	}
}
