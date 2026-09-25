// Package server exposes the agent over HTTP: a GraphQL API (typed,
// navigable interface for the agent's capabilities) plus REST endpoints used
// by Cloud Scheduler healthchecks and the demo scripts.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/graphql-go/graphql"

	"github.com/stickermule-demo/order-fulfillment-agent/internal/agent"
	"github.com/stickermule-demo/order-fulfillment-agent/internal/store"
)

type resolver struct {
	coord *agent.Coordinator
	st    *store.Store
}

var itemKind = graphql.NewObject(graphql.ObjectConfig{
	Name: "OrderItem",
	Fields: graphql.Fields{
		"sku":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"quantity": &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
	},
})

var orderKind = graphql.NewObject(graphql.ObjectConfig{
	Name: "Order",
	Fields: graphql.Fields{
		"id":               &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"customerId":       &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"items":            &graphql.Field{Type: graphql.NewList(itemKind)},
		"status":           &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"trackingNumber":   &graphql.Field{Type: graphql.String},
		"fulfillmentNotes": &graphql.Field{Type: graphql.String},
		"errorMsg":         &graphql.Field{Type: graphql.String},
		"createdAt":        &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"updatedAt":        &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"shippedAt":        &graphql.Field{Type: graphql.String},
	},
})

var eventKind = graphql.NewObject(graphql.ObjectConfig{
	Name: "FulfillmentEvent",
	Fields: graphql.Fields{
		"id":        &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"orderId":   &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"step":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"success":   &graphql.Field{Type: graphql.NewNonNull(graphql.Boolean)},
		"detail":    &graphql.Field{Type: graphql.String},
		"timestamp": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
	},
})

var metricsKind = graphql.NewObject(graphql.ObjectConfig{
	Name: "MetricsSummary",
	Fields: graphql.Fields{
		"totalOrders":           &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"autoProcessed":         &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"automationRatePct":     &graphql.Field{Type: graphql.NewNonNull(graphql.Float)},
		"avgProcessingSeconds":  &graphql.Field{Type: graphql.NewNonNull(graphql.Float)},
		"failedAttempts":        &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"errorRatePct":          &graphql.Field{Type: graphql.NewNonNull(graphql.Float)},
		"manualMinutesPerOrder": &graphql.Field{Type: graphql.NewNonNull(graphql.Float)},
		"hoursSaved":            &graphql.Field{Type: graphql.NewNonNull(graphql.Float)},
		"estimatedCostSavedUsd": &graphql.Field{Type: graphql.NewNonNull(graphql.Float)},
	},
})

func toGQL(o store.Order) map[string]any {
	items := []any{}
	for _, it := range o.Items {
		items = append(items, map[string]any{"sku": it.SKU, "quantity": it.Quantity})
	}
	m := map[string]any{
		"id": o.ID, "customerId": o.CustomerID, "items": items,
		"status": string(o.Status), "trackingNumber": o.TrackingNumber,
		"fulfillmentNotes": o.FulfillmentNotes, "errorMsg": o.ErrorMsg,
		"createdAt": o.CreatedAt.Format(time.RFC3339Nano),
		"updatedAt": o.UpdatedAt.Format(time.RFC3339Nano),
	}
	if o.ShippedAt != nil {
		m["shippedAt"] = o.ShippedAt.Format(time.RFC3339Nano)
	}
	return m
}

