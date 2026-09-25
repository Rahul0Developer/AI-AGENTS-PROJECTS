#!/usr/bin/env bash
# GraphQL smoke tests against a running agent (local or Cloud Run).
# Usage: BASE_URL=http://localhost:8080 ./scripts/smoke-test.sh
set -euo pipefail
BASE_URL="${BASE_URL:-http://localhost:8080}"
# Build the JSON envelope safely with python3 (handles nested double quotes in queries).
gql() {
  local payload
  payload=$(python3 -c 'import json,sys; print(json.dumps({"query": sys.argv[1]}))' "$1")
  curl -sS -X POST "$BASE_URL/graphql" -H 'content-type: application/json' -d "$payload"
  echo
}

echo "-- health"; curl -sS "$BASE_URL/healthz"; echo
echo "-- createOrder"
gql 'mutation { createOrder(customerId:"cust-003", items:[{sku:"STK-KISS-100", quantity:5}]) { id status } }'
echo "-- processPendingOrders (autonomous cycle)"
gql 'mutation { processPendingOrders }'
echo "-- shipped orders"
gql 'query { orders(status:"shipped", limit:5) { id status trackingNumber fulfillmentNotes } }'
echo "-- audit trace of one order"
gql 'query { orderEvents(orderId:"ord-0001") { step success detail } }'
echo "-- KPI metrics"
gql 'query { metrics { totalOrders autoProcessed automationRatePct avgProcessingSeconds errorRatePct hoursSaved estimatedCostSavedUsd } }'
