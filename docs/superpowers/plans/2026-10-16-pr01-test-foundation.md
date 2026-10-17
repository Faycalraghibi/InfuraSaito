# PR 1: Test Foundation and Real Forecast Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add unit test suites for both services and CI jobs that run them, and fix the two bugs that make the real (non-fallback) forecast path fail.

**Architecture:** Python tests use pytest with FastAPI's `TestClient` and a real Prophet fit on 3 days of synthetic 5-minute data (about 2s). Go tests are in `package main` and swap the `prometheusURL` and `aiServiceURL` globals for `httptest` servers. CI gets two fast unit jobs, and the Docker integration job runs only after both pass. The integration job also sends a real Prophet forecast through the AI service.

**Tech Stack:** Go 1.22 `testing` and `net/http/httptest`; Python 3.11, pytest 8, httpx (for `TestClient`); GitHub Actions `setup-go` and `setup-python`.

**Branch:** `test/foundation` (from `main` after PR 0 is merged)

---

## Background: the two bugs

1. **Dependency drift.** `requirements.txt` pins `prophet==1.1.6` but leaves `cmdstanpy` unpinned and allows `pandas>=2.2.3` and `numpy>=2.0.0`. A fresh install resolves `cmdstanpy 1.3.0` and `pandas 3.x`, and `Prophet()` then raises `AttributeError: 'Prophet' object has no attribute 'stan_backend'`. Verified working set: `cmdstanpy==1.2.5`, `pandas==2.2.3`, `numpy==2.2.6`.
2. **Timezone.** Go sends `ds` as RFC3339 UTC (`2026-01-01T00:00:00Z`). `pd.to_datetime` makes that timezone-aware, and Prophet raises `ValueError: Column ds has timezone specified, which is not supported. Remove timezone.` Fix: parse as UTC, then drop the timezone before fitting, and add it back on output.

## Running tests locally

Go is not installed on the dev machine, and Prophet has no wheels for the local Python 3.14. Use Docker (run from the repo root in Git Bash):

```bash
# Go
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W)/go-api:/src" -w /src golang:1.22 go test ./...

# Python
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W)/ai-service:/app" -w /app python:3.11-slim \
  sh -c "pip install -q -r requirements.txt -r requirements-dev.txt && pytest -q"
```

The rest of this plan writes these as `GO_TEST` and `PY_TEST`.

## File map

| File | Action | Responsibility |
|---|---|---|
| `ai-service/requirements.txt` | Modify | Pin Prophet's transitive dependencies |
| `ai-service/requirements-dev.txt` | Create | pytest and httpx |
| `ai-service/pytest.ini` | Create | Test path and import path |
| `ai-service/tests/conftest.py` | Create | Shared `history` fixture (Go-format UTC timestamps) |
| `ai-service/tests/test_model.py` | Create | `ForecastModel` tests |
| `ai-service/tests/test_main.py` | Create | FastAPI endpoint tests |
| `ai-service/model.py` | Modify | Timezone normalisation |
| `go-api/testhelpers_test.go` | Create | Fake Prometheus and AI servers, response builders |
| `go-api/prometheus_test.go` | Create | `queryPrometheusRange` and `queryPrometheus` tests |
| `go-api/handlers_test.go` | Create | `forecastHandler` tests |
| `.github/workflows/ci.yml` | Modify | `go-unit` and `py-unit` jobs; integration job needs them |
| `clean_setup.sh` | Modify | Real Prophet forecast check against the AI service |

---

### Task 1: Python test scaffolding

**Files:**
- Create: `ai-service/requirements-dev.txt`
- Create: `ai-service/pytest.ini`
- Create: `ai-service/tests/conftest.py`

- [ ] **Step 1: Create `ai-service/requirements-dev.txt`**

```text
pytest==8.3.3
httpx==0.27.2
```

- [ ] **Step 2: Create `ai-service/pytest.ini`**

`testpaths` keeps pytest away from the manual script `test_predict.py`, which is not a test suite. `pythonpath = .` lets tests `import model` and `import main`.

```ini
[pytest]
testpaths = tests
pythonpath = .
```

- [ ] **Step 3: Create `ai-service/tests/conftest.py`**

```python
import math
from datetime import datetime, timedelta, timezone

import pytest


@pytest.fixture
def history() -> list[dict]:
    """3 days of 5-minute points with a daily sine pattern, timestamped like the Go API sends them."""
    start = datetime(2026, 1, 1, tzinfo=timezone.utc)
    return [
        {
            "ds": (start + timedelta(minutes=5 * i)).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "y": 30 + 10 * math.sin(2 * math.pi * i / 288),
        }
        for i in range(3 * 288)
    ]
```

- [ ] **Step 4: Commit**

