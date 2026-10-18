#!/bin/bash
set -e

echo "=========================================="
echo "Starting AI Forecasting Infrastructure"
echo "=========================================="

echo "Cleaning up old containers and volumes..."
docker compose down -v --remove-orphans || true

echo "Building and starting Docker Compose stack (waiting for healthchecks)..."
if ! docker compose up -d --build --wait --wait-timeout 300; then
    echo "ERROR: Services did not become healthy within the timeout period."
    docker compose ps
    echo "--- Go API Logs ---"
    docker compose logs go-api
    echo "--- AI Service Logs ---"
    docker compose logs ai-service
    echo "--- Prometheus Logs ---"
    docker compose logs prometheus
    exit 1
fi
echo "Services are up and healthy!"

echo "Running Integration Test..."
# Retry while Prometheus completes its first scrape (the API returns 503 until then).
RESPONSE=""
for attempt in $(seq 1 12); do
    if RESPONSE=$(curl -sf -H "X-API-Key: ${API_KEY:-}" "http://localhost:8080/api/v1/forecast?metric=cpu&horizon_minutes=60"); then
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
            if curl -sf --max-time 15 -H "X-API-Key: ${API_KEY:-}" "http://localhost:8080/api/v1/$endpoint" > /dev/null; then
                OK=true
                break
            fi
            sleep 5
        done
        if [ "$OK" = false ]; then
            echo "Metric check failed: /api/v1/$endpoint"
            curl -s --max-time 15 -H "X-API-Key: ${API_KEY:-}" "http://localhost:8080/api/v1/$endpoint"
            exit 1
        fi
        echo "   ok  /api/v1/$endpoint"
    done
done

echo "Checking instance listing..."
INSTANCES=$(curl -s --max-time 15 -H "X-API-Key: ${API_KEY:-}" http://localhost:8080/api/v1/instances) || true
if echo "$INSTANCES" | grep -q '"instance":"node-exporter:9100","up":true'; then
    echo "   ok  /api/v1/instances"
else
    echo "Instance listing failed. Got:"
    echo "$INSTANCES"
    exit 1
fi

echo "Checking that Prometheus scrapes every target (node, go-api, ai-service)..."
TARGETS_UP=0
for attempt in $(seq 1 12); do
    TARGETS_UP=$(curl -s --max-time 10 "http://localhost:9090/api/v1/query?query=up" \
        | grep -o '"value":\[[^]]*,"1"\]' | wc -l) || true
    if [ "$TARGETS_UP" -ge 3 ]; then
        break
    fi
    sleep 5
done
if [ "$TARGETS_UP" -ge 3 ]; then
    echo "   ok  $TARGETS_UP Prometheus targets up"
else
    echo "Expected 3 Prometheus targets up, got $TARGETS_UP:"
    curl -s --max-time 10 "http://localhost:9090/api/v1/query?query=up"
    exit 1
fi
if curl -s --max-time 10 http://localhost:8080/metrics | grep -q '^infurasaito_http_requests_total'; then
    echo "   ok  /metrics"
else
    echo "Go API /metrics is missing infurasaito_http_requests_total"
    exit 1
fi

echo "Checking the forecast accuracy endpoint (Postgres-backed)..."
ACCURACY=$(curl -s --max-time 15 -H "X-API-Key: ${API_KEY:-}" \
    "http://localhost:8080/api/v1/forecast/accuracy?metric=cpu&days=1") || true
if echo "$ACCURACY" | grep -q '"compared":'; then
    echo "   ok  /api/v1/forecast/accuracy"
else
    echo "Accuracy check failed. Got:"
    echo "$ACCURACY"
    exit 1
fi

echo "Checking the threshold endpoint..."
THRESHOLD=$(curl -s --max-time 15 -H "X-API-Key: ${API_KEY:-}" \
    "http://localhost:8080/api/v1/forecast/threshold?metric=cpu&threshold=1000") || true
if echo "$THRESHOLD" | grep -q '"will_cross":false'; then
    echo "   ok  /api/v1/forecast/threshold"
else
    echo "Threshold check failed. Got:"
    echo "$THRESHOLD"
    exit 1
fi

if [ -n "${API_KEY:-}" ]; then
    echo "Checking that /api/ rejects requests without the API key..."
    STATUS=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 http://localhost:8080/api/v1/instances) || true
    if [ "$STATUS" = "401" ]; then
        echo "   ok  401 without X-API-Key"
    else
        echo "Expected 401 without X-API-Key, got $STATUS"
        exit 1
    fi
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

echo "Running AI cache test (train, then forecast from the cached model)..."
python3 -c '
import json, sys
payload = json.load(sys.stdin)
payload.pop("horizon_minutes")
payload["instance"] = "integration:9100"
print(json.dumps(payload))
' < /tmp/predict_payload.json > /tmp/train_payload.json

TRAIN_RESPONSE=$(curl -s --max-time 120 -X POST -H "Content-Type: application/json" \
    --data-binary @/tmp/train_payload.json http://localhost:5000/train) || true
CACHED_RESPONSE=$(curl -s --max-time 30 -w '\n%{time_total}' -X POST -H "Content-Type: application/json" \
    -d '{"metric_name":"cpu","instance":"integration:9100","horizon_minutes":30}' \
    http://localhost:5000/forecast) || true
CACHED_SECONDS=$(echo "$CACHED_RESPONSE" | tail -n 1)

if echo "$CACHED_RESPONSE" | grep -q '"trained_at"' && echo "$CACHED_RESPONSE" | grep -q '"predictions":\[{'; then
    echo "AI Cache Test Passed! (cached forecast took ${CACHED_SECONDS}s)"
else
    echo "AI Cache Test Failed! /train returned:"
    echo "$TRAIN_RESPONSE"
    echo "/forecast returned:"
    echo "$CACHED_RESPONSE"
    exit 1
fi

echo "Running AI anomaly test (check history against the cached model)..."
ANOMALY_RESPONSE=$(curl -s --max-time 60 -X POST -H "Content-Type: application/json" \
    --data-binary @/tmp/train_payload.json http://localhost:5000/anomalies) || true
if echo "$ANOMALY_RESPONSE" | grep -q '"checked":' && echo "$ANOMALY_RESPONSE" | grep -q '"anomalies":'; then
    echo "AI Anomaly Test Passed!"
else
    echo "AI Anomaly Test Failed! Got:"
    echo "$ANOMALY_RESPONSE"
    exit 1
fi

echo "=========================================="
echo "Setup complete! The system is running."
echo "Use 'docker compose down -v' to clean up when you're done."
echo "=========================================="
