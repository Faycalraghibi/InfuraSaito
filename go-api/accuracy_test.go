package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory predictionStore.
type fakeStore struct {
	mu    sync.Mutex
	saved map[string][]storedPrediction // keyed by metric|instance
	err   error
}

func newFakeStore() *fakeStore { return &fakeStore{saved: map[string][]storedPrediction{}} }

func (f *fakeStore) save(_ context.Context, metric, instance string, preds []storedPrediction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.saved[metric+"|"+instance] = append(f.saved[metric+"|"+instance], preds...)
	return nil
}

func (f *fakeStore) load(_ context.Context, metric, instance string, from, to time.Time) ([]storedPrediction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []storedPrediction
	for _, p := range f.saved[metric+"|"+instance] {
		if !p.Target.Before(from) && !p.Target.After(to) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) count(metric, instance string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved[metric+"|"+instance])
}

func useStore(t *testing.T, s predictionStore) {
	t.Helper()
	old := forecasts
	forecasts = s
	t.Cleanup(func() { forecasts = old })
}

func at(minutes int) time.Time {
	return time.Unix(1700000000, 0).Add(time.Duration(minutes) * time.Minute).UTC()
}

func actual(minutes int, y float64) DataPoint {
	return DataPoint{Ds: at(minutes).Format(time.RFC3339), Y: y}
}

func TestCompareForecasts(t *testing.T) {
	preds := []storedPrediction{
		{Target: at(0), Value: 90, Lower: 80, Upper: 120},                 // actual 100: 10% off, inside band
		{Target: at(5).Add(time.Minute), Value: 60, Lower: 55, Upper: 58}, // actual 50 (1 min away): 20% off, outside band
		{Target: at(10), Value: 5, Lower: -1, Upper: 1},                   // actual 0: excluded from MAPE, inside band
		{Target: at(60), Value: 1, Lower: 0, Upper: 2},                    // no actual within tolerance
	}
	actuals := []DataPoint{actual(0, 100), actual(5, 50), actual(10, 0), actual(30, 7)}

	got := compareForecasts(preds, actuals, 150*time.Second)

	if got.Compared != 3 {
		t.Fatalf("compared = %d, want 3", got.Compared)
	}
	if got.MAPE == nil || math.Abs(*got.MAPE-15) > 1e-9 {
		t.Errorf("MAPE = %v, want 15 (mean of 10%% and 20%%; zero actual excluded)", got.MAPE)
	}
	if got.Coverage == nil || math.Abs(*got.Coverage-200.0/3) > 1e-9 {
		t.Errorf("coverage = %v, want 66.67 (2 of 3 inside the band)", got.Coverage)
	}
}

func TestCompareForecastsWithNothingToCompare(t *testing.T) {
	got := compareForecasts([]storedPrediction{{Target: at(0), Value: 1}}, []DataPoint{actual(60, 1)}, time.Minute)

	if got.Compared != 0 || got.MAPE != nil || got.Coverage != nil {
		t.Errorf("got %+v, want 0 compared with null MAPE and coverage", got)
	}
}

func getAccuracy(query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/forecast/accuracy?"+query, nil)
	rec := httptest.NewRecorder()
	accuracyHandler(rec, req)
	return rec
}

func TestAccuracyWithoutStorageReturns503(t *testing.T) {
	useStore(t, nil)

	if rec := getAccuracy(""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when DATABASE_URL is not configured", rec.Code)
	}
}

func TestAccuracyRejectsInvalidDays(t *testing.T) {
	useStore(t, newFakeStore())
	for _, query := range []string{"days=0", "days=31", "days=abc", "metric=bogus"} {
		if rec := getAccuracy(query); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", query, rec.Code)
		}
	}
}

func TestAccuracyComparesStoredForecastsWithActuals(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	store := newFakeStore()
	store.save(context.Background(), "cpu", "a:9100", []storedPrediction{
		{Target: now.Add(-10 * time.Minute), Value: 90, Lower: 80, Upper: 120},
	})
	useStore(t, store)
	// One actual point for a:9100, exactly at the prediction's target time.
	fakePrometheus(t, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"instance":"a:9100"},"values":[[`+
		jsonNumber(now.Add(-10*time.Minute).Unix())+`,"100"]]}]}}`, "")

	rec := getAccuracy("metric=cpu&days=1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var got accuracyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.MetricName != "cpu" || got.Instance != "a:9100" || got.Days != 1 || got.Compared != 1 {
		t.Errorf("response = %+v, want cpu/a:9100/1 day with 1 compared", got)
	}
	if got.MAPE == nil || math.Abs(*got.MAPE-10) > 1e-9 || got.Coverage == nil || *got.Coverage != 100 {
		t.Errorf("MAPE/coverage = %v/%v, want 10/100", got.MAPE, got.Coverage)
	}
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestRecordForecastsStoresCachedForecasts(t *testing.T) {
	store := newFakeStore()
	useStore(t, store)
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1), promSampleJSON("down:9100", 0)))
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, `{"metric_name":"x","horizon_minutes":60,"predictions":[
		{"time":"2026-10-18T12:05:00+00:00","value":1,"lower":0,"upper":2},
		{"time":"2026-10-18T12:10:00+00:00","value":2,"lower":1,"upper":3}]}`}})

	recordForecasts(context.Background())

	for name := range metrics {
		if n := store.count(name, "a:9100"); n != 2 {
			t.Errorf("%s/a:9100: stored %d predictions, want 2", name, n)
		}
		if n := store.count(name, "down:9100"); n != 0 {
			t.Errorf("%s/down:9100: stored %d predictions, want 0 (instance is down)", name, n)
		}
	}
}

func TestRecordForecastsStopsWhenTheBreakerIsOpen(t *testing.T) {
	b := newCircuitBreaker(1, time.Minute)
	call(t, b, outcomeFailure)
	useBreaker(t, b)
	useStore(t, newFakeStore())
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1)))
	ai := fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, `{}`}})

	recordForecasts(context.Background())

	if ai.count() != 0 {
		t.Errorf("AI calls = %d, want 0 while the breaker is open", ai.count())
	}
}

func TestToStoredPredictionsSkipsUnparseableTimes(t *testing.T) {
	got := toStoredPredictions([]forecastPoint{
		{Time: "2026-10-18T12:05:00+00:00", Value: 1, Lower: 0, Upper: 2},
		{Time: "t1", Value: 3},
	})

	if len(got) != 1 || got[0].Value != 1 || !got[0].Target.Equal(time.Date(2026, 10, 18, 12, 5, 0, 0, time.UTC)) {
		t.Errorf("got %+v, want only the parseable prediction", got)
	}
}