```bash
git add ai-service/requirements-dev.txt ai-service/pytest.ini ai-service/tests/conftest.py
git commit -m "test(ai-service): add pytest scaffolding and history fixture"
```

---

### Task 2: Pin Prophet dependencies (bug 1)

**Files:**
- Create: `ai-service/tests/test_model.py`
- Modify: `ai-service/requirements.txt`

- [ ] **Step 1: Write the failing test**

Create `ai-service/tests/test_model.py`:

```python
import pandas as pd
import pytest

from model import ForecastModel


def test_predict_before_train_raises():
    with pytest.raises(RuntimeError):
        ForecastModel().predict(60)


def test_train_rejects_fewer_than_two_points():
    with pytest.raises(ValueError, match="at least 2"):
        ForecastModel().train([{"ds": "2026-01-01T00:00:00", "y": 1.0}])


def test_train_on_naive_timestamps(history):
    naive = [{"ds": p["ds"].rstrip("Z"), "y": p["y"]} for p in history]
    result = ForecastModel().train(naive)
    assert result["data_points"] == len(naive)
```

- [ ] **Step 2: Run to verify it fails**

Run: `PY_TEST`
Expected: `test_train_on_naive_timestamps` FAILS with `AttributeError: 'Prophet' object has no attribute 'stan_backend'`. The other two pass.

- [ ] **Step 3: Pin the dependencies**

Replace `ai-service/requirements.txt` with:

```text
fastapi==0.115.0
uvicorn==0.30.0
prophet==1.1.6
# Prophet 1.1.6 breaks with cmdstanpy 1.3 and pandas 3.x; keep these pinned together.
cmdstanpy==1.2.5
pandas==2.2.3
numpy==2.2.6
requests==2.32.3
```

- [ ] **Step 4: Run to verify it passes**

Run: `PY_TEST`
Expected: 3 passed.

- [ ] **Step 5: Commit**

```bash
git add ai-service/requirements.txt ai-service/tests/test_model.py
git commit -m "fix(ai-service): pin cmdstanpy, pandas and numpy so Prophet loads"
```

---

### Task 3: Accept UTC timestamps (bug 2)

**Files:**
- Modify: `ai-service/tests/test_model.py` (append)
- Modify: `ai-service/model.py` (`train` and `predict`)

- [ ] **Step 1: Write the failing tests**

Append to `ai-service/tests/test_model.py`:

```python
def test_train_accepts_utc_timestamps_from_go(history):
    result = ForecastModel().train(history)
    assert result["data_points"] == len(history)


def test_predict_returns_only_future_points_in_utc(history):
    model = ForecastModel()
    model.train(history)

    preds = model.predict(60)

    assert len(preds) == 12  # 60 min / 5 min
    last_seen = pd.Timestamp(history[-1]["ds"])
    for p in preds:
        t = pd.Timestamp(p["time"])
        assert t.tzinfo is not None, "times must carry a UTC offset"
        assert t > last_seen
        assert p["lower"] <= p["value"] <= p["upper"]
```

- [ ] **Step 2: Run to verify they fail**

Run: `PY_TEST`
Expected: both new tests FAIL with `ValueError: Column ds has timezone specified, which is not supported. Remove timezone.`

- [ ] **Step 3: Normalise timestamps in `model.py`**

In `train`, replace:

```python
        df["ds"] = pd.to_datetime(df["ds"])
```

with:

```python
        # Prophet rejects timezone-aware timestamps: parse as UTC, then drop the zone.
        df["ds"] = pd.to_datetime(df["ds"], utc=True).dt.tz_localize(None)
```

In `predict`, replace:

```python
                    "time": row["ds"].isoformat(),
```

with:

```python
                    "time": row["ds"].tz_localize("UTC").isoformat(),
```

- [ ] **Step 4: Run to verify they pass**

Run: `PY_TEST`
Expected: 5 passed.

- [ ] **Step 5: Commit**

```bash
git add ai-service/model.py ai-service/tests/test_model.py
git commit -m "fix(ai-service): accept UTC timestamps from the Go API"
```

---

### Task 4: FastAPI endpoint tests

**Files:**
- Create: `ai-service/tests/test_main.py`

- [ ] **Step 1: Write the tests**

These pin down the current `/health` and `/predict` behaviour (characterisation tests). Tasks 2 and 3 already fixed the code, so they should pass straight away.

