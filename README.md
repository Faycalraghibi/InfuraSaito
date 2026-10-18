<div align="center">

# InfuraSaito

[![Python](https://img.shields.io/badge/Python-3.11-blue.svg)](https://www.python.org/downloads/)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8.svg?logo=go&logoColor=white)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker%20Compose-v2-2496ED.svg?logo=docker&logoColor=white)](https://www.docker.com/)
[![FastAPI](https://img.shields.io/badge/FastAPI-0.115+-009688.svg?logo=fastapi&logoColor=white)](https://fastapi.tiangolo.com/)
[![Prometheus](https://img.shields.io/badge/Prometheus-v2.51.0-E6522C.svg?logo=prometheus&logoColor=white)](https://prometheus.io/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

</div>

InfuraSaito is a cloud-agnostic, AI-powered infrastructure forecasting system. It collects real-time metrics, analyzes historical trends, and predicts future resource usage using Facebook Prophet.

## Architecture

The system is built with a microservices architecture orchestrated via Docker Compose:

* **Prometheus & Node Exporter**: Scrapes and stores hardware metrics.
* **Go API (Orchestrator)**: Handles routing, data aggregation, and cold-start logic.
* **Python AI Service**: Uses Prophet to train on historical data and generate forecasts.

## Quick Start

The easiest way to run the entire stack locally is using the provided setup script, which builds the containers and runs an integration test:

```bash
chmod +x clean_setup.sh
./clean_setup.sh
```

Alternatively, you can manually start the cluster using Docker Compose v2:

```bash
docker compose up -d --build --wait
```

Every service has a healthcheck, and the Go API starts only once Prometheus and the AI service are healthy. `--wait` returns when the whole stack is healthy.

*Note: Initial build times may take a few minutes as the Python container compiles necessary C++ dependencies for the Prophet modeling backend.*

## Core Endpoints

Once the stack is running, traffic is routed through the Go API on port `8080`.

**Authentication and rate limiting.** When `API_KEY` is set, every `/api/` route requires the header `X-API-Key: <key>` and returns `401` without it; `/healthz` stays open. All `/api/` routes share one rate limit (default 10 requests/s, burst 20); over the limit they return `429` with `Retry-After`. The limit is global and also counts requests without a valid key, so it slows key guessing but lets any client use up the shared budget; put a per-client limiter or proxy in front for multi-tenant use. When `API_KEY` is set, add `-H "X-API-Key: <key>"` to the examples below.

```bash
API_KEY=change-me docker compose up -d --build --wait
curl -H "X-API-Key: change-me" "http://localhost:8080/api/v1/forecast?metric=cpu"
```

### `GET /api/v1/forecast`
Fetches historical metric data from Prometheus, passes it to the AI service, and returns a time-series forecast.
* **Parameters**: 
  * `metric` (default: `cpu`): one of the [supported metrics](#supported-metrics). Other values return `400`. Returns `503` if the (chosen) node-exporter target is not being scraped or has no data yet.
  * `horizon_minutes` (default: `60`, max: `10080` = 7 days). Larger values return `400`; non-numeric or non-positive values fall back to `60`.
  * `instance` (optional): the node-exporter instance, e.g. `node-exporter:9100`. Required when Prometheus scrapes more than one instance (`400` lists them); an unknown instance returns `404`.
* **Example**: `curl "http://localhost:8080/api/v1/forecast?metric=cpu&horizon_minutes=60"`

Forecasts come from a model the Go API trains in the background for each metric and instance (every `TRAIN_INTERVAL`), so they start after the model's last training point and include `trained_at`. Until the first training run finishes, the API trains on request instead.

If the AI service fails repeatedly (connection errors or `5xx`), a circuit breaker opens: `/forecast` then returns `503` with `Retry-After` without calling the AI service, and after the cooldown a single request is let through to test whether it has recovered. Only connection errors and `5xx` replies count as failures (one forecast request can make up to two AI calls: the cached forecast, then training on request); `4xx` replies and background-training errors on bad data do not.

### `GET /api/v1/metrics/current`
Returns the current value of a metric.
Returns `503` when there is no data yet.
* **Parameters**:
  * `metric` (default: `cpu`): one of the [supported metrics](#supported-metrics). Other values return `400`.
  * `instance` (optional): the node-exporter instance, e.g. `node-exporter:9100`. Required when Prometheus scrapes more than one instance (`400` lists them); an unknown instance returns `404`.
* **Example**: `curl "http://localhost:8080/api/v1/metrics/current?metric=memory"`

### `GET /api/v1/instances`
Lists the node-exporter targets Prometheus scrapes and whether each is up.
* **Example**: `curl http://localhost:8080/api/v1/instances` → `{"instances":[{"instance":"node-exporter:9100","up":true}]}`

### `GET /healthz`
Liveness probe to verify the Go orchestration API is successfully running.
* **Example**: `curl http://localhost:8080/healthz`

## Supported Metrics

| `metric` | Unit | Source |
|---|---|---|
| `cpu` | % busy | `node_cpu_seconds_total` |
| `memory` | % used | `node_memory_MemAvailable_bytes` / `MemTotal_bytes` |
| `disk` | % used of `/` (of Docker's data disk when node-exporter runs in a container) | `node_filesystem_avail_bytes` / `size_bytes` |
| `network_rx` | bytes/s received on the interfaces node-exporter sees, excluding loopback and container virtual interfaces | `node_network_receive_bytes_total` |
| `network_tx` | bytes/s sent, same interfaces as `network_rx` | `node_network_transmit_bytes_total` |
| `load1` | 1-minute load average | `node_load1` |

Under the bundled docker compose setup, node-exporter runs in its own container, so `network_rx`/`network_tx` reflect that container's interface rather than the host's. Run node-exporter on the host (or with `network_mode: host`) for host-wide network figures.

On a cold start (less than about 10 minutes of history), `/forecast` returns the metric's current value with `"confidence": "none - insufficient history, showing current value"`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `PROMETHEUS_URL` | `http://localhost:9090` | Prometheus base URL |
| `AI_SERVICE_URL` | `http://localhost:5000` | AI service base URL |
| `PROM_TIMEOUT` | `10s` | Timeout for each Prometheus request (Go duration) |
| `AI_TIMEOUT` | `60s` | Timeout for each AI service request (Go duration) |
| `PORT` | `8080` | Go API listen port |
| `API_KEY` | unset | When set, `/api/` routes require `X-API-Key` with this value |
| `RATE_LIMIT_RPS` | `10` | Requests per second allowed across all `/api/` routes; `0` disables limiting |
| `RATE_LIMIT_BURST` | `20` | Requests allowed in a burst above the steady rate |
| `TRAIN_INTERVAL` | `15m` | How often the Go API retrains cached models for every metric and instance (Go duration); `0` disables background training |
| `AI_BREAKER_THRESHOLD` | `5` | Consecutive AI service failures that open the circuit breaker; `0` disables it |
| `AI_BREAKER_COOLDOWN` | `30s` | How long the breaker stays open before letting a probe request through (Go duration) |

## Running Tests

```bash
# Go unit tests
cd go-api && go test ./...

# Python unit tests (Python 3.11)
cd ai-service && pip install -r requirements.txt -r requirements-dev.txt && pytest
```

## Continuous Integration

GitHub Actions runs Go and Python unit tests on every push and pull request to `main`, then brings up the full Docker stack and runs integration checks (API cold start and a real Prophet forecast). Image builds are cached in the GitHub Actions cache.
