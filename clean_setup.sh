#!/bin/bash
# clean_setup.sh - Script to cleanly start, test, and tear down the forecasting MVP infrastructure.
# Can be used locally or in CI pipelines.

set -e

echo "=========================================="
echo "🚀 Starting AI Forecasting Infrastructure"
echo "=========================================="

# 1. Clean up any existing state
echo "🧹 Cleaning up old containers and volumes..."
docker-compose down -v --remove-orphans || true

# 2. Build and start the stack
echo "🏗️  Building and starting Docker Compose stack..."
# Prophet takes a few minutes to build the first time because of CmdStan
docker-compose up -d --build

# 3. Wait for services to be healthy
echo "⏳ Waiting for Go API and AI Service to be ready..."
MAX_RETRIES=30
RETRY_COUNT=0
API_READY=false

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if curl -s http://localhost:8080/healthz > /dev/null; then
        API_READY=true
        echo "✅ Services are up and running!"
        break
    fi
    echo "   ...still waiting (attempt $((RETRY_COUNT+1))/$MAX_RETRIES)"
    sleep 5
    RETRY_COUNT=$((RETRY_COUNT+1))
done

if [ "$API_READY" = false ]; then
    echo "❌ ERROR: Services failed to start within the timeout period."
    echo "--- Go API Logs ---"
    docker-compose logs go-api
    echo "--- AI Service Logs ---"
    docker-compose logs ai-service
    exit 1
fi

# 4. Run MVP Integration Test
echo "🧪 Running Integration Test (Cold Start scenario)..."
# We expect this to return the fallback response since Prometheus is fresh and has no data
RESPONSE=$(curl -s "http://localhost:8080/api/v1/forecast?metric=cpu&horizon_minutes=60")

if echo "$RESPONSE" | grep -q '"metric":'; then
    echo "✅ Integration Test Passed! Full response:"
    echo "$RESPONSE" | jq . || echo "$RESPONSE"
else
    echo "❌ Integration Test Failed! Expected JSON response. Got:"
    echo "$RESPONSE"
    exit 1
fi

echo "=========================================="
echo "🎉 Setup complete! The system is running."
echo "Use 'docker-compose down -v' to clean up when you're done."
echo "=========================================="
