package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestInstrumentCountsRequestsByRouteAndCode(t *testing.T) {
	counter := httpRequests.WithLabelValues("/test/teapot", "418")
	before := testutil.ToFloat64(counter)
	h := instrument("/test/teapot", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if got := testutil.ToFloat64(counter) - before; got != 2 {
		t.Errorf("requests counted = %v, want 2", got)
	}
}

func TestInstrumentDefaultsToStatus200(t *testing.T) {
	counter := httpRequests.WithLabelValues("/test/implicit", "200")
	before := testutil.ToFloat64(counter)
	h := instrument("/test/implicit", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok")) // no explicit WriteHeader
	})

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if got := testutil.ToFloat64(counter) - before; got != 1 {
		t.Errorf("requests counted with code 200 = %v, want 1", got)
	}
}

func TestRouterInstrumentsAPIRoutes(t *testing.T) {
	fakePrometheus(t, "", promEmptyVector)
	counter := httpRequests.WithLabelValues("/api/v1/instances", "200")
	before := testutil.ToFloat64(counter)

	newRouter("", nil).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil))

	if got := testutil.ToFloat64(counter) - before; got != 1 {
		t.Errorf("/api/v1/instances 200 count = %v, want 1", got)
	}
}

func TestRouterCountsRejectedRequests(t *testing.T) {
	unauthorized := httpRejected.WithLabelValues("unauthorized")
	limited := httpRejected.WithLabelValues("rate_limited")
	beforeUnauthorized, beforeLimited := testutil.ToFloat64(unauthorized), testutil.ToFloat64(limited)
	router := newRouter("s3cret", newTokenBucket(0.001, 1))

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil))

	if testutil.ToFloat64(unauthorized)-beforeUnauthorized != 1 || testutil.ToFloat64(limited)-beforeLimited != 1 {
		t.Errorf("rejected: unauthorized +%v, rate_limited +%v; want +1 each",
			testutil.ToFloat64(unauthorized)-beforeUnauthorized, testutil.ToFloat64(limited)-beforeLimited)
	}
}

func TestMetricsEndpointIsOpenWithoutAPIKey(t *testing.T) {
	rec := httptest.NewRecorder()

	newRouter("s3cret", newTokenBucket(0.001, 1)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200 without a key", rec.Code)
	}
	for _, name := range []string{"infurasaito_http_requests_total", "infurasaito_ai_breaker_state", "go_goroutines"} {
		if !strings.Contains(rec.Body.String(), name) {
			t.Errorf("/metrics is missing %s", name)
		}
	}
}

func TestBreakerStateGaugeFollowsTransitions(t *testing.T) {
	b, now := testBreaker(1, 10*time.Second)
	t.Cleanup(func() { breakerStateGauge.Set(0) })

	call(t, b, outcomeFailure)
	if got := testutil.ToFloat64(breakerStateGauge); got != float64(breakerOpen) {
		t.Errorf("gauge after opening = %v, want %d", got, breakerOpen)
	}
	*now = now.Add(10 * time.Second)
	gen, _ := b.allow()
	if got := testutil.ToFloat64(breakerStateGauge); got != float64(breakerHalfOpen) {
		t.Errorf("gauge while probing = %v, want %d", got, breakerHalfOpen)
	}
	b.record(gen, outcomeSuccess)
	if got := testutil.ToFloat64(breakerStateGauge); got != float64(breakerClosed) {
		t.Errorf("gauge after closing = %v, want %d", got, breakerClosed)
	}
}

func TestAIRequestDurationIsRecorded(t *testing.T) {
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, `{}`}})

	forecastFromCache("cpu", "", 60)

	if !strings.Contains(gatherText(t), `infurasaito_ai_request_duration_seconds_count{outcome="success",path="/forecast"}`) {
		t.Error("missing the /forecast success duration series")
	}
}

func TestTrainAllUpdatesTrainingMetrics(t *testing.T) {
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1, 2)), "")
	fakeAIRoutes(t, map[string]aiReply{"/train": {http.StatusOK, `{}`}})
	start := time.Now()

	trainAll()

	if got := testutil.ToFloat64(modelsTrained); got != float64(len(metrics)) {
		t.Errorf("models trained gauge = %v, want %d", got, len(metrics))
	}
	if got := testutil.ToFloat64(lastTraining); got < float64(start.Unix()) {
		t.Errorf("last training timestamp = %v, want at least %d", got, start.Unix())
	}
}

func TestRecordForecastsExportsThirtyMinuteForecast(t *testing.T) {
	useStore(t, nil) // gauges are exported even without storage
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1)))
	now := time.Now().UTC()
	var points []string
	for m := 5; m <= 60; m += 5 {
		points = append(points, fmt.Sprintf(`{"time":%q,"value":%d,"lower":%d,"upper":%d}`,
			now.Add(time.Duration(m)*time.Minute).Format(time.RFC3339), m, m-1, m+1))
	}
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK,
		`{"metric_name":"cpu","horizon_minutes":60,"predictions":[` + strings.Join(points, ",") + `]}`}})

	recordForecasts(context.Background())

	for bound, want := range map[string]float64{"value": 30, "lower": 29, "upper": 31} {
		if got := testutil.ToFloat64(forecastGauge.WithLabelValues("cpu", "a:9100", bound)); got != want {
			t.Errorf("cpu/a:9100 %s = %v, want %v (the prediction 30 minutes ahead)", bound, got, want)
		}
	}
}

func TestPredictionAt(t *testing.T) {
	base := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	preds := []forecastPoint{
		{Time: base.Add(25 * time.Minute).Format(time.RFC3339), Value: 25},
		{Time: base.Add(32 * time.Minute).Format(time.RFC3339), Value: 32},
		{Time: "not-a-time", Value: 99},
	}

	if p, ok := predictionAt(preds, base.Add(30*time.Minute), 5*time.Minute); !ok || p.Value != 32 {
		t.Errorf("predictionAt(+30m) = %+v, %v; want the +32m point", p, ok)
	}
	if _, ok := predictionAt(preds, base.Add(2*time.Hour), 5*time.Minute); ok {
		t.Error("predictionAt(+2h) found a point, want none within tolerance")
	}
}

// gatherText renders the metrics registry in the text exposition format.
func gatherText(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}
