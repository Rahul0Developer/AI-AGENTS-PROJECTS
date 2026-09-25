# Intelligent Order Fulfillment Coordinator (Go)

An **autonomous AI agent** that moves e-commerce orders from `new` → `shipped` without human intervention:
polls PostgreSQL for pending orders, validates inventory, checks promotional eligibility (Give), generates
shipping labels (Ship), updates state, and notifies the customer (Notify). Every decision is persisted as an
audit-trail event, and business KPIs (automation rate, processing time, error rate, hours & dollars saved)
are queryable over GraphQL — built to demonstrate the "connect agents to internal tools and measure results"
loop.

## Features
- 🤖 Closed-loop autonomous workflow: detect → inventory → giveaway → label → state update → notify
- 🔌 Pluggable tool integrations (Inventory/Ship/Give/Notify): live HTTP mode via env vars, deterministic mock mode for dev/CI
- 🧠 Durable state in Cloud SQL PostgreSQL (restart-safe); SQLite fallback for zero-setup local demos
- 🔭 GraphQL API + GraphiQL playground exposing orders, per-order traces, metrics, and the agent's own tool functions (`generateShippingLabel`, `checkGiveawayEligibility`, `processPendingOrders`)
- 📊 Evaluation framework: offline regression scenario suite (`testdata/scenarios.json` + Go tests) and online KPI computation (`/metrics`, GraphQL, `fulfillment_kpis` SQL view)
- ☁️ Free-tier-friendly GCP deployment: Cloud Run (scale-to-zero) + Cloud Scheduler + Cloud SQL Express shared core; one-command deploy script
- ⚡ Concurrent order processing with bounded parallelism (Go goroutines + semaphore)

## Architecture
```
Cloud Scheduler ──POST──▶ /run-cycle ◀──────────────┐
                          ┌───────────────────────────┴─────────┐
                          │  Cloud Run: Go agent                │
                          │  Supervisor (internal/agent)        │
                          │   ├─ Inventory tool ─▶ inventory API│
                          │   ├─ Give tool      ─▶ Give API     │
                          │   ├─ Ship tool      ─▶ Ship API     │
                          │   └─ Notify tool    ─▶ webhook      │
                          │  GraphQL API (internal/server)      │
                          └───────────────┬─────────────────────┘
                                          ▼
                        Cloud SQL PostgreSQL: orders · fulfillment_events · kpi view
```

## Quick start (local, $0)
```bash
make demo                 # seed 12 orders, fulfill them, print traces + KPIs
make serve                # http://localhost:8080/graphql (GraphiQL) + autonomous polling
make test                 # offline regression suite
./scripts/smoke-test.sh   # exercise GraphQL end-to-end
```

## Deploy to Google Cloud
See **[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)** (free-tier resource choices, step-by-step, teardown).
One command:
```bash
PROJECT_ID=my-gcp-project ./deploy/deploy.sh
```

## Video guide
A complete shot-by-shot recording script (timestamps, narration, on-screen commands) is in
**[docs/VIDEO_GUIDE.md](docs/VIDEO_GUIDE.md)** — follow it to produce a ~20-minute YouTube walkthrough of this project.

## KPIs measured
| Metric | Definition | Source |
|---|---|---|
| Automation Rate | shipped-without-human / total new × 100% | `orders` table |
| Avg Processing Time | mean(`shipped_at` − `created_at`) | `orders` table |
| Error Rate | failed / (shipped+failed) × 100% | `orders` + `fulfillment_events` |
| Hours Saved | auto_processed × manual-min-per-order ÷ 60 | computed |
| Cost Saved | hours saved × loaded hourly cost | computed |

## Project layout
```
cmd/agent/            CLI entrypoints: serve | run-once | seed | metrics | demo
internal/agent/       supervisor workflow + unit/regression tests
internal/tools/       Inventory / Ship / Give / Notify clients (live or mock)
internal/store/       persistence, migrations, KPI computation
internal/server/      GraphQL schema + REST endpoints (/graphql, /run-cycle, /metrics, /healthz)
db/migrations/        Cloud SQL PostgreSQL DDL (+ fulfillment_kpis view)
graphql/schema.graphqls  reference schema contract
deploy/deploy.sh      one-shot GCP free-tier deployment
scripts/              local-demo.sh, smoke-test.sh
testdata/             curated offline regression scenarios
docs/                 DEPLOYMENT.md, VIDEO_GUIDE.md
Dockerfile            distroless container for Cloud Run
```

## Configuration (env vars)
| Var | Default | Purpose |
|---|---|---|
| `DB_DRIVER` / `DB_DSN` | sqlite3 / ./data/agent.db | `postgres` + DSN for Cloud SQL |
| `PORT` | 8080 | HTTP port (Cloud Run injects) |
| `POLL_SECONDS` | 15 | autonomous loop interval |
| `INVENTORY_API_URL`, `SHIP_API_URL`, `GIVE_API_URL`, `NOTIFY_WEBHOOK_URL` | unset = mocks | switch tools to live internal platforms |
| `MANUAL_MINUTES_PER_ORDER`, `OPS_HOURLY_COST_USD` | 6 / 25 | ROI assumptions |

## Roadmap
- Gemini (Vertex AI) planner for free-text order notes & notification drafting behind the `Planner` seam
- OpenTelemetry/LangSmith span export for failure forensics
- Hasura/Wundergraph supergraph over the same Postgres schema
- Idempotency keys + dead-letter queue for retry storms
