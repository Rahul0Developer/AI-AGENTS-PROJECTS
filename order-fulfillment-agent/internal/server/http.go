package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/handler"

	"github.com/stickermule-demo/order-fulfillment-agent/internal/agent"
	"github.com/stickermule-demo/order-fulfillment-agent/internal/store"
)

// Server hosts the GraphQL API plus REST endpoints for schedulers/demos.
type Server struct {
	http   *http.Server
	schema graphql.Schema
	st     *store.Store
	coord  *agent.Coordinator
}

// New wires routes. addr e.g. ":8080" (Cloud Run injects $PORT).
func New(coord *agent.Coordinator, st *store.Store, addr string) (*Server, error) {
	sch, err := BuildSchema(&resolver{coord: coord, st: st})
	if err != nil {
		return nil, fmt.Errorf("build graphql schema: %w", err)
	}
	h := handler.New(&handler.Config{Schema: &sch, GraphiQL: true})

	mux := http.NewServeMux()
	mux.Handle("/graphql", h) // POST queries; GET opens GraphiQL playground
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/orders", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			status := store.OrderStatus(r.URL.Query().Get("status"))
			if status == "" {
				status = store.StatusNew
			}
			list, err := st.ListOrdersByStatus(status, 100)
			writeJSON(w, list, err)
		case http.MethodPost:
			var body struct {
				CustomerID string            `json:"customer_id"`
				Items      []store.OrderItem `json:"items"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			o := store.Order{ID: newID("ord"), CustomerID: body.CustomerID, Items: body.Items}
			err := st.CreateOrder(&o)
			writeJSON(w, o, err)
		default:
			writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		}
	})
	// Cloud Scheduler target: POST /run-cycle triggers one autonomous pass.
	mux.HandleFunc("/run-cycle", func(w http.ResponseWriter, r *http.Request) {
		n, err := coord.RunOnce(context.Background())
		writeJSON(w, map[string]any{"attempted": n}, err)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		mm := envFloat("MANUAL_MINUTES_PER_ORDER", 6.0)
		hc := envFloat("OPS_HOURLY_COST_USD", 25.0)
		m, err := st.ComputeMetrics(mm, hc)
		writeJSON(w, m, err)
	})

	return &Server{
		http:   &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second},
		schema: sch, st: st, coord: coord,
	}, nil
}

// ListenAndServe blocks serving HTTP until shutdown.
func (s *Server) ListenAndServe() error {
	slog.Info("server: listening", "addr", s.http.Addr, "graphql", "/graphql")
	return s.http.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func writeJSON(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
			return f
		}
	}
	return def
}