```python
from fastapi.testclient import TestClient

import main

client = TestClient(main.app)


def test_health_before_any_training(monkeypatch):
    monkeypatch.setattr(main, "model_has_trained", False)

    resp = client.get("/health")

    assert resp.status_code == 200
    assert resp.json() == {"status": "ok", "model_trained": False}


def test_predict_rejects_short_history():
    resp = client.post(
        "/predict",
        json={"metric_name": "cpu", "history": [{"ds": "2026-01-01T00:00:00Z", "y": 1}]},
    )

    assert resp.status_code == 400


def test_predict_returns_forecast_for_utc_history(monkeypatch, history):
    monkeypatch.setattr(main, "model_has_trained", False)

    resp = client.post(
        "/predict",
        json={"metric_name": "memory", "history": history, "horizon_minutes": 30},
    )

    assert resp.status_code == 200
    body = resp.json()
    assert body["metric_name"] == "memory"
    assert body["horizon_minutes"] == 30
    assert len(body["predictions"]) == 6
    assert main.model_has_trained is True
```

- [ ] **Step 2: Run**

Run: `PY_TEST`
Expected: 8 passed.

Sanity check that the tests can fail: temporarily revert the `tz_localize(None)` line in `model.py` and rerun. `test_predict_returns_forecast_for_utc_history` must fail with status 500. Restore the line.

- [ ] **Step 3: Commit**

```bash
git add ai-service/tests/test_main.py
git commit -m "test(ai-service): cover /health and /predict endpoints"
```

---

### Task 5: Go test helpers

**Files:**
- Create: `go-api/testhelpers_test.go`

- [ ] **Step 1: Create the helpers**

```go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

// getForecast calls forecastHandler with the given raw query string.
func getForecast(query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/forecast?"+query, nil)
	rec := httptest.NewRecorder()
	forecastHandler(rec, req)
	return rec
}
```

- [ ] **Step 2: Verify it compiles**

Run: `GO_TEST`
Expected: `ok` with `[no tests to run]`.

- [ ] **Step 3: Commit**

```bash
git add go-api/testhelpers_test.go
git commit -m "test(go-api): add fake Prometheus and AI service helpers"
```

---

### Task 6: Prometheus client tests

**Files:**
- Create: `go-api/prometheus_test.go`

- [ ] **Step 1: Write the tests**

These are characterisation tests for existing behaviour, so they should pass straight away.

```go
package main

import (
	"testing"
	"time"
)

func TestQueryPrometheusRangeParsesValues(t *testing.T) {
	fakePrometheus(t, promRange(1.5, 2.5), "")

	pts, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("got %d points, want 2", len(pts))
	}
	wantDs := time.Unix(1700000000, 0).UTC().Format(time.RFC3339)
	if pts[0].Ds != wantDs || pts[0].Y != 1.5 || pts[1].Y != 2.5 {
		t.Errorf("got %+v, want first point {%s 1.5} and second y=2.5", pts, wantDs)
	}
}

func TestQueryPrometheusRangeSkipsMalformedRows(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` +
		`[1700000000,"1"],[1700000300,"not-a-number"],["bad-ts","2"],[1700000600,"3"]]}]}}`
	fakePrometheus(t, body, "")

	pts, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pts) != 2 || pts[0].Y != 1 || pts[1].Y != 3 {
		t.Errorf("got %+v, want only the two well-formed rows", pts)
	}
}

func TestQueryPrometheusRangeEmptyResultIsNotAnError(t *testing.T) {
	fakePrometheus(t, promRange(), "")

	pts, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil || len(pts) != 0 {
		t.Errorf("got (%v, %v), want (empty, nil)", pts, err)
	}
}

func TestQueryPrometheusRangeErrorStatus(t *testing.T) {
	fakePrometheus(t, `{"status":"error","data":{"resultType":"","result":[]}}`, "")

	if _, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m"); err == nil {
		t.Error("want error for status=error, got nil")
	}
}

func TestQueryPrometheusRangeHTTPError(t *testing.T) {
	fakePrometheus(t, "", "")

	if _, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m"); err == nil {
		t.Error("want error for HTTP 500, got nil")
	}
}

func TestQueryPrometheusReturnsInstantValue(t *testing.T) {
	fakePrometheus(t, "", promInstant(0.75))

	v, err := queryPrometheus("q")

	if err != nil || v != 0.75 {
		t.Errorf("got (%v, %v), want (0.75, nil)", v, err)
	}
}

