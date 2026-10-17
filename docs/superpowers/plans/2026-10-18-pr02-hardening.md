# PR 2: Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Go API robust against slow dependencies and bad input: HTTP timeouts, a 7-day cap on `horizon_minutes` in both services, one metric definition table shared by `/forecast` and `/metrics/current`, and no index panics on malformed Prometheus data.

**Architecture:** Two package-level `*http.Client`s (`promClient`, `aiClient`) with timeouts set from env vars replace the `http.Get`/`http.Post` defaults, which never time out. Metric definitions move to `go-api/metrics.go` as `map[string]metricDef{rangeQuery, instantQuery}` behind one `resolveMetric` helper, so PR 3 only has to add entries. Test helpers gain query capture (`fakePrometheus` returns the queries it received) and a slow server.

**Tech Stack:** Go 1.22 stdlib; Python 3.11, FastAPI/Pydantic v2, pytest.

**Branch:** `fix/hardening` (from `main` after PR #2 is merged)

---

## Running tests locally (Git Bash, repo root)

```bash
# GO_TEST
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W)/go-api:/src" -w /src golang:1.22 sh -c "go vet ./... && go test -race ./..."
# PY_TEST
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W)/ai-service:/app" -w /app python:3.11-slim \
  sh -c "pip install -q -r requirements.txt -r requirements-dev.txt && pytest -q"
```

Local `gofmt -l` flags `handlers.go` and `prometheus.go` because of Windows CRLF checkouts. Git stores LF, so CI is unaffected. Only check new or rewritten files.

## File map

| File | Action | Responsibility |
|---|---|---|
| `go-api/httpclient.go` | Create | `promClient`, `aiClient`, `durationFromEnv` |
| `go-api/httpclient_test.go` | Create | `durationFromEnv` tests |
| `go-api/metrics.go` | Create | `metricDef`, `metrics` table, `supportedMetrics`, `resolveMetric` |
| `go-api/handlers.go` | Modify | Use `resolveMetric`, horizon cap, `/metrics/current` per metric |
| `go-api/prometheus.go` | Modify | Use `promClient`; guard row and value lengths |
| `go-api/ai_client.go` | Modify | Use `aiClient` |
| `go-api/testhelpers_test.go` | Modify | `fakePrometheus` records queries; `fakeSlowServer`; `useClient`; `getCurrent` |
| `go-api/prometheus_test.go` | Modify | Malformed-length and timeout tests |
| `go-api/handlers_test.go` | Modify | Horizon cap, metric routing, `/metrics/current`, timeout status tests |
| `ai-service/main.py` | Modify | `horizon_minutes: Field(60, gt=0, le=10080)` |
| `ai-service/tests/test_main.py` | Modify | 422 tests |
| `README.md` | Modify | Env vars, horizon limit, `/metrics/current?metric=` |

---

### Task 1: Test helpers: query capture, slow server, client swap

**Files:**
- Modify: `go-api/testhelpers_test.go`

- [ ] **Step 1: Make `fakePrometheus` record queries**

Replace the existing `fakePrometheus` with the version below. Existing callers ignore the return value, which Go allows, so no other test changes.

```go
// promCalls records the PromQL queries the fake Prometheus received, in order.
type promCalls struct {
	queries []string
}

// fakePrometheus serves rangeBody on /api/v1/query_range and instantBody on /api/v1/query,
// and points prometheusURL at it for the test. An empty body makes that endpoint return 500.
func fakePrometheus(t *testing.T, rangeBody, instantBody string) *promCalls {
	t.Helper()
	calls := &promCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.queries = append(calls.queries, r.URL.Query().Get("query"))
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
```

- [ ] **Step 2: Add the slow server, client swap and `/metrics/current` caller**

Add `"time"` to the imports and append:

```go
// fakeSlowServer returns the URL of a server that waits for delay (or until the client
// gives up) before answering 200 with an empty body.
func fakeSlowServer(t *testing.T, delay time.Duration) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
```

`useClient` won't compile until Task 2 defines `promClient` and `aiClient`, so do Task 2 before running tests. Commit Tasks 1 and 2 together.

---

### Task 2: HTTP clients with timeouts

**Files:**
- Create: `go-api/httpclient.go`, `go-api/httpclient_test.go`
- Modify: `go-api/prometheus.go`, `go-api/ai_client.go`, `go-api/prometheus_test.go`, `go-api/handlers_test.go`

- [ ] **Step 1: Write the failing tests**

Create `go-api/httpclient_test.go`:

```go
package main

import (
	"testing"
	"time"
)

func TestDurationFromEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"unset uses default", "", 7 * time.Second},
		{"valid duration", "250ms", 250 * time.Millisecond},
		{"garbage uses default", "soon", 7 * time.Second},
		{"zero uses default", "0s", 7 * time.Second},
		{"negative uses default", "-1s", 7 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEST_TIMEOUT", tc.value)

			if got := durationFromEnv("TEST_TIMEOUT", 7*time.Second); got != tc.want {
				t.Errorf("durationFromEnv(%q) = %s, want %s", tc.value, got, tc.want)
			}
		})
	}
}
```

Append to `go-api/prometheus_test.go`:

```go
func TestQueryPrometheusRangeTimesOut(t *testing.T) {
	useClient(t, &promClient, 50*time.Millisecond)
	prometheusURL = fakeSlowServer(t, 5*time.Second)

	start := time.Now()
	_, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s, want the 50ms client timeout to apply", elapsed)
	}
}
```

`prometheusURL` must be restored after the test. Add `old := prometheusURL; t.Cleanup(func() { prometheusURL = old })` before overwriting it.

Append to `go-api/handlers_test.go` (add `"time"` to imports):

```go
func TestForecastAIServiceTimeoutReturns502(t *testing.T) {
	fakePrometheus(t, promRange(1, 2), promInstant(1))
	useClient(t, &aiClient, 50*time.Millisecond)
	old := aiServiceURL
	aiServiceURL = fakeSlowServer(t, 5*time.Second)
	t.Cleanup(func() { aiServiceURL = old })

	if rec := getForecast(""); rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `GO_TEST`
Expected: build failure, because `undefined: durationFromEnv`, `promClient` and `aiClient` don't exist yet.

- [ ] **Step 3: Create `go-api/httpclient.go`**

```go
package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

// Clients for outbound calls. http.Get/http.Post use a client with no timeout,
// so a hung dependency would hang the request forever.
var (
	promClient = &http.Client{Timeout: durationFromEnv("PROM_TIMEOUT", 10*time.Second)}
	aiClient   = &http.Client{Timeout: durationFromEnv("AI_TIMEOUT", 60*time.Second)}
)

// durationFromEnv parses envVar as a Go duration (e.g. "30s"), returning def when it
// is unset, invalid or not positive.
func durationFromEnv(envVar string, def time.Duration) time.Duration {
	v := os.Getenv(envVar)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("invalid %s=%q, using %s", envVar, v, def)
		return def
	}
	return d
}
```

- [ ] **Step 4: Use the clients**

In `go-api/prometheus.go`, replace both `http.Get(apiQuery)` calls with `promClient.Get(apiQuery)`. In `go-api/ai_client.go`, replace `http.Post(aiServiceURL+"/predict", ...)` with `aiClient.Post(aiServiceURL+"/predict", ...)`, keeping the same arguments. Remove `"net/http"` from an import list only if the compiler reports it unused (`ai_client.go` still uses `http.StatusOK`).

- [ ] **Step 5: Run to verify they pass**

Run: `GO_TEST`
Expected: `ok`. Each timeout test finishes in well under a second.

- [ ] **Step 6: Commit (Tasks 1 and 2)**

```bash
git add go-api/httpclient.go go-api/httpclient_test.go go-api/prometheus.go go-api/ai_client.go \
        go-api/testhelpers_test.go go-api/prometheus_test.go go-api/handlers_test.go
