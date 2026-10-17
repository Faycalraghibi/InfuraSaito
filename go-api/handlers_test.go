package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestForecastRejectsUnsupportedMetric(t *testing.T) {
	failIfAIServiceCalled(t)

	// A quote-laden value must be rejected, not echoed into the response.
	rec := getForecast(`metric=bogus%22,%22x%22:%22y`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cpu, memory") {
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

func TestForecastInvalidHorizonDefaultsTo60(t *testing.T) {
	tests := []struct {
		query string
		want  int
	}{
		{"horizon_minutes=abc", 60},
		{"horizon_minutes=0", 60},
		{"horizon_minutes=-5", 60},
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
			fakePrometheus(t, tc.rangeBody, promInstant(1))

			rec := getForecast("metric=cpu&horizon_minutes=15")

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body fallbackResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if body.MetricName != "cpu" || body.HorizonMinutes != 15 || len(body.Predictions) != 1 {
				t.Errorf("fallback = %+v, want cpu/15 with 1 prediction", body)
			}
		})
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
