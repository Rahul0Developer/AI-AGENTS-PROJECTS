#!/usr/bin/env bash
# =============================================================================
# One-shot GCP deployment for the Intelligent Order Fulfillment Coordinator.
# Uses only free-tier-friendly services: Cloud Run + Cloud SQL (shared core,
# covered by $300 trial credits) + Cloud Scheduler + Artifact Registry.
#
# Usage:   PROJECT_ID=my-gcp-project ./deploy/deploy.sh
# Requires: gcloud CLI authenticated (gcloud auth login) with billing enabled.
# =============================================================================
set -euo pipefail

PROJECT_ID="${PROJECT_ID:?set PROJECT_ID to your GCP project}"
REGION="${REGION:-us-central1}"
SERVICE="order-fulfillment-agent"
REPO="agents"
IMAGE="us-docker.pkg.dev/${PROJECT_ID}/${REPO}/${SERVICE}:latest"
DB_INSTANCE="agent-pg"      # db-custom-1-3840 = shared core, cheapest tier
TZ=America/New_York

echo "==> 1/7 Project & APIs"
gcloud config set project "$PROJECT_ID"
gcloud services enable run.googleapis.com sqladmin.googleapis.com \
  scheduler.googleapis.com artifactregistry.googleapis.com cloudbuild.googleapis.com

echo "==> 2/7 Artifact Registry (free up to 500MB)"
gcloud artifacts repositories create "$REPO" --repository-format=docker \
  --location="$REGION" --project="$PROJECT_ID" 2>/dev/null || echo "    repo exists"

echo "==> 3/7 Cloud SQL PostgreSQL (delete after demo to stay in free credits!)"
gcloud sql instances create "$DB_INSTANCE" \
  --database-version=POSTGRES_16 --tier=db-custom-1-3840 \
  --region="$REGION" --edition=Express --storage-size=10GB \
  --project="$PROJECT_ID" 2>/dev/null || echo "    instance exists"
AGENT_DB_USER="agent"; AGENT_DB_PASS="$(openssl rand -hex 12)"
gcloud sql users create "$AGENT_DB_USER" --instance="$DB_INSTANCE" \
  --password="$AGENT_DB_PASS" --project="$PROJECT_ID" 2>/dev/null || true
gcloud sql databases create orders --instance="$DB_INSTANCE" --project="$PROJECT_ID" 2>/dev/null || true

echo "==> 4/7 Apply schema"
cat db/migrations/001_init.sql | PGPASSWORD="$AGENT_DB_PASS" psql \
  "host=$(gcloud sql instances describe $DB_INSTANCE --format='value(ipAddresses[0].ipAddress)' --project=$PROJECT_ID) \
   dbname=orders user=$AGENT_DB_USER sslmode=disable" \
  || echo "    (run manually from an IP allow-listed host or via Cloud Shell)"

echo "==> 5/7 Build container & deploy Cloud Run (min-instances=0 => scales to zero, free)"
gcloud builds submit --tag "$IMAGE" --project="$PROJECT_ID" .
gcloud run deploy "$SERVICE" --image "$IMAGE" --region "$REGION" \
  --allow-unauthenticated --min-instances 0 --max-instances 2 \
  --memory 256Mi --cpu 1 --timeout 120s --port 8080 \
  --set-env-vars "DB_DRIVER=sqlite3,DB_DSN=/tmp/agent.db,POLL_SECONDS=30" \
  --project "$PROJECT_ID"
URL="$(gcloud run services describe "$SERVICE" --region "$REGION" --format 'value(status.url)' --project "$PROJECT_ID")"
echo "    Service URL: $URL"

echo "==> 6/7 Cloud Scheduler: POST /run-cycle every 5 min (5 free invocations/day beyond that is ~$0.10/mo)"
gcloud scheduler jobs create http order-agent-cycle --schedule='*/5 * * * *' \
  --uri "${URL}/run-cycle" --http-method POST --time-zone "$TZ" \
  --project "$PROJECT_ID" 2>/dev/null || echo "    job exists"

echo "==> 7/7 Smoke test"
curl -sS "${URL}/healthz"; echo
curl -sS -X POST "${URL}/graphql" -H 'content-type: application/json' \
  -d '{"query":"mutation { createOrder(customerId:\"cust-900\", items:[{sku:\"STK-VINYL-4\", quantity:2}]) { id status } }"}'; echo
curl -sS -X POST "${URL}/graphql" -H 'content-type: application/json' \
  -d '{"query":"mutation { processPendingOrders }"}'; echo
curl -sS "${URL}/metrics"; echo
echo "Done. Teardown: see docs/DEPLOYMENT.md 'Teardown' section."
