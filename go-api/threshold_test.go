package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFirstCrossing(t *testing.T) {
	preds := []forecastPoint{
		{Time: "t0", Value: 50, Lower: 40, Upper: 60},
		{Time: "t1", Value: 60, Lower: 50, Upper: 75},
		{Time: "t2", Value: 81, Lower: 70, Upper: 90},
	}
	tests := []struct {
		name         string
		threshold    float64
		wantCross    bool
		wantExpected string // "" means null
		wantPossible string
	}{
		{"never", 95, false, "", ""},
		{"expected and possible", 80, true, "t2", "t2"},
		{"possible before expected", 70, true, "t2", "t1"},
		{"possible only", 85, false, "", "t2"},
		{"already at the first point", 50, true, "t0", "t0"},
		{"equal counts as reaching", 60, true, "t1", "t0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := firstCrossing(preds, tc.threshold)

			if got.WillCross != tc.wantCross || deref(got.ExpectedAt) != tc.wantExpected || deref(got.PossibleAt) != tc.wantPossible {
				t.Errorf("firstCrossing(%v) = {%v %q %q}, want {%v %q %q}", tc.threshold,
					got.WillCross, deref(got.ExpectedAt), deref(got.PossibleAt), tc.wantCross, tc.wantExpected, tc.wantPossible)
			}
		})
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func getThreshold(query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/forecast/threshold?"+query, nil)
	rec := httptest.NewRecorder()
	thresholdHandler(rec, req)
	return rec
}

func TestThresholdRequiresAFiniteThreshold(t *testing.T) {
	for _, query := range []string{"", "threshold=", "threshold=abc", "threshold=NaN", "threshold=Inf"} {
		t.Run(query, func(t *testing.T) {
			failIfAIServiceCalled(t)
			calls := fakePrometheus(t, promRange(1, 2), promInstant(1))

			rec := getThreshold(query)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if calls.count() != 0 {
				t.Errorf("Prometheus queried %d times, want 0", calls.count())
			}
		})
	}
}

func TestThresholdReportsCrossingFromForecast(t *testing.T) {
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1, 2)), promInstant(1))
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, `{"metric_name":"cpu","instance":"a:9100","horizon_minutes":10,
		"predictions":[{"time":"t1","value":50,"lower":40,"upper":70},{"time":"t2","value":85,"lower":75,"upper":95}]}`}})

	rec := getThreshold("metric=cpu&threshold=65&horizon_minutes=10")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var body thresholdResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body.MetricName != "cpu" || body.Instance != "a:9100" || body.Threshold != 65 || body.HorizonMinutes != 10 {
		t.Errorf("header fields = %+v, want cpu/a:9100/65/10", body)
	}
	if !body.WillCross || deref(body.ExpectedAt) != "t2" || deref(body.PossibleAt) != "t1" {
		t.Errorf("crossing = {%v %q %q}, want {true t2 t1}", body.WillCross, deref(body.ExpectedAt), deref(body.PossibleAt))
	}
}

func TestThresholdNoCrossingHasNullTimes(t *testing.T) {
	fakePrometheus(t, promRange(1, 2), promInstant(1))
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, `{"metric_name":"cpu","horizon_minutes":60,
		"predictions":[{"time":"t1","value":10,"lower":5,"upper":15}]}`}})

	rec := getThreshold("threshold=90")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var raw map[string]any
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if raw["will_cross"] != false || raw["expected_at"] != nil || raw["possible_at"] != nil {
		t.Errorf("body = %v, want will_cross false with null times", raw)
	}
	if _, present := raw["expected_at"]; !present {
		t.Errorf("body = %v, want expected_at present as null", raw)
	}
}

func TestThresholdUsesColdStartFallback(t *testing.T) {
	failIfAIServiceCalled(t)
	fakePrometheusByQuery(t, promRange(), map[string]string{
		nodeUpQuery:                 promInstant(1),
		metrics["cpu"].instantQuery: promInstant(92),
	})

	rec := getThreshold("threshold=90")

	var body thresholdResponse
	json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusOK || !body.WillCross || body.ExpectedAt == nil {
		t.Errorf("got %d %+v, want 200 with the current value 92 already over 90", rec.Code, body)
	}
}

func TestThresholdPassesForecastErrorsThrough(t *testing.T) {
	t.Run("unknown metric", func(t *testing.T) {
		if rec := getThreshold("metric=bogus&threshold=1"); rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
	t.Run("AI failure", func(t *testing.T) {
		fakePrometheus(t, promRange(1, 2), promInstant(1))
		fakeAIRoutes(t, map[string]aiReply{"/predict": {http.StatusInternalServerError, `{}`}})

		if rec := getThreshold("threshold=1"); rec.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", rec.Code)
		}
	})
	t.Run("malformed AI response", func(t *testing.T) {
		fakePrometheus(t, promRange(1, 2), promInstant(1))
		fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, `not json`}})

		if rec := getThreshold("threshold=1"); rec.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", rec.Code)
		}
	})
}