git commit -m "fix(go-api): add timeouts to Prometheus and AI service calls"
```

---

### Task 3: Guard malformed Prometheus data

**Files:**
- Modify: `go-api/prometheus.go`, `go-api/prometheus_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `go-api/prometheus_test.go`:

```go
func TestQueryPrometheusRangeSkipsShortRows(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` +
		`[1700000000,"1"],[1700000300],[]]}]}}`
	fakePrometheus(t, body, "")

	pts, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil || len(pts) != 1 || pts[0].Y != 1 {
		t.Errorf("got (%+v, %v), want only the one complete row", pts, err)
	}
}

func TestQueryPrometheusShortValueIsError(t *testing.T) {
	fakePrometheus(t, "", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000]}]}}`)

	if _, err := queryPrometheus("q"); err == nil {
		t.Error("want error for a value without a sample, got nil")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `GO_TEST`
Expected: both tests PANIC with `index out of range`.

- [ ] **Step 3: Add the guards**

In `queryPrometheusRange`, at the top of the `for _, valArr := range ...Values` loop body, before the type assertions:

```go
		if len(valArr) < 2 {
			continue // Skip rows without both timestamp and value
		}
```

In `queryPrometheus`, replace:

```go
	// Value is typically [timestamp, "string_value"]
	valStr, ok := promResp.Data.Result[0].Value[1].(string)
