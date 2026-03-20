<div align="center">

# InfuraSaito

[![Python](https://img.shields.io/badge/Python-3.11+-blue.svg)](https://www.python.org/downloads/)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8.svg?logo=go&logoColor=white)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-24.x-2496ED.svg?logo=docker&logoColor=white)](https://www.docker.com/)
[![FastAPI](https://img.shields.io/badge/FastAPI-0.115+-009688.svg?logo=fastapi&logoColor=white)](https://fastapi.tiangolo.com/)
[![Prometheus](https://img.shields.io/badge/Prometheus-2.51+-E6522C.svg?logo=prometheus&logoColor=white)](https://prometheus.io/)
[![CI Status](https://github.com/Faycalraghibi/InfuraSaito/actions/workflows/ci.yml/badge.svg)](https://github.com/Faycalraghibi/InfuraSaito/actions)
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
docker compose up -d --build
```

*Note: Initial build times may take a few minutes as the Python container compiles necessary C++ dependencies for the Prophet modeling backend.*

## Core Endpoints

Once the stack is running, traffic is routed through the Go API on port `8080`.

### `GET /api/v1/forecast`
Fetches historical metric data from Prometheus, passes it to the AI service, and returns a time-series forecast.
* **Parameters**: 
  * `metric` (default: `cpu`) 
  * `horizon_minutes` (default: `60`)
* **Example**: `curl "http://localhost:8080/api/v1/forecast?metric=cpu&horizon_minutes=60"`

### `GET /api/v1/metrics/current`
Returns the instant rolling average of the specified infrastructure metric.
* **Example**: `curl http://localhost:8080/api/v1/metrics/current`

### `GET /healthz`
Liveness probe to verify the Go orchestration API is successfully running.
* **Example**: `curl http://localhost:8080/healthz`

## Continuous Integration

A GitHub Actions pipeline is configured to validate the integration flow on every push to the `main` branch. The pipeline automatically uses Docker layer caching to accelerate subsequent testing builds.