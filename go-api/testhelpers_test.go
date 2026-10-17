package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestMain points the AI client at an unreachable address so a test that forgets
// fakeAIService can never hit a real AI service running on localhost:5000.
func TestMain(m *testing.M) {
	aiServiceURL = "http://127.0.0.1:0"
	os.Exit(m.Run())
}

const promEmptyVector = `{"status":"success","data":{"resultType":"vector","result":[]}}`

// promRange builds a query_range response with one series, 5 minutes apart.
func promRange(values ...float64) string {
	pts := make([]string, len(values))
	for i, v := range values {
		pts[i] = fmt.Sprintf(`[%d,"%g"]`, 1700000000+i*300, v)
	}
	if len(pts) == 0 {
		return `{"status":"success","data":{"resultType":"matrix","result":[]}}`
	}
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[%s]}]}}`,
		strings.Join(pts, ","))
}

// promInstant builds an instant-query response with one sample.
func promInstant(v float64) string {
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"%g"]}]}}`, v)
}

// fakePrometheus serves rangeBody on /api/v1/query_range and instantBody on /api/v1/query,
// and points prometheusURL at it for the test. An empty body makes that endpoint return 500.
func fakePrometheus(t *testing.T, rangeBody, instantBody string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{
			"/api/v1/query_range": rangeBody,
			"/api/v1/query":       instantBody,
		}[r.URL.Path]
		if body == "" {
			http.Error(w, "fake prometheus error", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	old := prometheusURL
	prometheusURL = srv.URL
	t.Cleanup(func() { prometheusURL = old })
}

// fakeAIService replies to /predict with status and body, and points aiServiceURL at it.
// The returned PredictRequest is filled in with what the handler sent.
func fakeAIService(t *testing.T, status int, body string) *PredictRequest {
	t.Helper()
	got := &PredictRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/predict" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Errorf("AI service got invalid JSON: %v", err)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	old := aiServiceURL
	aiServiceURL = srv.URL
	t.Cleanup(func() { aiServiceURL = old })
	return got
}

// failIfAIServiceCalled points aiServiceURL at a server that fails the test if it is called.
func failIfAIServiceCalled(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("AI service should not be called, got %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected call", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	old := aiServiceURL
	aiServiceURL = srv.URL
	t.Cleanup(func() { aiServiceURL = old })
}

// getForecast calls forecastHandler with the given raw query string.
func getForecast(query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/forecast?"+query, nil)
	rec := httptest.NewRecorder()
	forecastHandler(rec, req)
	return rec
}