```

with:

```go
	// Value is typically [timestamp, "string_value"]
	value := promResp.Data.Result[0].Value
	if len(value) < 2 {
		return 0, fmt.Errorf("unexpected value format from Prometheus")
	}
	valStr, ok := value[1].(string)
```

- [ ] **Step 4: Run to verify they pass**

Run: `GO_TEST`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add go-api/prometheus.go go-api/prometheus_test.go
git commit -m "fix(go-api): skip short Prometheus rows instead of panicking"
```

---

### Task 4: Shared metric table and `/metrics/current?metric=`

**Files:**
- Create: `go-api/metrics.go`
- Modify: `go-api/handlers.go`, `go-api/handlers_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `go-api/handlers_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify they fail**

Run: `GO_TEST`
Expected: build failure, `undefined: metrics`.

- [ ] **Step 3: Create `go-api/metrics.go`**

```go
package main

import (
	"net/http"
	"sort"
	"strings"
)

// metricDef holds the PromQL for one supported metric.
type metricDef struct {
	rangeQuery   string // history for forecasting, evaluated with query_range at a 5m step
	instantQuery string // current value
}

// metrics maps the supported `metric` query values to their PromQL.
var metrics = map[string]metricDef{
	"cpu": {
		rangeQuery:   `100 - (avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)`,
		instantQuery: `100 - (avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[1m])) * 100)`,
	},
	"memory": {
		rangeQuery:   `100 * (1 - avg by (instance) (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes))`,
		instantQuery: `100 * (1 - avg by (instance) (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes))`,
	},
}

func supportedMetrics() string {
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// resolveMetric reads the `metric` query parameter (default "cpu"). For an unsupported
// value it writes a 400 response and returns ok=false.
func resolveMetric(w http.ResponseWriter, r *http.Request) (name string, def metricDef, ok bool) {
	name = r.URL.Query().Get("metric")
	if name == "" {
		name = "cpu"
	}
	def, ok = metrics[name]
	if !ok {
		http.Error(w, "Unsupported metric; supported: "+supportedMetrics(), http.StatusBadRequest)
	}
	return name, def, ok
}
```

- [ ] **Step 4: Update `go-api/handlers.go`**

Remove from `handlers.go`: the `metricQueries` map and its comment, the `supportedMetrics` function, and the now-unused `"sort"` and `"strings"` imports.

In `forecastHandler`, replace:

```go
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		metric = "cpu"
	}

	query, ok := metricQueries[metric]
	if !ok {
		http.Error(w, "Unsupported metric; supported: "+supportedMetrics(), http.StatusBadRequest)
		return
	}
```

with:

```go
	metric, def, ok := resolveMetric(w, r)
	if !ok {
		return
	}
```

and change `queryPrometheusRange(query, start, end, step)` to `queryPrometheusRange(def.rangeQuery, start, end, step)`.

Replace the whole `currentMetricsHandler` with:

