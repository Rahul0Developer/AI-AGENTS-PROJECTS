// Command agent runs the Intelligent Order Fulfillment Coordinator.
//
// Modes (subcommands):
//
// serve                 – HTTP server: GraphQL API + GraphiQL playground + REST endpoints;
//
//	also polls for new orders on a ticker (autonomous loop).
//
// run-once              – single fulfillment pass, then exit (pair with Cloud Scheduler).
// seed --orders N       – insert N demo 'new' orders (deterministic SKUs/customers).
// metrics               – print the KPI summary as JSON.
// demo                  – end-to-end: seed -> run-once -> print orders, traces and metrics.
//
// Environment:
//
// DB_DRIVER   sqlite3 (default) | postgres
// DB_DSN      path to sqlite file (default ./data/agent.db) or postgres DSN
//
//	e.g. host=... dbname=orders user=... password=... sslmode=require
//
// PORT        HTTP port for serve (Cloud Run injects this; default 8080)
// POLL_SECONDS autonomous poll interval in serve mode (default 15)
// SHIP_API_URL / GIVE_API_URL / INVENTORY_API_URL / NOTIFY_WEBHOOK_URL
//
//	live internal-platform endpoints; unset => deterministic mocks.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/stickermule-demo/order-fulfillment-agent/internal/agent"
	"github.com/stickermule-demo/order-fulfillment-agent/internal/server"
	"github.com/stickermule-demo/order-fulfillment-agent/internal/store"
	"github.com/stickermule-demo/order-fulfillment-agent/internal/tools"
)

// Demo stock & customers used by seed/demo and the mock tools.
var demoStock = map[string]int{
	"STK-VINYL-4":   500, // 4" vinyl die-cut stickers
	"STK-HOLO-3":    250, // holographic stickers
	"STK-KISS-100":  1000,
	"STK-BOTTLE-20": 0, // deliberately out of stock -> exercises on_hold path
	"STK-LAPTOP-50": 75,
	"MAG-AUTO-10":   40,
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func buildStore() *store.Store {
	driver := env("DB_DRIVER", "sqlite3")
	dsn := env("DB_DSN", "./data/agent.db")
	if driver == "sqlite3" && dsn == "./data/agent.db" {
		_ = os.MkdirAll("./data", 0o755)
	}
	st, err := store.Open(driver, dsn)
	if err != nil {
		slog.Error("cannot open database", "driver", driver, "err", err.Error())
		os.Exit(1)
	}
	return st
}

func buildTools() *agent.Tools {
	return &agent.Tools{
		Inventory: &tools.InventoryClient{BaseURL: env("INVENTORY_API_URL", ""), Mock: tools.NewMockInventory(demoStock)},
		Ship:      &tools.ShipClient{BaseURL: env("SHIP_API_URL", "")},
		Give:      &tools.GiveClient{BaseURL: env("GIVE_API_URL", ""), Mock: &tools.MockGive{EligibleEvery: 3, BonusItem: "free die-cut sample pack"}},
		Notify:    &tools.NotifyClient{WebhookURL: env("NOTIFY_WEBHOOK_URL", ""), Sent: make(chan tools.Notification, 128)},
	}
}

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(logHandler))

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agent <serve|run-once|seed|metrics|demo> [flags]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	_ = fs.Parse(os.Args[2:])

	switch cmd {
	case "serve":
		serve()
	case "run-once":
		runOnceCmd()
	case "seed":
		seed(fs)
	case "metrics":
		metricsCmd()
	case "demo":
		demo()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		os.Exit(2)
	}
}

