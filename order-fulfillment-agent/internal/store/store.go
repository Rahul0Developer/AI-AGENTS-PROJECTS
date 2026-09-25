package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store wraps a *sql.DB and exposes order/agent persistence operations.
// Works with SQLite (go-sqlite3 driver) for local dev; the SQL is written to
// also run on PostgreSQL via the same interface (see db/migrations for PG DDL).
type Store struct {
	DB      *sql.DB
	Dialect string // "sqlite" | "postgres"
}

// Open creates a store backed by the given driver/dsn.
func Open(driver, dsn string) (*Store, error) {
	dialect := "sqlite"
	if strings.Contains(driver, "pg") {
		dialect = "postgres"
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if dialect == "sqlite" {
		db.SetMaxOpenConns(1) // avoid sqlite lock contention
	}
	s := &Store{DB: db, Dialect: dialect}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS orders (
id TEXT PRIMARY KEY,
customer_id TEXT NOT NULL,
items_json TEXT NOT NULL,
status TEXT NOT NULL DEFAULT 'new',
tracking_number TEXT DEFAULT '',
fulfillment_notes TEXT DEFAULT '',
error_msg TEXT DEFAULT '',
created_at TIMESTAMP NOT NULL,
updated_at TIMESTAMP NOT NULL,
shipped_at TIMESTAMP NULL
)`,
		`CREATE TABLE IF NOT EXISTS fulfillment_events (
id INTEGER PRIMARY KEY {AUTOINCREMENT},
order_id TEXT NOT NULL,
step TEXT NOT NULL,
success BOOLEAN NOT NULL,
detail TEXT DEFAULT '',
timestamp TIMESTAMP NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status)`,
	}
	for _, stmt := range stmts {
		if s.Dialect == "postgres" {
			stmt = strings.ReplaceAll(stmt, "{AUTOINCREMENT}", "GENERATED ALWAYS AS IDENTITY")
		} else {
			stmt = strings.ReplaceAll(stmt, "{AUTOINCREMENT}", "AUTOINCREMENT")
		}
		if _, err := s.DB.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w\nstmt: %s", err, stmt)
		}
	}
	return nil
}

// CreateOrder inserts a new order with status 'new'.
func (s *Store) CreateOrder(o *Order) error {
	now := time.Now().UTC()
	o.CreatedAt, o.UpdatedAt, o.Status = now, now, StatusNew
	items, _ := json.Marshal(o.Items)
	_, err := s.DB.Exec(
		`INSERT INTO orders (id, customer_id, items_json, status, created_at, updated_at)
 VALUES (?, ?, ?, ?, ?, ?)`,
		o.ID, o.CustomerID, string(items), string(o.Status), now, now)
	return err
}

func scanOrder(row interface{ Scan(...any) error }) (Order, error) {
	var o Order
	var items, created, updated string
	var shipped sql.NullString
	err := row.Scan(&o.ID, &o.CustomerID, &items, &o.Status, &o.TrackingNumber,
		&o.FulfillmentNotes, &o.ErrorMsg, &created, &updated, &shipped)
	if err != nil {
		return o, err
	}
	_ = json.Unmarshal([]byte(items), &o.Items)
	o.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	o.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if shipped.Valid {
		t, _ := time.Parse(time.RFC3339Nano, shipped.String)
		o.ShippedAt = &t
	}
	return o, nil
}

const orderCols = `id, customer_id, items_json, status, tracking_number, fulfillment_notes, error_msg, created_at, updated_at, shipped_at`

// Timestamps are stored as RFC3339 text so both SQLite and Postgres round-trip identically.

// GetOrder fetches one order by ID.
func (s *Store) GetOrder(id string) (Order, error) {
	return scanOrder(s.DB.QueryRow(`SELECT `+orderCols+` FROM orders WHERE id = ?`, id))
}

// ListAllOrders returns orders of any status, most recently updated first.
func (s *Store) ListAllOrders(limit int) ([]Order, error) {
	rows, err := s.DB.Query(`SELECT `+orderCols+` FROM orders ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ListOrdersByStatus returns orders in the given status, oldest first.
func (s *Store) ListOrdersByStatus(status OrderStatus, limit int) ([]Order, error) {
	rows, err := s.DB.Query(`SELECT `+orderCols+` FROM orders WHERE status = ? ORDER BY created_at ASC LIMIT ?`,
		string(status), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// UpdateOrderStatus sets status plus optional fulfillment fields.
func (s *Store) UpdateOrderStatus(id string, status OrderStatus, tracking, notes, errMsg string, markShipped bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	q := `UPDATE orders SET status = ?, tracking_number = ?, fulfillment_notes = ?, error_msg = ?, updated_at = ?`
	args := []any{string(status), tracking, notes, errMsg, now}
	if markShipped {
		q += `, shipped_at = ?`
		args = append(args, now)
	}
	q += ` WHERE id = ?`
	args = append(args, id)
	_, err := s.DB.Exec(q, args...)
	return err
}

// LogEvent appends an audit-trail entry for one agent step.
func (s *Store) LogEvent(orderID, step string, success bool, detail string) error {
	_, err := s.DB.Exec(
		`INSERT INTO fulfillment_events (order_id, step, success, detail, timestamp) VALUES (?, ?, ?, ?, ?)`,
		orderID, step, success, detail, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// EventsForOrder returns the audit trail of an order.
func (s *Store) EventsForOrder(orderID string) ([]FulfillmentEvent, error) {
	rows, err := s.DB.Query(`SELECT id, order_id, step, success, detail, timestamp
FROM fulfillment_events WHERE order_id = ? ORDER BY id ASC`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FulfillmentEvent
	for rows.Next() {
		var e FulfillmentEvent
		var ts string
		if err := rows.Scan(&e.ID, &e.OrderID, &e.Step, &e.Success, &e.Detail, &ts); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ComputeMetrics derives the KPI table from persisted data.
// manualMinutesPerOrder: average minutes a human spends per order manually.
// hourlyCostUSD: loaded cost per ops-hour, used for the savings estimate.
func (s *Store) ComputeMetrics(manualMinutesPerOrder, hourlyCostUSD float64) (MetricsSummary, error) {
	var m MetricsSummary
	var total, auto, failed int
	var avgSec sql.NullFloat64

	err := s.DB.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&total)
	if err != nil {
		return m, err
	}
	err = s.DB.QueryRow(`SELECT COUNT(*) FROM orders WHERE status = 'shipped'`).Scan(&auto)
	if err != nil {
		return m, err
	}
	err = s.DB.QueryRow(`SELECT COUNT(*) FROM orders WHERE status = 'failed'`).Scan(&failed)
	if err != nil {
		return m, err
	}
	// Average processing seconds between created_at and shipped_at (text timestamps sort lexically for RFC3339 UTC).
	avgSec, err = s.avgProcessingSeconds()
	if err != nil {
		return m, err
	}

	m.TotalOrders = total
	m.AutoProcessed = auto
	m.FailedAttempts = failed
	if total > 0 {
		m.AutomationRatePct = float64(auto) / float64(total) * 100
	}
	attempted := auto + failed
	if attempted > 0 {
		m.ErrorRatePct = float64(failed) / float64(attempted) * 100
	}
	if avgSec.Valid {
		m.AvgProcessingSeconds = avgSec.Float64
	}
	m.ManualMinutesPerOrder = manualMinutesPerOrder
	m.HoursSaved = float64(auto) * manualMinutesPerOrder / 60.0
	m.EstimatedCostSavedUSD = m.HoursSaved * hourlyCostUSD
	return m, nil
}

func (s *Store) avgProcessingSeconds() (sql.NullFloat64, error) {
	rows, err := s.DB.Query(`SELECT created_at, shipped_at FROM orders WHERE shipped_at IS NOT NULL AND shipped_at != ''`)
	if err != nil {
		return sql.NullFloat64{}, err
	}
	defer rows.Close()
	var sum, n float64
	for rows.Next() {
		var c, sh string
		if err := rows.Scan(&c, &sh); err != nil {
			return sql.NullFloat64{}, err
		}
		ct, err1 := time.Parse(time.RFC3339Nano, c)
		st, err2 := time.Parse(time.RFC3339Nano, sh)
		if err1 != nil || err2 != nil {
			continue
		}
		sum += st.Sub(ct).Seconds()
		n++
	}
	if n == 0 {
		return sql.NullFloat64{}, nil
	}
	return sql.NullFloat64{Float64: sum / n, Valid: true}, rows.Err()
}
