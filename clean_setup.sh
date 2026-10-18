#!/bin/bash
set -e

echo "=========================================="
echo "Starting AI Forecasting Infrastructure"
echo "=========================================="

echo "Cleaning up old containers and volumes..."
docker compose down -v --remove-orphans || true

echo "Building and starting Docker Compose stack..."
docker compose up -d --build

echo "Waiting for Go API and AI Service to be ready..."
MAX_RETRIES=30
RETRY_COUNT=0
API_READY=false

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if curl -s http://localhost:8080/healthz > /dev/null; then
        API_READY=true
        echo "Services are up and running!"
        break
    fi
    echo "   ...still waiting (attempt $((RETRY_COUNT+1))/$MAX_RETRIES)"
    sleep 5
    RETRY_COUNT=$((RETRY_COUNT+1))
done

if [ "$API_READY" = false ]; then
    echo "ERROR: Services failed to start within the timeout period."
    echo "--- Go API Logs ---"
    docker compose logs go-api
    echo "--- AI Service Logs ---"
    docker compose logs ai-service
    exit 1
fi

echo "Running Integration Test..."
# Retry while Prometheus completes its first scrape (the API returns 503 until then).
RESPONSE=""
for attempt in $(seq 1 12); do
    if RESPONSE=$(curl -sf "http://localhost:8080/api/v1/forecast?metric=cpu&horizon_minutes=60"); then
        break
    fi
    echo "   ...forecast not ready yet (attempt $attempt/12)"
    RESPONSE=""
    sleep 5
done

if echo "$RESPONSE" | grep -q '"metric_name":'; then
    echo "Integration Test Passed! Full response:"
    echo "$RESPONSE" | jq . || echo "$RESPONSE"
else
    echo "Integration Test Failed! Expected JSON response. Got:"
    echo "$RESPONSE"
    exit 1
fi

echo "Checking every metric against real Prometheus data..."
for metric in cpu memory disk network_rx network_tx load1; do
    for endpoint in "metrics/current?metric=$metric" "forecast?metric=$metric&horizon_minutes=30"; do
        OK=false
        for attempt in $(seq 1 12); do
            if curl -sf --max-time 15 "http://localhost:8080/api/v1/$endpoint" > /dev/null; then
                OK=true
                break
            fi
            sleep 5
        done
        if [ "$OK" = false ]; then
            echo "Metric check failed: /api/v1/$endpoint"
            curl -s --max-time 15 "http://localhost:8080/api/v1/$endpoint"
            exit 1
        fi
        echo "   ok  /api/v1/$endpoint"
    done
done

echo "Checking instance listing..."
INSTANCES=$(curl -s --max-time 15 http://localhost:8080/api/v1/instances) || true
if echo "$INSTANCES" | grep -q '"instance":"node-exporter:9100","up":true'; then
    echo "   ok  /api/v1/instances"
else
    echo "Instance listing failed. Got:"
    echo "$INSTANCES"
    exit 1
fi

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

AI_RESPONSE=$(curl -s --max-time 120 -X POST -H "Content-Type: application/json" \
    --data-binary @/tmp/predict_payload.json http://localhost:5000/predict) || true

if echo "$AI_RESPONSE" | grep -q '"predictions":\[{'; then
    echo "AI Service Test Passed!"
else
    echo "AI Service Test Failed! Got:"
    echo "$AI_RESPONSE"
    exit 1
fi

echo "=========================================="
echo "Setup complete! The system is running."
echo "Use 'docker compose down -v' to clean up when you're done."
echo "=========================================="
