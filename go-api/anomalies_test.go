package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func getAnomalies(query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?"+query, nil)
	rec := httptest.NewRecorder()
	anomaliesHandler(rec, req)
	return rec
}

func TestAnomaliesRejectsInvalidWindow(t *testing.T) {
	for _, query := range []string{"window_minutes=0", "window_minutes=-5", "window_minutes=1441", "window_minutes=abc", "metric=bogus"} {
		t.Run(query, func(t *testing.T) {
			failIfAIServiceCalled(t)
			calls := fakePrometheus(t, promRange(1, 2), promInstant(1))

			if rec := getAnomalies(query); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if calls.count() != 0 {
				t.Errorf("Prometheus queried %d times, want 0", calls.count())
			}
		})
	}
}

func TestAnomaliesPassesTheAIResponseThrough(t *testing.T) {
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1, 2, 3)), "")
	aiBody := `{"metric_name":"memory","instance":"a:9100","checked":3,"anomalies":[],"trained_at":"x"}`
	ai := fakeAIRoutes(t, map[string]aiReply{"/anomalies": {http.StatusOK, aiBody}})

	rec := getAnomalies("metric=memory&window_minutes=30")

	if rec.Code != http.StatusOK || rec.Body.String() != aiBody {
		t.Fatalf("got %d %s, want the AI response passed through", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	calls := ai.snapshot()
	if len(calls) != 1 {
		t.Fatalf("AI calls = %d, want 1", len(calls))
	}
	b := calls[0].body
	history, _ := b["history"].([]any)
	if b["metric_name"] != "memory" || b["instance"] != "a:9100" || len(history) != 3 {
		t.Errorf("/anomalies body = %v, want memory/a:9100 with 3 points", b)
	}
}

func TestAnomaliesWithoutRecentDataReturns503(t *testing.T) {
	failIfAIServiceCalled(t)
	fakePrometheus(t, promRange(), "")

	if rec := getAnomalies(""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestAnomaliesWithSeveralInstancesRequiresChoice(t *testing.T) {
	failIfAIServiceCalled(t)
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1), promSeriesJSON("b:9100", 2)), "")

	if rec := getAnomalies(""); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAnomaliesWithoutTrainedModelReturns503(t *testing.T) {
	fakePrometheus(t, promRange(1, 2), "")
	fakeAIRoutes(t, map[string]aiReply{}) // /anomalies answers 404

	if rec := getAnomalies(""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 until background training creates a model", rec.Code)
	}
}

func TestAnomaliesAIErrorReturns502(t *testing.T) {
	fakePrometheus(t, promRange(1, 2), "")
	fakeAIRoutes(t, map[string]aiReply{"/anomalies": {http.StatusInternalServerError, `{}`}})

	if rec := getAnomalies(""); rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestAnomaliesFailFastWhileBreakerIsOpen(t *testing.T) {
	b := newCircuitBreaker(1, time.Minute)
	call(t, b, outcomeFailure)
	useBreaker(t, b)
	fakePrometheus(t, promRange(1, 2), "")
	ai := fakeAIRoutes(t, map[string]aiReply{"/anomalies": {http.StatusOK, `{}`}})

	rec := getAnomalies("")

	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || ai.count() != 0 {
		t.Errorf("got %d (Retry-After %q, %d AI calls), want 503 with Retry-After and no AI call",
			rec.Code, rec.Header().Get("Retry-After"), ai.count())
	}
}

const anomalyFound = `{"checked":1,"anomalies":[{"time":"t","value":9,"expected":1,"lower":0,"upper":2,"deviation":8}],"trained_at":"x"}`

func TestCheckLatestAnomaliesExportsTheNewestSample(t *testing.T) {
	anomalyGauge.Reset()
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1, 9)), "")
	ai := fakeAIRoutes(t, map[string]aiReply{"/anomalies": {http.StatusOK, anomalyFound}})
	counter := anomaliesTotal.WithLabelValues("cpu", "a:9100")
	before := testutil.ToFloat64(counter)

	checkLatestAnomalies()

	if got := testutil.ToFloat64(anomalyGauge.WithLabelValues("cpu", "a:9100")); got != 1 {
		t.Errorf("cpu/a:9100 anomaly gauge = %v, want 1", got)
	}
	if got := testutil.ToFloat64(counter) - before; got != 1 {
		t.Errorf("cpu/a:9100 anomalies counted = %v, want 1", got)
	}
	calls := ai.snapshot()
	if len(calls) != len(metrics) {
		t.Fatalf("AI calls = %d, want one per metric (%d)", len(calls), len(metrics))
	}
	history, _ := calls[0].body["history"].([]any)
	if len(history) != 1 {
		t.Fatalf("history sent = %v, want only the newest sample", history)
	}
	if y := history[0].(map[string]any)["y"]; y != 9.0 {
		t.Errorf("checked y = %v, want 9 (the newest sample)", y)
	}
}

func TestCheckLatestAnomaliesExportsNormalSamplesAsZero(t *testing.T) {
	anomalyGauge.Reset()
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1)), "")
	fakeAIRoutes(t, map[string]aiReply{"/anomalies": {http.StatusOK, `{"checked":1,"anomalies":[]}`}})
	counter := anomaliesTotal.WithLabelValues("memory", "a:9100")
	before := testutil.ToFloat64(counter)

	checkLatestAnomalies()

	if got := testutil.ToFloat64(anomalyGauge.WithLabelValues("memory", "a:9100")); got != 0 {
		t.Errorf("memory/a:9100 anomaly gauge = %v, want 0", got)
	}
	if got := testutil.ToFloat64(counter) - before; got != 0 {
		t.Errorf("memory/a:9100 anomalies counted = %v, want 0", got)
	}
}

func TestCheckLatestAnomaliesSkipsSeriesWithoutAModel(t *testing.T) {
	anomalyGauge.WithLabelValues("cpu", "stale:9100").Set(1)
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1)), "")
	fakeAIRoutes(t, map[string]aiReply{}) // /anomalies answers 404

	checkLatestAnomalies()

	if n := testutil.CollectAndCount(anomalyGauge); n != 0 {
		t.Errorf("anomaly gauges = %d, want 0 (no models, and the stale one dropped)", n)
	}
}

func TestCheckLatestAnomaliesStopsWhenTheBreakerIsOpen(t *testing.T) {
	b := newCircuitBreaker(1, time.Minute)
	call(t, b, outcomeFailure)
	useBreaker(t, b)
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1)), "")
	ai := fakeAIRoutes(t, map[string]aiReply{"/anomalies": {http.StatusOK, anomalyFound}})

	checkLatestAnomalies()

	if ai.count() != 0 {
		t.Errorf("AI calls = %d, want 0 while the breaker is open", ai.count())
	}
}

func TestAnomalyIntervalFromEnv(t *testing.T) {
	tests := []struct {
		value string
		want  time.Duration
	}{
		{"", time.Minute},
		{"0", 0},
		{"30s", 30 * time.Second},
		{"soon", time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("ANOMALY_INTERVAL", tc.value)

			if got := anomalyIntervalFromEnv(); got != tc.want {
				t.Errorf("anomalyIntervalFromEnv() = %s, want %s", got, tc.want)
			}
		})
	}
}
