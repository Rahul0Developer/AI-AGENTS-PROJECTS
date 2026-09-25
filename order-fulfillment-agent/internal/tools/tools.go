// Package tools implements the agent's custom tools: thin, well-typed clients
// for the Ship (labels), Give (promotional giveaways), Notify (customer
// messaging) and Inventory platforms. Each client works in two modes:
//
//   - Live mode: real HTTP calls to the internal platform endpoints
//     (configured via SHIP_API_URL, GIVE_API_URL, NOTIFY_WEBHOOK_URL,
//     INVENTORY_API_URL).
//   - Mock mode: deterministic in-process simulation used for local dev,
//     CI and the offline regression suite. Enabled when no URL is set or
//     AGENT_MOCK_MODE=true.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

func postJSON(ctx context.Context, url string, payload any, out any) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http call %s: %w", url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ---------- Inventory ----------

// InventoryResult reports stock availability for one SKU.
type InventoryResult struct {
	SKU        string `json:"sku"`
	Available  bool   `json:"available"`
	QuantityOn int    `json:"quantity_on_hand"`
}

// InventoryClient checks product availability before fulfillment.
type InventoryClient struct {
	BaseURL string // empty => mock mode
	Mock    *MockInventory
}

// MockInventory lets tests seed stock levels per SKU.
type MockInventory struct {
	mu    sync.Mutex
	stock map[string]int
}

// NewMockInventory seeds a stock table; SKUs missing from it are considered out of stock.
func NewMockInventory(stock map[string]int) *MockInventory {
	cp := map[string]int{}
	for k, v := range stock {
		cp[k] = v
	}
	return &MockInventory{stock: cp}
}

// Check returns whether quantity units of sku are available.
func (c *InventoryClient) Check(ctx context.Context, sku string, quantity int) (InventoryResult, error) {
	if c.BaseURL == "" {
		c.Mock.mu.Lock()
		on := c.Mock.stock[sku]
		c.Mock.mu.Unlock()
		ok := on >= quantity
		if !ok {
			// simulate an API that reports partial stock as unavailable
			slog.Info("inventory(mock)", "sku", sku, "need", quantity, "on_hand", on, "available", ok)
		}
		return InventoryResult{SKU: sku, Available: ok, QuantityOn: on}, nil
	}
	var res InventoryResult
	url := fmt.Sprintf("%s/check?sku=%s&qty=%d", c.BaseURL, sku, quantity)
	err := getJSON(ctx, url, &res)
	return res, err
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http call %s: %w", url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

// ---------- Ship (labels) ----------

// LabelResult is returned by the Ship platform after label creation.
type LabelResult struct {
	TrackingNumber string `json:"tracking_number"`
	Carrier        string `json:"carrier"`
	LabelURL       string `json:"label_url"`
}

// ShipClient generates shipping labels through the internal Ship platform.
type ShipClient struct {
	BaseURL string // empty => mock mode
}

// GenerateShippingLabel creates a label for the given order/destination.
func (c *ShipClient) GenerateShippingLabel(ctx context.Context, orderID, customerID string) (LabelResult, error) {
	if c.BaseURL == "" {
		// Deterministic mock tracking number derived from the order ID.
		sum := 0
		for _, r := range orderID {
			sum += int(r)
		}
		carrier := []string{"UPS", "FedEx", "USPS"}[sum%3]
		res := LabelResult{
			TrackingNumber: fmt.Sprintf("1Z%016d", sum*7919),
			Carrier:        carrier,
			LabelURL:       fmt.Sprintf("https://ship.mock.internal/labels/%s.pdf", orderID),
		}
		slog.Info("ship(mock): label generated", "order", orderID, "tracking", res.TrackingNumber, "carrier", carrier)
		return res, nil
	}
	var res LabelResult
	err := postJSON(ctx, c.BaseURL+"/v1/labels", map[string]string{
		"order_id": orderID, "customer_id": customerID,
	}, &res)
	return res, err
}

// ---------- Give (promotional giveaways) ----------

// GiveawayResult reports whether a customer qualifies for a bonus item.
type GiveawayResult struct {
	Eligible bool   `json:"eligible"`
	Item     string `json:"item,omitempty"`
	Reason   string `json:"reason"`
}

// GiveClient queries the internal Give platform.
type GiveClient struct {
	BaseURL string // empty => mock mode
	Mock    *MockGive
}

// MockGive lets tests control eligibility deterministically.
type MockGive struct {
	mu            sync.Mutex
	EligibleEvery int // customers whose numeric suffix % N == 0 qualify
	BonusItem     string
}

// CheckGiveawayEligibility asks Give if the customer earns a promo item.
func (c *GiveClient) CheckGiveawayEligibility(ctx context.Context, customerID string) (GiveawayResult, error) {
	if c.BaseURL == "" {
		c.Mock.mu.Lock()
		defer c.Mock.mu.Unlock()
		n, _ := fmt.Sscanf(customerID, "cust-%d", new(int))
		_ = n
		digits := 0
		fmt.Sscanf(strings.TrimPrefix(customerID, "cust-"), "%d", &digits)
		eligible := c.Mock.EligibleEvery > 0 && digits%c.Mock.EligibleEvery == 0
		res := GiveawayResult{Eligible: eligible, Reason: "loyalty tier check (mock)"}
		if eligible {
			res.Item = c.Mock.BonusItem
		}
		slog.Info("give(mock): eligibility", "customer", customerID, "eligible", eligible, "item", res.Item)
		return res, nil
	}
	var res GiveawayResult
	err := getJSON(ctx, c.BaseURL+"/v1/eligibility?customer_id="+customerID, &res)
	return res, err
}

// ---------- Notify (customer messaging) ----------

// NotifyClient sends customer notifications via webhook (Notify platform).
type NotifyClient struct {
	WebhookURL string            // empty => mock mode (logs only)
	Sent       chan Notification // observable in tests/demo
}

// Notification is the payload delivered to the customer.
type Notification struct {
	OrderID    string    `json:"order_id"`
	CustomerID string    `json:"customer_id"`
	Channel    string    `json:"channel"`
	Message    string    `json:"message"`
	SentAt     time.Time `json:"sent_at"`
}

// SendShipmentNotification notifies the customer their order shipped.
func (c *NotifyClient) SendShipmentNotification(ctx context.Context, orderID, customerID, trackingURL string) error {
	msg := fmt.Sprintf("Your Sticker Mule order %s has been shipped! Track your package here: %s", orderID, trackingURL)
	n := Notification{OrderID: orderID, CustomerID: customerID, Channel: "email+sms", Message: msg, SentAt: time.Now().UTC()}
	if c.WebhookURL == "" {
		slog.Info("notify(mock): message sent", "order", orderID, "customer", customerID, "message", msg)
		select {
		case c.Sent <- n:
		default:
		}
		return nil
	}
	return postJSON(ctx, c.WebhookURL, n, nil)
}
