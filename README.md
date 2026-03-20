# InfuraSaito AI Infrastructure Forecaster

This project is a scalable, cloud-agnostic Minimum Viable Product (MVP) for an AI-powered infrastructure forecasting system. It collects real-time infrastructure metrics, stores them locally, and uses an AI model to predict future resource usage.

## Architecture

The system is containerized and composed of four primary services orchestrated via Docker Compose:

1. **Node Exporter (Port 9100):** Exposes raw CPU and hardware metrics from the host machine.
2. **Prometheus (Port 9090):** Time-series database that scrapes and stores metrics from the Node Exporter.
3. **Python AI Service (Port 5000):** A FastAPI microservice that wraps Facebook Prophet. It receives historical time-series data and returns predicted future usage bands (with confidence intervals).
4. **Go API Orchestrator (Port 8080):** The main entry point. It handles incoming forecast requests, queries historical data from Prometheus, proxies the data to the Python AI service, and returns the final JSON response to the user. Includes fallback logic for cold starts (when insufficient historical data exists).

## Prerequisites

- Docker
- Docker Compose
- Git
- Open ports: `8080`, `5000`, `9090`, `9100`

## Installation and Execution

The repository includes an automated setup script that handles building the images, waiting for health checks, and running an integration test.

```bash
chmod +x clean_setup.sh
./clean_setup.sh
```

Alternatively, you can manually start the services using Docker Compose:

```bash
docker-compose up -d --build
```

*Note: The first build of the Python AI Service container may take several minutes due to the compilation of the Prophet C++ CmdStan backend.*

## API Endpoints

### Go API (Port 8080)

- **GET `/healthz`**: Liveness check.
- **GET `/api/v1/forecast?metric=cpu&horizon_minutes=60`**: Fetches the last 14 days of CPU metrics from Prometheus, runs it through the Prophet model, and returns a 60-minute prediction.

### Python AI Service (Port 5000)

- **GET `/health`**: Internal liveness check.
- **POST `/predict`**: Internal endpoint used by the Go API to pass raw historical JSON and receive JSON predictions.

## Continuous Integration

A GitHub Actions pipeline (`.github/workflows/ci.yml`) is configured to run on every push and pull request to the `main` branch. It utilizes Docker layer caching to optimize build times and automatically executes the `clean_setup.sh` integration testing flow.