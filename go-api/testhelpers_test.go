package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain points the AI client at an unreachable address so a test that forgets
// fakeAIService can never hit a real AI service running on localhost:5000. It also
// disables the circuit breaker so failures in one test can't open it for the next;
// breaker tests opt in with useBreaker.
func TestMain(m *testing.M) {
	aiServiceURL = "http://127.0.0.1:0"
	aiBreaker = nil
	os.Exit(m.Run())
}

// useBreaker installs b as the AI circuit breaker for the test.
func useBreaker(t *testing.T, b *circuitBreaker) {
	t.Helper()
	old := aiBreaker
	aiBreaker = b
	t.Cleanup(func() { aiBreaker = old })
}

const promEmptyVector = `{"status":"success","data":{"resultType":"vector","result":[]}}`

// promLabels renders the metric labels for a series: {} for "", else {"instance":...}.
func promLabels(instance string) string {
	if instance == "" {
		return "{}"
	}
	return fmt.Sprintf(`{"instance":%q}`, instance)
}

// promSeriesJSON builds one matrix series for instance, points 5 minutes apart.
func promSeriesJSON(instance string, values ...float64) string {
	pts := make([]string, len(values))
	for i, v := range values {
		pts[i] = fmt.Sprintf(`[%d,"%g"]`, 1700000000+i*300, v)
	}
	return fmt.Sprintf(`{"metric":%s,"values":[%s]}`, promLabels(instance), strings.Join(pts, ","))
}

// promMatrix builds a query_range response from promSeriesJSON series.
func promMatrix(series ...string) string {
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[%s]}}`, strings.Join(series, ","))
}

// promRange builds a query_range response with one unlabelled series, 5 minutes apart.
func promRange(values ...float64) string {
	if len(values) == 0 {
		return promMatrix()
	}
	return promMatrix(promSeriesJSON("", values...))
}

// promSampleJSON builds one vector sample for instance.
func promSampleJSON(instance string, v float64) string {
	return fmt.Sprintf(`{"metric":%s,"value":[1700000000,"%g"]}`, promLabels(instance), v)
}

// promVector builds an instant-query response from promSampleJSON samples.
func promVector(samples ...string) string {
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[%s]}}`, strings.Join(samples, ","))
}

// promInstant builds an instant-query response with one unlabelled sample.
func promInstant(v float64) string {
	return promVector(promSampleJSON("", v))
}

// promCalls records the PromQL queries the fake Prometheus received, in order.
type promCalls struct {
	mu      sync.Mutex
	queries []string
}

func (c *promCalls) record(query string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, query)
}

// snapshot returns a copy of the recorded queries.
func (c *promCalls) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.queries...)
}

func (c *promCalls) count() int { return len(c.snapshot()) }

// fakePrometheus serves rangeBody on /api/v1/query_range and instantBody on /api/v1/query,
// and points prometheusURL at it for the test. An empty body makes that endpoint return 500.
func fakePrometheus(t *testing.T, rangeBody, instantBody string) *promCalls {
	t.Helper()
	calls := &promCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.record(r.URL.Query().Get("query"))
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
	return calls
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

// fakeSlowServer returns the URL of a server that waits for delay (or until the client
// gives up) before answering 200 with an empty body.
func fakeSlowServer(t *testing.T, delay time.Duration) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // net/http only notices a disconnect once the body is read
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// useClient replaces *target (promClient or aiClient) with a client using timeout for the test.
func useClient(t *testing.T, target **http.Client, timeout time.Duration) {
	t.Helper()
	old := *target
	*target = &http.Client{Timeout: timeout}
	t.Cleanup(func() { *target = old })
}

// getCurrent calls currentMetricsHandler with the given raw query string.
func getCurrent(query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/current?"+query, nil)
	rec := httptest.NewRecorder()
	currentMetricsHandler(rec, req)
	return rec
}

// fakePrometheusByQuery is like fakePrometheus, but answers instant queries by PromQL:
// instant maps a query to its response body. Unknown queries get HTTP 500.
func fakePrometheusByQuery(t *testing.T, rangeBody string, instant map[string]string) *promCalls {
	t.Helper()
	calls := &promCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		calls.record(query)
		body := rangeBody
		if r.URL.Path == "/api/v1/query" {
			body = instant[query]
		}
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
	return calls
}

// getInstances calls instancesHandler.
func getInstances() *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	rec := httptest.NewRecorder()
	instancesHandler(rec, req)
	return rec
}

// aiReply is a canned response from the fake AI service.
type aiReply struct {
	status int
	body   string
}

// aiCall is one request the fake AI service received.
type aiCall struct {
	path string
	body map[string]any
}

// aiCalls records the requests the fake AI service received, in order.
type aiCalls struct {
	mu    sync.Mutex
	calls []aiCall
}

func (c *aiCalls) record(call aiCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

// snapshot returns a copy of the recorded calls.
func (c *aiCalls) snapshot() []aiCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]aiCall(nil), c.calls...)
}

func (c *aiCalls) count() int { return len(c.snapshot()) }

func (c *aiCalls) paths() []string {
	calls := c.snapshot()
	paths := make([]string, len(calls))
	for i, call := range calls {
		paths[i] = call.path
	}
	return paths
}

// fakeAIRoutes answers each path with its reply (unknown paths get 404), records every
// request, and points aiServiceURL at it.
func fakeAIRoutes(t *testing.T, routes map[string]aiReply) *aiCalls {
	t.Helper()
	recorded := &aiCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		recorded.record(aiCall{path: r.URL.Path, body: body})

		reply, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(reply.status)
		fmt.Fprint(w, reply.body)
	}))
	t.Cleanup(srv.Close)

	old := aiServiceURL
	aiServiceURL = srv.URL
	t.Cleanup(func() { aiServiceURL = old })
	return recorded
}