func TestQueryPrometheusEmptyResultIsError(t *testing.T) {
	fakePrometheus(t, "", promEmptyVector)

	if _, err := queryPrometheus("q"); err == nil {
		t.Error("want error for empty instant result, got nil")
	}
}
```

- [ ] **Step 2: Run**

Run: `GO_TEST`
Expected: `ok`. Add `-v` to see the 7 tests pass.

- [ ] **Step 3: Commit**

```bash
git add go-api/prometheus_test.go
git commit -m "test(go-api): cover Prometheus range and instant queries"
```

---

### Task 7: Forecast handler tests

**Files:**
- Create: `go-api/handlers_test.go`

- [ ] **Step 1: Write the tests**

```go
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
```

- [ ] **Step 2: Run**

Run: `GO_TEST`
Expected: `ok` (15 tests in total with Task 6).

Sanity check that the tests can fail: temporarily change `if err != nil || up != 1` in `handlers.go` to `if err != nil`, rerun, and confirm `TestForecastColdStartWithExporterDown` fails. Restore the line.

- [ ] **Step 3: Commit**

```bash
git add go-api/handlers_test.go
git commit -m "test(go-api): cover forecast handler paths"
```

---

### Task 8: CI unit-test jobs

**Files:**
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Add the two jobs and make the integration job wait for them**

Add these jobs above `test-mvp-stack` under `jobs:`:

```yaml
  go-unit:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: go-api
    steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with:
        go-version: '1.22'
    - name: Vet
      run: go vet ./...
    - name: Check formatting
      run: test -z "$(gofmt -l .)"
    - name: Test
      run: go test -race ./...

  py-unit:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: ai-service
    steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-python@v5
      with:
        python-version: '3.11'
        cache: pip
        cache-dependency-path: |
          ai-service/requirements.txt
          ai-service/requirements-dev.txt
    - name: Install
      run: pip install -r requirements.txt -r requirements-dev.txt
    - name: Test
      run: pytest -q
```

Then add `needs` to the existing integration job:

```yaml
  test-mvp-stack:
    needs: [go-unit, py-unit]
    runs-on: ubuntu-latest
```

- [ ] **Step 2: Validate the YAML**

Run: `python -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml')); print('YAML_OK')"`
Expected: `YAML_OK`. If PyYAML is missing, run `pip install pyyaml` first.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: run Go and Python unit tests before integration"
```

---

### Task 9: Integration check for a real Prophet forecast

**Files:**
- Modify: `clean_setup.sh` (insert before the final `echo "=========================================="` block)

- [ ] **Step 1: Add the AI service check**

Insert after the existing integration test's closing `fi`:

```bash
echo "Waiting for AI Service..."
for attempt in $(seq 1 24); do
    if curl -sf http://localhost:5000/health > /dev/null; then
        break
    fi
    sleep 5
done

echo "Running AI Service Test (real Prophet forecast on UTC history)..."
python3 -c '
import json, math
from datetime import datetime, timedelta, timezone
start = datetime(2026, 1, 1, tzinfo=timezone.utc)
history = [
    {"ds": (start + timedelta(minutes=5 * i)).strftime("%Y-%m-%dT%H:%M:%SZ"),
     "y": 30 + 10 * math.sin(2 * math.pi * i / 288)}
    for i in range(3 * 288)
]
print(json.dumps({"metric_name": "cpu", "history": history, "horizon_minutes": 30}))
' > /tmp/predict_payload.json

AI_RESPONSE=$(curl -s -X POST -H "Content-Type: application/json" \
    --data-binary @/tmp/predict_payload.json http://localhost:5000/predict)

if echo "$AI_RESPONSE" | grep -q '"predictions":\[{'; then
    echo "AI Service Test Passed!"
else
    echo "AI Service Test Failed! Got:"
    echo "$AI_RESPONSE"
    exit 1
fi
```

- [ ] **Step 2: Run the full stack locally**

Run: `./clean_setup.sh` (the first build takes several minutes).
Expected: `Integration Test Passed!`, then `AI Service Test Passed!`, then `Setup complete!`.

Then run `docker compose down -v`.

- [ ] **Step 3: Commit**

```bash
git add clean_setup.sh
git commit -m "test: verify a real Prophet forecast in the integration run"
```

---

### Task 10: Docs, push and PR

- [ ] **Step 1: Update `README.md`**

Add a `## Running Tests` section before `## Continuous Integration`:

````markdown
## Running Tests

```bash
# Go unit tests
cd go-api && go test ./...

# Python unit tests (Python 3.11)
cd ai-service && pip install -r requirements.txt -r requirements-dev.txt && pytest
```
````

Replace the `## Continuous Integration` paragraph with:

```markdown
GitHub Actions runs Go and Python unit tests on every push and pull request to `main`, then brings up the full Docker stack and runs integration checks (API cold start and a real Prophet forecast). Image builds are cached in the GitHub Actions cache.
```

- [ ] **Step 2: Commit, push, open the PR**

```bash
git add README.md
git commit -m "docs: document how to run tests"
git push -u origin test/foundation
gh pr create --title "Test foundation and fix the real forecast path" --body "<summary of both bugs and the new test jobs>"
```

- [ ] **Step 3: Confirm CI is green**

Run: `gh pr checks --watch`
Expected: `go-unit`, `py-unit` and `test-mvp-stack` all pass.
