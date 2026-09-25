// Package store provides persistence for orders, fulfillment events and errors.
// It supports two backends: PostgreSQL (Cloud SQL in production) and an
// embedded SQLite driver (local development / demos), selected via Dialect.
package store

import "time"

// OrderStatus enumerates the lifecycle of an order handled by the agent.
type OrderStatus string

const (
	StatusNew        OrderStatus = "new"
	StatusProcessing OrderStatus = "processing"
	StatusShipped    OrderStatus = "shipped"
	StatusFailed     OrderStatus = "failed"
	StatusOnHold     OrderStatus = "on_hold" // e.g. out of stock – needs human review
)

// OrderItem is a single line item inside an order.
type OrderItem struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// Order represents a customer order row in the orders table.
type Order struct {
	ID               string      `json:"id"`
	CustomerID       string      `json:"customer_id"`
	Items            []OrderItem `json:"items"`
	Status           OrderStatus `json:"status"`
	TrackingNumber   string      `json:"tracking_number,omitempty"`
	FulfillmentNotes string      `json:"fulfillment_notes,omitempty"`
	ErrorMsg         string      `json:"error_msg,omitempty"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	ShippedAt        *time.Time  `json:"shipped_at,omitempty"`
}

// FulfillmentEvent records each step the agent took on an order (audit trail).
type FulfillmentEvent struct {
	ID        int64     `json:"id"`
	OrderID   string    `json:"order_id"`
	Step      string    `json:"step"` // inventory_check | label_generation | giveaway_check | status_update | notification
	Success   bool      `json:"success"`
	Detail    string    `json:"detail"`
	Timestamp time.Time `json:"timestamp"`
}

// MetricsSummary aggregates KPIs computed from the orders/events tables.
type MetricsSummary struct {
	TotalOrders           int     `json:"total_orders"`
	AutoProcessed         int     `json:"auto_processed"`      // shipped without human intervention
	AutomationRatePct     float64 `json:"automation_rate_pct"` // auto_processed / total_new
	AvgProcessingSeconds  float64 `json:"avg_processing_seconds"`
	FailedAttempts        int     `json:"failed_attempts"`
	ErrorRatePct          float64 `json:"error_rate_pct"`
	ManualMinutesPerOrder float64 `json:"manual_minutes_per_order"`
	HoursSaved            float64 `json:"hours_saved"`
	EstimatedCostSavedUSD float64 `json:"estimated_cost_saved_usd"`
}
