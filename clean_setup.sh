#!/bin/bash
set -e

echo "=========================================="
echo "Starting AI Forecasting Infrastructure"
echo "=========================================="

echo "Cleaning up old containers and volumes..."
docker-compose down -v --remove-orphans || true

echo "Building and starting Docker Compose stack..."
docker-compose up -d --build

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
    docker-compose logs go-api
    echo "--- AI Service Logs ---"
    docker-compose logs ai-service
    exit 1
fi

echo "Running Integration Test (Cold Start scenario)..."
RESPONSE=$(curl -s "http://localhost:8080/api/v1/forecast?metric=cpu&horizon_minutes=60")

if echo "$RESPONSE" | grep -q '"metric":'; then
    echo "Integration Test Passed! Full response:"
    echo "$RESPONSE" | jq . || echo "$RESPONSE"
else
    echo "Integration Test Failed! Expected JSON response. Got:"
    echo "$RESPONSE"
    exit 1
fi

echo "=========================================="
echo "Setup complete! The system is running."
echo "Use 'docker-compose down -v' to clean up when you're done."
echo "=========================================="
