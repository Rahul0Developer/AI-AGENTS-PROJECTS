// Package agent implements the Intelligent Order Fulfillment Coordinator:
// a supervisor that autonomously drives orders through the workflow
//
// detect -> validate inventory -> check giveaway -> generate label
//
//	-> update state -> notify customer
//
// Design notes (ADK-style): each workflow step is modeled as a "tool" with a
// typed input/output contract, and the supervisor records every tool
// invocation into fulfillment_events (the session/trace store). This mirrors
// the Agent Development Kit's tool-calling loop while keeping the core logic
// deterministic and unit-testable. An optional LLM planner (Vertex AI Gemini)
// can be plugged in via the Planner interface for free-text order notes; when
// GOOGLE_API_KEY is unset the rule-based planner is used, so the agent runs
// fully offline on Cloud Run's free tier.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/stickermule-demo/order-fulfillment-agent/internal/store"
	"github.com/stickermule-demo/order-fulfillment-agent/internal/tools"
)

// Tools bundles the agent's integrated platforms.
type Tools struct {
	Inventory *tools.InventoryClient
	Ship      *tools.ShipClient
	Give      *tools.GiveClient
	Notify    *tools.NotifyClient
}

// Coordinator is the supervisor agent polling the orders table.
type Coordinator struct {
	Store     *store.Store
	Tools     *Tools
	BatchSize int
	Parallel  int // concurrent orders per batch
}

// New builds a coordinator with sane defaults.
func New(st *store.Store, t *Tools) *Coordinator {
	return &Coordinator{Store: st, Tools: t, BatchSize: 25, Parallel: 4}
}

// RunOnce processes all currently-pending orders once. It returns the number
// of orders attempted. Safe to invoke from a scheduler (Cloud Scheduler ->
// Cloud Run request) or a ticker loop.
func (c *Coordinator) RunOnce(ctx context.Context) (int, error) {
	orders, err := c.Store.ListOrdersByStatus(store.StatusNew, c.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("query new orders: %w", err)
	}
	if len(orders) == 0 {
		slog.Info("agent: no new orders")
		return 0, nil
	}
	slog.Info("agent: picked up orders", "count", len(orders))

	sem := make(chan struct{}, max1(c.Parallel))
	var wg sync.WaitGroup
	for i := range orders {
		wg.Add(1)
		go func(o store.Order) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := c.FulfillOrder(ctx, o); err != nil {
				slog.Error("agent: fulfillment failed", "order", o.ID, "err", err.Error())
			}
		}(orders[i])
	}
	wg.Wait()
	return len(orders), nil
}

// RunLoop polls until ctx is cancelled (for long-running Cloud Run services).
func (c *Coordinator) RunLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := c.RunOnce(ctx); err != nil {
				slog.Error("agent: run cycle error", "err", err.Error())
			}
		}
	}
}

func (c *Coordinator) event(orderID, step string, ok bool, detail string) {
	if err := c.Store.LogEvent(orderID, step, ok, detail); err != nil {
		slog.Error("agent: failed to log event", "order", orderID, "step", step, "err", err.Error())
	}
}

func (c *Coordinator) fail(orderID, step string, err error) error {
	c.event(orderID, step, false, err.Error())
	_ = c.Store.UpdateOrderStatus(orderID, store.StatusFailed, "", "", err.Error(), false)
	return err
}

// FulfillOrder executes the complete autonomous workflow for one order.
func (c *Coordinator) FulfillOrder(ctx context.Context, o store.Order) error {
	start := time.Now()
	c.event(o.ID, "detect", true, fmt.Sprintf("status=%s items=%d", o.Status, len(o.Items)))

	// Step 1 – mark processing (claim the order so retries don't double-handle).
	if err := c.Store.UpdateOrderStatus(o.ID, store.StatusProcessing, "", "", "", false); err != nil {
		return c.fail(o.ID, "status_update", fmt.Errorf("claim order: %w", err))
	}
	c.event(o.ID, "status_update", true, "claimed, status=processing")

	// Step 2 – Inventory Validation: every line item must be in stock.
	for _, it := range o.Items {
		res, err := c.Tools.Inventory.Check(ctx, it.SKU, it.Quantity)
		if err != nil {
			return c.fail(o.ID, "inventory_check", fmt.Errorf("sku %s: %w", it.SKU, err))
		}
		if !res.Available {
			detail := fmt.Sprintf("out of stock: %s (need %d, have %d)", it.SKU, it.Quantity, res.QuantityOn)
			c.event(o.ID, "inventory_check", false, detail)
			// Out-of-stock is a business hold, not an agent error: park for humans.
			_ = c.Store.UpdateOrderStatus(o.ID, store.StatusOnHold, "", detail, "", false)
			return fmt.Errorf(detail)
		}
		c.event(o.ID, "inventory_check", true, fmt.Sprintf("%s x%d available", it.SKU, it.Quantity))
	}

	// Step 3 – Giveaway eligibility via the Give platform (before finalizing).
	notes := ""
	giveRes, err := c.Tools.Give.CheckGiveawayEligibility(ctx, o.CustomerID)
	if err != nil {
		// Non-fatal: log and continue — never block shipping over a promo check.
		c.event(o.ID, "giveaway_check", false, err.Error())
		slog.Warn("agent: giveaway check failed, continuing", "order", o.ID, "err", err.Error())
	} else {
		if giveRes.Eligible {
			notes = fmt.Sprintf("Include bonus item: %s", giveRes.Item)
		}
		c.event(o.ID, "giveaway_check", true, fmt.Sprintf("eligible=%v item=%q reason=%s", giveRes.Eligible, giveRes.Item, giveRes.Reason))
	}

	// Step 4 – Label Generation via the Ship platform.
	label, err := c.Tools.Ship.GenerateShippingLabel(ctx, o.ID, o.CustomerID)
	if err != nil {
		return c.fail(o.ID, "label_generation", err)
	}
	c.event(o.ID, "label_generation", true, fmt.Sprintf("tracking=%s carrier=%s", label.TrackingNumber, label.Carrier))

	// Step 5 – State Update: persist tracking + notes, status stays 'processing' until notified.
	fullNotes := fmt.Sprintf("carrier=%s label=%s %s", label.Carrier, label.LabelURL, notes)
	if err := c.Store.UpdateOrderStatus(o.ID, store.StatusProcessing, label.TrackingNumber, fullNotes, "", false); err != nil {
		return c.fail(o.ID, "status_update", err)
	}
	c.event(o.ID, "status_update", true, "tracking stored, status=processing")

	// Step 6 – Customer Notification via the Notify platform.
	trackingURL := fmt.Sprintf("https://www.stickermule.com/track/%s", label.TrackingNumber)
	if err := c.Tools.Notify.SendShipmentNotification(ctx, o.ID, o.CustomerID, trackingURL); err != nil {
		return c.fail(o.ID, "notification", err)
	}
	c.event(o.ID, "notification", true, "customer notified with tracking link")

	// Step 7 – Close the loop: mark shipped.
	if err := c.Store.UpdateOrderStatus(o.ID, store.StatusShipped, label.TrackingNumber, fullNotes, "", true); err != nil {
		return c.fail(o.ID, "status_update", err)
	}
	c.event(o.ID, "status_update", true, "status=shipped")
	slog.Info("agent: order fulfilled", "order", o.ID, "tracking", label.TrackingNumber, "duration_ms", time.Since(start).Milliseconds())
	return nil
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
