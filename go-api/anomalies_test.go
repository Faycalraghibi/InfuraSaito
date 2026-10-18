package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