```go
func currentMetricsHandler(w http.ResponseWriter, r *http.Request) {
	metric, def, ok := resolveMetric(w, r)
	if !ok {
		return
	}

	val, err := queryPrometheus(def.instantQuery)
	if err != nil {
		http.Error(w, "Failed to fetch metrics: "+err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"metric": metric,
		"value":  val,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
```

- [ ] **Step 5: Run to verify they pass**

Run: `GO_TEST`
Expected: `ok`. `TestForecastRejectsUnsupportedMetric` still passes, because the message text is unchanged.

- [ ] **Step 6: Commit**

```bash
git add go-api/metrics.go go-api/handlers.go go-api/handlers_test.go
git commit -m "feat(go-api): share metric definitions between forecast and current endpoints"
```

---

### Task 5: Cap `horizon_minutes` in the Go API

**Files:**
- Modify: `go-api/handlers.go`, `go-api/handlers_test.go`

- [ ] **Step 1: Write the failing tests**

In `go-api/handlers_test.go`, rename `TestForecastInvalidHorizonDefaultsTo60` to `TestForecastHorizonParsing` and add two rows to its table:

```go
		{"horizon_minutes=1", 1},
		{"horizon_minutes=10080", 10080},
```

Append:

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `GO_TEST`
Expected: `TestForecastRejectsHorizonAboveSevenDays` fails (status 200). The two new parsing rows pass.

- [ ] **Step 3: Add the cap**

In `go-api/handlers.go`, above `forecastHandler`, add:

```go
// maxHorizonMinutes caps forecasts at 7 days; longer horizons are not meaningful for
// 14 days of history and would make Prophet build very large frames.
const maxHorizonMinutes = 7 * 24 * 60
```

Replace the horizon parsing block:

```go
	horizonStr := r.URL.Query().Get("horizon_minutes")
	horizonMin := 60
	if h, err := strconv.Atoi(horizonStr); err == nil && h > 0 {
		horizonMin = h
	}
```

with:

```go
	horizonStr := r.URL.Query().Get("horizon_minutes")
	horizonMin := 60
	if h, err := strconv.Atoi(horizonStr); err == nil && h > 0 {
		if h > maxHorizonMinutes {
			http.Error(w, fmt.Sprintf("horizon_minutes must be at most %d", maxHorizonMinutes), http.StatusBadRequest)
			return
		}
		horizonMin = h
	}
```

Add `"fmt"` to the imports.

- [ ] **Step 4: Run to verify it passes**

Run: `GO_TEST`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add go-api/handlers.go go-api/handlers_test.go
git commit -m "fix(go-api): reject forecast horizons longer than 7 days"
```

---

### Task 6: Cap `horizon_minutes` in the AI service

**Files:**
- Modify: `ai-service/main.py`, `ai-service/tests/test_main.py`

- [ ] **Step 1: Write the failing tests**

Append to `ai-service/tests/test_main.py` (add `import pytest` if it isn't already imported):

```python
@pytest.mark.parametrize("horizon", [0, -5, 10081])
def test_predict_rejects_out_of_range_horizon(client, history, horizon):
    resp = client.post(
        "/predict",
        json={"metric_name": "cpu", "history": history[:2], "horizon_minutes": horizon},
    )

    assert resp.status_code == 422
```

- [ ] **Step 2: Run to verify it fails**

Run: `PY_TEST`
Expected: 3 new cases FAIL. They return 200, or 500 for negative values, instead of 422.

- [ ] **Step 3: Constrain the field**

In `ai-service/main.py`, in `PredictRequest`, replace:

```python
    horizon_minutes: int = Field(default=60, description="How far ahead to predict (minutes)")
```

with:

```python
    horizon_minutes: int = Field(
        default=60, gt=0, le=10080, description="How far ahead to predict (minutes, at most 7 days)"
    )
