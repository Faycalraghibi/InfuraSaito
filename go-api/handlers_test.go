package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestForecastRejectsUnsupportedMetric(t *testing.T) {
	rec := getForecast(`metric=bogus%22,%22x%22:%22y`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
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
	if rec.Body.String() != aiBody {
		t.Errorf("body = %s, want AI response passed through", rec.Body)
	}
	if got.MetricName != "memory" || got.HorizonMinutes != 30 || len(got.History) != 3 || got.History[1].Y != 20 {
		t.Errorf("AI request = %+v, want memory/30 with 3 points", got)
	}
}

func TestForecastDefaultsToCPUAnd60Minutes(t *testing.T) {
	fakePrometheus(t, promRange(1, 2), promInstant(1))
	got := fakeAIService(t, http.StatusOK, `{}`)

	getForecast("")

	if got.MetricName != "cpu" || got.HorizonMinutes != 60 {
		t.Errorf("AI request = %s/%d, want cpu/60", got.MetricName, got.HorizonMinutes)
	}
}

func TestForecastColdStartFallback(t *testing.T) {
	fakePrometheus(t, promRange(), promInstant(1))

	rec := getForecast("metric=cpu&horizon_minutes=15")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body fallbackResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body.MetricName != "cpu" || body.HorizonMinutes != 15 || len(body.Predictions) != 1 {
		t.Errorf("fallback = %+v", body)
	}
}

func TestForecastColdStartWithExporterDown(t *testing.T) {
	fakePrometheus(t, promRange(), promInstant(0))

	if rec := getForecast(""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestForecastColdStartWithNoUpSeries(t *testing.T) {
	fakePrometheus(t, promRange(), promEmptyVector)

	if rec := getForecast(""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestForecastPrometheusError(t *testing.T) {
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
