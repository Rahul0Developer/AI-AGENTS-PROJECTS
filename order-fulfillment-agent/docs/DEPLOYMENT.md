# Deployment Guide — Intelligent Order Fulfillment Coordinator

Two paths: **A) Free local demo** (no cloud account needed) and **B) GCP production deploy on free resources**.

## Prerequisites
- Go 1.23+ (`go version`) and a C compiler for the SQLite driver (`gcc` / Xcode CLT on macOS).
- For path B only: `gcloud` CLI (`gcloud auth login`), a GCP project with billing enabled (the $300 free trial credits cover everything below for months).

---

## A. Local demo (5 minutes, $0)

```bash
./scripts/local-demo.sh          # builds, runs end-to-end demo, then serves on :8080
```

What you'll see:
1. 12 orders seeded into SQLite (`./data/agent.db`).
2. The agent autonomously processes them: inventory check → giveaway check → Ship label → DB state update → customer notification. Out-of-stock orders are parked as `on_hold`.
3. A per-order audit trace and the KPI summary (automation rate, avg processing time, error rate, hours & dollars saved).
4. The HTTP server starts — open **http://localhost:8080/graphql** for the GraphiQL playground.

Run the smoke tests in another terminal:
```bash
BASE_URL=http://localhost:8080 ./scripts/smoke-test.sh
```

Useful commands:
```bash
go test ./...                    # offline regression suite (curated scenarios)
./bin/agent seed --orders 20     # add more pending orders
./bin/agent run-once             # one fulfillment pass (what Cloud Scheduler calls)
./bin/agent metrics              # print KPI JSON
```

### Example GraphQL calls
```graphql
# create an order
mutation { createOrder(customerId:"cust-003", items:[{sku:"STK-KISS-100", quantity:5}]) { id status } }

# trigger the autonomous cycle; returns number of orders attempted
mutation { processPendingOrders }

# inspect the agent's decision trace for one order
query { orderEvents(orderId:"ord-0001") { step success detail timestamp } }

# KPIs for "measure results"
query { metrics { automationRatePct avgProcessingSeconds errorRatePct hoursSaved estimatedCostSavedUsd } }
```

---

## B. Deploy to Google Cloud (free-tier friendly)

Architecture: **Cloud Run** (containerized agent, scales to zero) + **Cloud SQL PostgreSQL** (state store) + **Cloud Scheduler** (triggers `/run-cycle`) + **Artifact Registry** (image). Optional: Vertex AI Gemini for NLP steps; LangSmith/OpenTelemetry for traces.

```bash
PROJECT_ID=my-gcp-project ./deploy/deploy.sh
```

The script does, in order:
| Step | Resource | Cost control |
|---|---|---|
| 1 | Enable APIs (Run, SQL, Scheduler, Artifact Registry, Cloud Build) | free |
| 2 | Artifact Registry docker repo | free ≤ 500 MB |
| 3 | Cloud SQL `db-custom-1-3840` (shared core, Express edition) | paid hourly but covered by $300 trial credits — **delete when done** |
| 4 | Apply `db/migrations/001_init.sql` (orders, fulfillment_events, KPI view) | — |
| 5 | `gcloud builds submit` + `gcloud run deploy --min-instances 0` | Cloud Run free tier: 2M vCPU-s + 4M GiB-s/mo; scale-to-zero ⇒ ~$0 for an idle agent |
| 6 | Cloud Scheduler job `*/5 * * * *` POSTing `/run-cycle` | first 3 jobs free, ~$0.10 after |
| 7 | Smoke test with curl against the public URL | — |

### Point the agent at Cloud SQL
After deploying, set env vars on the service (use [Cloud Run `--add-sql-instance`](https://cloud.google.com/run/docs/configuring/connecting-cloudsql) for the Serverless VPC connector automatically):

```bash
gcloud run services update order-fulfillment-agent --region us-central1 \
  --set-env-vars "DB_DRIVER=postgres,DB_DSN=host=/cloudsql/PROJECT:REGION:agent-pg dbname=orders user=agent password=$AGENT_DB_PASS sslmode=disable" \
  --add-sql-instance agent-pg
```
(For the Postgres DSN over the Unix socket, install the schema first from Cloud Shell:
`psql "postgresql://agent@$AGENT_DB_PASS@127.0.0.1/orders" ...` or via `gcloud sql connect agent-pg --user=agent < db/migrations/001_init.sql`.)

### Verify
```bash
URL=$(gcloud run services describe order-fulfillment-agent --region us-central1 --format 'value(status.url)')
curl -s $URL/healthz
BASE_URL=$URL ./scripts/smoke-test.sh
curl -s $URL/metrics   # KPI JSON
```

### Teardown (avoid surprise bills)
```bash
gcloud scheduler jobs delete order-agent-cycle
gcloud run services delete order-fulfillment-agent --region us-central1
gcloud sql instances delete agent-pg
gcloud artifacts repositories delete agents --location us-central1
```

## Connecting real internal tools (Ship / Give / Notify / Inventory)
Set any of these env vars to switch that tool from deterministic mock mode to live HTTP mode — no code change required:
`INVENTORY_API_URL`, `SHIP_API_URL`, `GIVE_API_URL`, `NOTIFY_WEBHOOK_URL`. Store secrets in Secret Manager and mount them via `--set-secrets`.

## Observability & evaluation
- Every tool call is persisted to `fulfillment_events` (order-level trace = your offline eval dataset).
- Structured JSON logs (`slog`) ship to Cloud Logging for free; build a Logs-Based Metric on `"agent: order fulfilled"` for dashboards/alerts.
- Query KPIs anytime: `GET /metrics` or GraphQL `metrics { ... }`; on Postgres use the `fulfillment_kpis` view.
- Optional: wrap tool calls with OpenTelemetry/LangSmith spans to analyze *why* a specific order failed (tool param error vs network timeout vs bad reasoning).
