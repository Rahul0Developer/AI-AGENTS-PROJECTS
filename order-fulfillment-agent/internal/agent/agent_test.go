package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stickermule-demo/order-fulfillment-agent/internal/store"
	"github.com/stickermule-demo/order-fulfillment-agent/internal/tools"
)

func newTestCoord(t *testing.T, stock map[string]int, giveEvery int) (*Coordinator, *store.Store, chan tools.Notification) {
	t.Helper()
	st, err := store.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan tools.Notification, 64)
	coord := New(st, &Tools{
		Inventory: &tools.InventoryClient{Mock: tools.NewMockInventory(stock)},
		Ship:      &tools.ShipClient{},
		Give:      &tools.GiveClient{Mock: &tools.MockGive{EligibleEvery: giveEvery, BonusItem: "sample pack"}},
		Notify:    &tools.NotifyClient{Sent: sent},
	})
	return coord, st, sent
}

// Offline regression scenario table: curated order scenarios with expected outcomes.
func TestWorkflowScenarios(t *testing.T) {
	stock := map[string]int{"SKU-A": 100, "SKU-B": 50, "SKU-OOS": 0}
	cases := []struct {
		name       string
		customer   string
		items      []store.OrderItem
		wantStatus store.OrderStatus
		wantErrIn  string
		wantNote   string // substring expected in fulfillment_notes
	}{
		{"happy path", "cust-001", []store.OrderItem{{SKU: "SKU-A", Quantity: 10}}, store.StatusShipped, "", ""},
		{"giveaway eligible every 3rd", "cust-003", []store.OrderItem{{SKU: "SKU-B", Quantity: 2}}, store.StatusShipped, "", "Include bonus item"},
		{"out of stock -> on_hold", "cust-007", []store.OrderItem{{SKU: "SKU-OOS", Quantity: 1}}, store.StatusOnHold, "", ""},
		{"multi-line partial oos", "cust-005", []store.OrderItem{{SKU: "SKU-A", Quantity: 1}, {SKU: "SKU-OOS", Quantity: 3}}, store.StatusOnHold, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coord, st, _ := newTestCoord(t, stock, 3)
			o := store.Order{ID: "ord-" + strings.ReplaceAll(tc.name, " ", "-"), CustomerID: tc.customer, Items: tc.items}
			if err := st.CreateOrder(&o); err != nil {
				t.Fatal(err)
			}
			_ = coord.FulfillOrder(context.Background(), o)
			got, err := st.GetOrder(o.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s (err=%q)", got.Status, tc.wantStatus, got.ErrorMsg)
			}
			if tc.wantStatus == store.StatusShipped {
				if got.TrackingNumber == "" || got.ShippedAt == nil {
					t.Fatalf("shipped order missing tracking/shipped_at: %+v", got)
				}
			}
			if tc.wantNote != "" && !strings.Contains(got.FulfillmentNotes, tc.wantNote) {
				t.Fatalf("notes %q missing %q", got.FulfillmentNotes, tc.wantNote)
			}
			evs, _ := st.EventsForOrder(o.ID)
			if len(evs) < 3 {
				t.Fatalf("expected audit trail >=3 events, got %d", len(evs))
			}
		})
	}
}

func TestRunOnceAndMetrics(t *testing.T) {
	stock := map[string]int{"SKU-A": 100, "SKU-OOS": 0}
	coord, st, sent := newTestCoord(t, stock, 3)
	for i := 1; i <= 8; i++ {
		sku := "SKU-A"
		if i == 7 {
			sku = "SKU-OOS" // one out-of-stock order
		}
		o := store.Order{ID: sprintf("ord-%d", i), CustomerID: sprintf("cust-%03d", i),
			Items: []store.OrderItem{{SKU: sku, Quantity: 5}}}
		if err := st.CreateOrder(&o); err != nil {
			t.Fatal(err)
		}
	}
	n, err := coord.RunOnce(context.Background())
	if err != nil || n != 8 {
		t.Fatalf("RunOnce = %d, %v; want 8, nil", n, err)
	}
	// nothing left 'new'
	if again, _ := coord.RunOnce(context.Background()); again != 0 {
		t.Fatalf("second pass attempted %d orders, want 0", again)
	}
	m, err := st.ComputeMetrics(6.0, 25.0)
	if err != nil {
		t.Fatal(err)
	}
	if m.TotalOrders != 8 || m.AutoProcessed != 7 || m.FailedAttempts != 0 {
		t.Fatalf("metrics wrong: %+v", m)
	}
	if m.AutomationRatePct < 87 || m.AutomationRatePct > 88 {
		t.Fatalf("automation rate = %f, want ~87.5", m.AutomationRatePct)
	}
	if m.HoursSaved != 0.7 {
		t.Fatalf("hours saved = %f, want 0.7", m.HoursSaved)
	}
	if len(sent) != 7 {
		t.Fatalf("notifications sent = %d, want 7", len(sent))
	}
}

func TestIdempotentNoDoubleProcessing(t *testing.T) {
	stock := map[string]int{"SKU-A": 100}
	coord, st, _ := newTestCoord(t, stock, 0)
	o := store.Order{ID: "ord-x", CustomerID: "cust-001", Items: []store.OrderItem{{SKU: "SKU-A", Quantity: 1}}}
	_ = st.CreateOrder(&o)
	_, _ = coord.RunOnce(context.Background())
	_, _ = coord.RunOnce(context.Background()) // second pass must be a no-op
	evs, _ := st.EventsForOrder("ord-x")
	detects := 0
	for _, e := range evs {
		if e.Step == "detect" {
			detects++
		}
	}
	if detects != 1 {
		t.Fatalf("order processed %d times, want 1", detects)
	}
}

func sprintf(f string, a ...any) string { return fmtSprintf(f, a...) }
