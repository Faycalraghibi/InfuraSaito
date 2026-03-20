# InfuraSaito

InfuraSaito is a proof-of-concept AI-driven monitoring and forecasting system for Kubernetes clusters. It combines Prometheus for metrics collection, a Go API for orchestration, and a Python service using Facebook Prophet for time-series forecasting.

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                                         │
│                                                                             │
│  ┌─────────────────────┐   ┌─────────────────────┐   ┌─────────────────────┐ │
│  │  Prometheus         │   │  Grafana            │   │  Alertmanager       │ │
│  │  (Metrics + Alerting) │   │  (Dashboards)       │   │  (Alert Routing)    │ │
│  └─────────────────────┘   └─────────────────────┘   └─────────────────────┘ │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │  InfuraSaito Backend (Go)                                             │ │
│  │                                                                       │ │
│  │  ┌─────────────────────┐   ┌─────────────────────┐   ┌─────────────────┐ │ │
│  │  │  Current Metrics    │   │  Forecast Handler   │   │  AI Service     │ │ │
│  │  │  (Prometheus)       │   │  (Orchestration)    │   │  (Prophet)      │ │ │
│  │  └─────────────────────┘   └─────────────────────┘   └─────────────────┘ │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Components

### 1. Prometheus
- **Role**: Collects metrics from Kubernetes nodes and pods
- **Configuration**: `prometheus.yml`
- **Key Metrics**:
  - `node_cpu_seconds_total`: CPU usage across all nodes
  - `container_cpu_usage_seconds_total`: CPU usage per container
  - `container_memory_working_set_bytes`: Memory usage per container

### 2. Go API
- **Role**: Backend orchestration service
- **Endpoints**:
  - `GET /healthz`: Health check
  - `GET /api/v1/metrics/current`: Get current metrics from Prometheus
  - `GET /api/v1/forecast`: Get AI-generated forecast
- **Dependencies**:
  - `github.com/prometheus/client_golang/api`: Prometheus client
  - `github.com/joho/godotenv`: Environment variable management

### 3. AI Service
- **Role**: Time-series forecasting using Facebook Prophet
- **Endpoints**:
  - `GET /health`: Health check
  - `POST /predict`: Train model on historical data and return forecast
- **Dependencies**:
  - `fastapi`: Web framework
  - `prophet`: Forecasting library
  - `pandas`, `numpy`: Data manipulation

## Deployment

### Prerequisites
- Docker and Docker Compose
- Kubernetes cluster (Minikube, Kind, or cloud-based)
- Helm (optional, for easier management)

### Quick Start (Docker Compose)

1. **Clone the repository**
   ```bash
   git clone <repository-url>
   cd InfuraSaito
   ```

2. **Start the stack**
   ```bash
   docker compose up -d
   ```

3. **Verify deployment**
   ```bash
   # Check services are running
   docker compose ps
   
   # Check logs
   docker compose logs go-api
   docker compose logs ai-service
   docker compose logs prometheus
   
   # Test health endpoints
   curl http://localhost:8080/healthz
   curl http://localhost:5000/health
   ```

### Kubernetes Deployment

1. **Install Prometheus Operator**
   ```bash
   helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
   helm repo update
   helm install prometheus prometheus-community/kube-prometheus-stack
   ```

2. **Deploy Go API**
   ```bash
   kubectl apply -f k8s/go-api-deployment.yaml
   kubectl apply -f k8s/go-api-service.yaml
   ```

3. **Deploy AI Service**
   ```bash
   kubectl apply -f k8s/ai-service-deployment.yaml
   kubectl apply -f k8s/ai-service-service.yaml
   ```

4. **Configure Prometheus**
   - Create PrometheusServiceMonitor CRD
   - Add scrape config for Go API

## Usage

### Get Current Metrics
```bash
curl http://localhost:8080/api/v1/metrics/current
```

### Get AI Forecast
```bash
curl http://localhost:8080/api/v1/forecast?horizon_minutes=60
```

### Test AI Service Directly
```bash
# Generate synthetic data
python ai-service/test_predict.py
```

## Configuration

### Environment Variables

**Go API**:
- `PROMETHEUS_URL`: Prometheus endpoint (default: `http://localhost:9090`)
- `PORT`: API port (default: `8080`)

**AI Service**:
- `PORT`: Service port (default: `5000`)

### Prometheus Configuration

Edit `prometheus.yml` to:
- Add scrape targets for Go API
- Configure alerting rules
- Adjust scrape intervals

## Development

### Adding New Metrics

1. **Update Prometheus**
   - Add metric to `prometheus.yml`
   - Create ServiceMonitor CRD for Kubernetes

2. **Update Go API**
   - Add query to `handlers.go`
   - Update `currentMetricsHandler` if needed

3. **Update AI Service**
   - Add new Prophet model if needed
   - Update `model.py` with Prophet configuration

### Testing

```bash
# Run AI service tests
python -m pytest ai-service/tests/

# Run integration tests
./clean_setup.sh
```

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.