func serve() {
	st := buildStore()
	coord := agent.New(st, buildTools())
	pollSec := 15
	if v := env("POLL_SECONDS", ""); v != "" {
		fmt.Sscanf(v, "%d", &pollSec)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	go coord.RunLoop(ctx, time.Duration(pollSec)*time.Second)

	addr := ":" + env("PORT", "8080")
	srv, err := server.New(coord, st, addr)
	if err != nil {
		slog.Error("server init failed", "err", err.Error())
		os.Exit(1)
	}
	go func() {
		<-ctx.Done()
		shutCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && !strings.Contains(err.Error(), "Server closed") {
		slog.Error("server error", "err", err.Error())
		os.Exit(1)
	}
}

func runOnceCmd() {
	st := buildStore()
	coord := agent.New(st, buildTools())
	n, err := coord.RunOnce(context.Background())
	printJSON(map[string]any{"attempted": n, "ok": err == nil, "error": fmtErr(err)})
}

func seed(fs *flag.FlagSet) {
	orders := fs.Int("orders", 10, "number of demo orders to create")
	st := buildStore()
	skus := []string{"STK-VINYL-4", "STK-HOLO-3", "STK-KISS-100", "STK-BOTTLE-20", "STK-LAPTOP-50", "MAG-AUTO-10"}
	for i := 1; i <= *orders; i++ {
		o := store.Order{
			ID:         fmt.Sprintf("ord-%04d", i),
			CustomerID: fmt.Sprintf("cust-%03d", i),
			Items: []store.OrderItem{
				{SKU: skus[i%len(skus)], Quantity: (i % 7) + 1},
				{SKU: skus[(i*3)%len(skus)], Quantity: (i % 4) + 1},
			},
		}
		if err := st.CreateOrder(&o); err != nil {
			slog.Warn("seed: skip order", "id", o.ID, "err", err.Error())
		}
	}
	printJSON(map[string]any{"seeded": *orders})
}

func metricsCmd() {
	st := buildStore()
	mm := 6.0
	hc := 25.0
	if v := env("MANUAL_MINUTES_PER_ORDER", ""); v != "" {
		fmt.Sscanf(v, "%f", &mm)
	}
	if v := env("OPS_HOURLY_COST_USD", ""); v != "" {
		fmt.Sscanf(v, "%f", &hc)
	}
	m, err := st.ComputeMetrics(mm, hc)
	if err != nil {
		slog.Error("metrics", "err", err.Error())
		os.Exit(1)
	}
	printJSON(m)
}

func demo() {
	st := buildStore()
	coord := agent.New(st, buildTools())
	slog.Info("=== DEMO: seeding 12 orders ===")
	skus := []string{"STK-VINYL-4", "STK-HOLO-3", "STK-KISS-100", "STK-BOTTLE-20", "STK-LAPTOP-50", "MAG-AUTO-10"}
	for i := 1; i <= 12; i++ {
		o := store.Order{
			ID:         fmt.Sprintf("ord-%04d", i),
			CustomerID: fmt.Sprintf("cust-%03d", i),
			Items:      []store.OrderItem{{SKU: skus[i%len(skus)], Quantity: (i % 7) + 1}},
		}
		if err := st.CreateOrder(&o); err != nil {
			continue // duplicate id from earlier demos - ignore
		}
	}
	slog.Info("=== DEMO: running autonomous fulfillment cycle ===")
	n, _ := coord.RunOnce(context.Background())
	fmt.Printf("\nAttempted %d orders. Final state:\n\n", n)

	for _, status := range []store.OrderStatus{store.StatusShipped, store.StatusOnHold, store.StatusFailed, store.StatusNew} {
		list, _ := st.ListOrdersByStatus(status, 100)
		fmt.Printf("%-11s : %d\n", status, len(list))
	}
	one, err := st.GetOrder("ord-0001")
	if err == nil {
		evs, _ := st.EventsForOrder(one.ID)
		fmt.Printf("\nTrace for %s (status=%s tracking=%s):\n", one.ID, one.Status, one.TrackingNumber)
		for _, e := range evs {
			ok := "✓"
			if !e.Success {
				ok = "✗"
			}
			fmt.Printf("  %s %-16s %s\n", ok, e.Step, e.Detail)
		}
	}
	m, _ := st.ComputeMetrics(6.0, 25.0)
	fmt.Println("\nKPI summary:")
	b, _ := json.MarshalIndent(m, "  ", "  ")
	fmt.Println("  " + string(b))
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
