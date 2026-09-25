# Video Guide Script — "Build an Autonomous Order Fulfillment Agent in Go" (~18–22 min)

A complete shot-by-shot plan with narration, on-screen actions and timestamps.
Record with OBS/QuickTime + `asciinema` for the terminal segments. Each section lists what to SHOW and what to SAY.

## 0:00 – Cold open & problem statement (60s)
SHOW: Sticker-Mule-style order dashboard mock; counter ticking up "orders placed today".
SAY: "Thousands of orders a day. Every one needs the same five steps: check stock, print a label, update the database, email the customer. That's hours of manual work — today we replace it with an autonomous agent written in Go."

## 1:00 – Architecture whiteboard (2 min)
SHOW: draw/diagram (Excalidraw): Cloud Scheduler → Cloud Run (Go agent) ⇄ Cloud SQL Postgres; agent tools → Ship / Give / Notify / Inventory APIs; GraphQL layer on top.
SAY: walk through the supervisor pattern: poll `status='new'`, delegate each step to typed tools, persist every decision as an event for evaluation. Emphasize: state lives in the DB, so restarts never lose context.

## 3:00 – Repo tour (2 min)
SHOW: VS Code, `tree -L 2`. Open files quickly.
SAY: "`internal/agent` is the coordinator workflow, `internal/tools` are the platform integrations (live HTTP or deterministic mocks), `internal/store` is persistence + KPI math, `internal/server` exposes everything over GraphQL. Tests in `agent_test.go` are our offline regression suite."

## 5:00 – Core code walkthrough (4 min)
SHOW: `internal/agent/agent.go` — scroll `FulfillOrder`.
SAY: narrate each numbered step live: claim order (`processing`) → inventory validation per line item → giveaway eligibility (non-fatal by design) → Ship label generation → tracking persisted → Notify webhook → `shipped`. Point out error handling: infra failures ⇒ `failed`; business blockers ⇒ `on_hold` for humans. Highlight structured `slog` JSON logs = free Cloud Logging ingestion.

## 9:00 – Live build & test (2 min)
SHOW terminal:
```bash
go build ./... && go test ./... -v
```
SAY: "The scenario table in the tests mirrors production edge cases: happy path, promo eligibility, out-of-stock hold, multi-line partial stock-out. This is the offline eval loop — run it in CI before every deploy."

## 11:00 – End-to-end demo (3 min)
SHOW terminal:
```bash
./scripts/local-demo.sh        # seeds 12 orders, agent fulfills them, prints traces + KPIs
```
Zoom into the trace output for `ord-0001`, then the KPI block. Then open http://localhost:8080/graphql and run live:
```graphql
mutation { createOrder(customerId:"cust-003", items:[{sku:"STK-KISS-100", quantity:5}]) { id status } }
mutation { processPendingOrders }
query { orderEvents(orderId:"…") { step success detail } }
query { metrics { automationRatePct avgProcessingSeconds errorRatePct hoursSaved estimatedCostSavedUsd } }
```
SAY: "Watch this order go from `new` to `shipped` in milliseconds, with a full audit trail — that's first-contact resolution data you can hand to ops."

## 14:00 – Deploy to GCP (3 min)
SHOW terminal, sped-up where safe:
```bash
PROJECT_ID=demo ./deploy/deploy.sh
URL=$(gcloud run services describe order-fulfillment-agent --region us-central1 --format 'value(status.url)')
BASE_URL=$URL ./scripts/smoke-test.sh
curl $URL/metrics
```
SAY: call out free-tier math: Cloud Run scale-to-zero ≈ $0 idle; scheduler job ~$0.10; Cloud SQL shared core covered by trial credits; teardown commands at the end of the guide.

## 17:00 – Measuring results & when to kill the agent (2 min)
SHOW: Cloud Logging query + a simple Looker Studio/Grafana panel fed by `/metrics` (or the `fulfillment_kpis` SQL view).
SAY: "Automation rate, average processing time, error rate, hours saved × loaded hourly cost. If error rate trends up or automation drops below X%, we roll back — measure results, remove agents that don't deliver."

## 19:00 – Wrap-up & extensions (1 min)
SHOW: README roadmap list.
SAY: "Next steps: plug Gemini via Vertex AI to parse free-text order notes, swap mocks for real Ship/Give/Notify endpoints via env vars, add LangSmith/OTel tracing. Full code + deployment + this script are in the repo — link in description."

### Production tips
- Record terminal at 1440p+, font ≥ 18pt; pre-run the demo once so caches are warm.
- Keep a backup screen recording of the successful demo in case live network fails.
- Captions: generate with Whisper; upload SRT for SEO.
