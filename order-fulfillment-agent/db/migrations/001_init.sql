-- Cloud SQL for PostgreSQL schema for the Intelligent Order Fulfillment Coordinator.
-- Apply with:  psql "$DATABASE_URL" -f db/migrations/001_init.sql

CREATE TABLE IF NOT EXISTS orders (
    id                TEXT PRIMARY KEY,
    customer_id       TEXT NOT NULL,
    items_json        JSONB NOT NULL,
    status            TEXT NOT NULL DEFAULT 'new'
                      CHECK (status IN ('new','processing','shipped','failed','on_hold')),
    tracking_number   TEXT DEFAULT '',
    fulfillment_notes TEXT DEFAULT '',
    error_msg         TEXT DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    shipped_at        TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_orders_status_created ON orders (status, created_at);
CREATE INDEX IF NOT EXISTS idx_orders_customer ON orders (customer_id);

-- Audit trail / agent session trace: one row per tool invocation per order.
CREATE TABLE IF NOT EXISTS fulfillment_events (
    id        BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id  TEXT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    step      TEXT NOT NULL,   -- detect | inventory_check | giveaway_check | label_generation | status_update | notification
    success   BOOLEAN NOT NULL,
    detail    TEXT DEFAULT '',
    timestamp TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_events_order ON fulfillment_events (order_id);

-- KPI view used by dashboards / the "measure results" requirement.
CREATE OR REPLACE VIEW fulfillment_kpis AS
SELECT
    COUNT(*)                                                        AS total_orders,
    COUNT(*) FILTER (WHERE status = 'shipped')                      AS auto_processed,
    ROUND(100.0 * COUNT(*) FILTER (WHERE status = 'shipped') / NULLIF(COUNT(*),0), 2) AS automation_rate_pct,
    COUNT(*) FILTER (WHERE status = 'failed')                       AS failed_attempts,
    ROUND(100.0 * COUNT(*) FILTER (WHERE status = 'failed')
          / NULLIF(COUNT(*) FILTER (WHERE status IN ('shipped','failed')),0), 2) AS error_rate_pct,
    ROUND(AVG(EXTRACT(EPOCH FROM (shipped_at - created_at)))
          FILTER (WHERE shipped_at IS NOT NULL)::numeric, 2)        AS avg_processing_seconds
FROM orders;