// BuildSchema wires queries and mutations against the coordinator + store.
// The mutations mirror the agent's own tool functions (generateShippingLabel,
// checkGiveawayEligibility, processPendingOrders, ...) so the agent's
// capabilities are exposed type-safely to the rest of the ecosystem.
func BuildSchema(r *resolver) (graphql.Schema, error) {

	queryType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"order": &graphql.Field{
				Type: orderKind,
				Args: graphql.FieldConfigArgument{"id": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)}},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					o, err := r.st.GetOrder(p.Args["id"].(string))
					if err != nil {
						return nil, err
					}
					return toGQL(o), nil
				},
			},
			"orders": &graphql.Field{
				Type: graphql.NewList(orderKind),
				Args: graphql.FieldConfigArgument{
					"status": &graphql.ArgumentConfig{Type: graphql.String},
					"limit":  &graphql.ArgumentConfig{Type: graphql.Int, DefaultValue: 50},
				},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					st, _ := p.Args["status"].(string)
					if st == "" {
						st = string(store.StatusNew)
					}
					lim, ok := p.Args["limit"].(int)
					if !ok || lim <= 0 {
						lim = 50
					}
					// "all" is a convenience sentinel that skips the status filter.
					if st == "all" {
						allList, allErr := r.st.ListAllOrders(lim)
						if allErr != nil {
							return nil, allErr
						}
						allOut := []any{}
						for _, o := range allList {
							allOut = append(allOut, toGQL(o))
						}
						return allOut, nil
					}
					list, err := r.st.ListOrdersByStatus(store.OrderStatus(st), lim)
					if err != nil {
						return nil, err
					}
					out := []any{}
					for _, o := range list {
						out = append(out, toGQL(o))
					}
					return out, nil
				},
			},
			"orderEvents": &graphql.Field{
				Type: graphql.NewList(eventKind),
				Args: graphql.FieldConfigArgument{"orderId": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)}},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					evs, err := r.st.EventsForOrder(p.Args["orderId"].(string))
					if err != nil {
						return nil, err
					}
					out := []any{}
					for _, e := range evs {
						out = append(out, map[string]any{
							"id": int(e.ID), "orderId": e.OrderID, "step": e.Step,
							"success": e.Success, "detail": e.Detail,
							"timestamp": e.Timestamp.Format(time.RFC3339Nano),
						})
					}
					return out, nil
				},
			},
			"metrics": &graphql.Field{
				Type: metricsKind,
				Args: graphql.FieldConfigArgument{
					"manualMinutesPerOrder": &graphql.ArgumentConfig{Type: graphql.Float, DefaultValue: 6.0},
					"hourlyCostUsd":         &graphql.ArgumentConfig{Type: graphql.Float, DefaultValue: 25.0},
				},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					mm, _ := p.Args["manualMinutesPerOrder"].(float64)
					hc, _ := p.Args["hourlyCostUsd"].(float64)
					m, err := r.st.ComputeMetrics(mm, hc)
					if err != nil {
						return nil, err
					}
					return map[string]any{
						"totalOrders": m.TotalOrders, "autoProcessed": m.AutoProcessed,
						"automationRatePct":    round2(m.AutomationRatePct),
						"avgProcessingSeconds": round2(m.AvgProcessingSeconds),
						"failedAttempts":       m.FailedAttempts, "errorRatePct": round2(m.ErrorRatePct),
						"manualMinutesPerOrder": m.ManualMinutesPerOrder,
						"hoursSaved":            round2(m.HoursSaved), "estimatedCostSavedUsd": round2(m.EstimatedCostSavedUSD),
					}, nil
				},
			},
		},
	})

	mutationType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Mutation",
		Fields: graphql.Fields{
			"createOrder": &graphql.Field{
				Type: orderKind,
				Args: graphql.FieldConfigArgument{
					"customerId": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
					"items":      &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(itemInput)))},
				},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					cid, _ := p.Args["customerId"].(string)
					raw, _ := p.Args["items"].([]any)
					var items []store.OrderItem
					for _, ri := range raw {
						m, _ := ri.(map[string]any)
						sku, _ := m["sku"].(string)
						qty, _ := m["quantity"].(int)
						if sku == "" || qty <= 0 {
							return nil, fmt.Errorf("invalid item: %+v", m)
						}
						items = append(items, store.OrderItem{SKU: sku, Quantity: qty})
					}
					o := store.Order{ID: newID("ord"), CustomerID: cid, Items: items}
					if err := r.st.CreateOrder(&o); err != nil {
						return nil, err
					}
					return toGQL(o), nil
				},
			},
			"processPendingOrders": &graphql.Field{
				Type:        graphql.NewNonNull(graphql.Int),
				Description: "Trigger one autonomous fulfillment cycle; returns orders attempted.",
				Resolve: func(p graphql.ResolveParams) (any, error) {
					return r.coord.RunOnce(context.Background())
				},
			},
			"generateShippingLabel": &graphql.Field{
				Type: graphql.NewObject(graphql.ObjectConfig{Name: "LabelResult", Fields: graphql.Fields{
					"trackingNumber": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
					"carrier":        &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
					"labelUrl":       &graphql.Field{Type: graphql.String},
				}}),
				Args: graphql.FieldConfigArgument{"orderId": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)}},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					id, _ := p.Args["orderId"].(string)
					o, err := r.st.GetOrder(id)
					if err != nil {
						return nil, err
					}
					label, err := r.coord.Tools.Ship.GenerateShippingLabel(context.Background(), o.ID, o.CustomerID)
					if err != nil {
						return nil, err
					}
					return map[string]any{"trackingNumber": label.TrackingNumber, "carrier": label.Carrier, "labelUrl": label.LabelURL}, nil
				},
			},
			"checkGiveawayEligibility": &graphql.Field{
				Type: graphql.NewObject(graphql.ObjectConfig{Name: "GiveawayResult", Fields: graphql.Fields{
					"eligible": &graphql.Field{Type: graphql.NewNonNull(graphql.Boolean)},
					"item":     &graphql.Field{Type: graphql.String},
					"reason":   &graphql.Field{Type: graphql.String},
				}}),
				Args: graphql.FieldConfigArgument{"customerId": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)}},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					res, err := r.coord.Tools.Give.CheckGiveawayEligibility(context.Background(), p.Args["customerId"].(string))
					if err != nil {
						return nil, err
					}
					return map[string]any{"eligible": res.Eligible, "item": res.Item, "reason": res.Reason}, nil
				},
			},
		},
	})

	return graphql.NewSchema(graphql.SchemaConfig{Query: queryType, Mutation: mutationType})
}

var itemInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name: "OrderItemInput",
	Fields: graphql.InputObjectConfigFieldMap{
		"sku":      &graphql.InputObjectFieldConfig{Type: graphql.NewNonNull(graphql.String)},
		"quantity": &graphql.InputObjectFieldConfig{Type: graphql.NewNonNull(graphql.Int)},
	},
})

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func newID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%1e15)
}

var _ = json.Marshal