```

- [ ] **Step 4: Run to verify it passes**

Run: `PY_TEST`
Expected: all pass (10 earlier + 3 new = 13).

- [ ] **Step 5: Commit**

```bash
git add ai-service/main.py ai-service/tests/test_main.py
git commit -m "fix(ai-service): reject horizons outside 1..10080 minutes"
```

---

### Task 7: Script robustness and CI polish (PR 1 review follow-ups)

**Files:**
- Create: `.gitattributes`
- Modify: `clean_setup.sh`, `.github/workflows/ci.yml`

- [ ] **Step 1: Create `.gitattributes`**

Without this, Windows clones check out Go and shell files as CRLF, so a local `gofmt -l .` disagrees with CI.

```gitattributes
*.go text eol=lf
*.sh text eol=lf
*.yml text eol=lf
```

Then refresh the CRLF working copies. The index already stores LF. Only do this with a clean working tree (`git status --short` empty apart from `.gitattributes`), because it re-checks files out from the index:

```bash
git add .gitattributes
git add --renormalize .          # expect no changes besides .gitattributes
rm go-api/*.go clean_setup.sh .github/workflows/ci.yml
git checkout -- go-api clean_setup.sh .github/workflows/ci.yml
```

Verify: `git ls-files --eol go-api/handlers.go` shows `w/lf`.

- [ ] **Step 2: Make the AI check in `clean_setup.sh` fail loudly**

Replace the `Waiting for AI Service...` loop with:

```bash
echo "Waiting for AI Service..."
AI_READY=false
for attempt in $(seq 1 24); do
    if curl -sf http://localhost:5000/health > /dev/null; then
        AI_READY=true
        break
    fi
    sleep 5
done

if [ "$AI_READY" = false ]; then
    echo "ERROR: AI Service did not become healthy."
    docker compose logs ai-service
    exit 1
fi
```

Replace the `AI_RESPONSE=$(curl ...)` assignment, including the stray spaces, with:

```bash
AI_RESPONSE=$(curl -s --max-time 120 -X POST -H "Content-Type: application/json" \
    --data-binary @/tmp/predict_payload.json http://localhost:5000/predict) || true
```

- [ ] **Step 3: Disable setup-go's module cache**

The module has no `go.sum`, so the cache only produces a warning. In `.github/workflows/ci.yml`, under the `actions/setup-go@v7` step's `with:`, add `cache: false`.

- [ ] **Step 4: Verify and commit**

Run: `bash ./clean_setup.sh`, expecting both "Passed!" lines, then `docker compose down -v`.

```bash
git add .gitattributes clean_setup.sh .github/workflows/ci.yml
git commit -m "chore: enforce LF line endings and make the AI integration check fail loudly"
```

---

### Task 8: Docs

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Update the endpoint docs**

Under `### GET /api/v1/forecast`, replace the `horizon_minutes` bullet with:

```markdown
  * `horizon_minutes` (default: `60`, max: `10080` = 7 days). Larger values return `400`; non-numeric or non-positive values fall back to `60`.
```

Replace the `### GET /api/v1/metrics/current` section body with:

```markdown
Returns the current value of a metric.
* **Parameters**:
  * `metric` (default: `cpu`): `cpu` or `memory`. Other values return `400`.
* **Example**: `curl "http://localhost:8080/api/v1/metrics/current?metric=memory"`
```

Add a section before `## Running Tests`:

```markdown
## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `PROMETHEUS_URL` | `http://localhost:9090` | Prometheus base URL |
| `AI_SERVICE_URL` | `http://localhost:5000` | AI service base URL |
| `PROM_TIMEOUT` | `10s` | Timeout for each Prometheus request (Go duration) |
| `AI_TIMEOUT` | `60s` | Timeout for each AI service request (Go duration) |
| `PORT` | `8080` | Go API listen port |

```

- [ ] **Step 2: Commit, push, open the PR**

```bash
git add README.md
git commit -m "docs: document timeouts, horizon limit and current-metric parameter"
git push -u origin fix/hardening
gh pr create --title "Hardening: timeouts, input limits, shared metric table" --body "<summary>"
```

- [ ] **Step 3: Confirm CI is green**

Run: `gh pr checks --watch`
Expected: `go-unit`, `py-unit` and `test-mvp-stack` pass.
