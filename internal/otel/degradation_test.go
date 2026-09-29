package otel_test

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/trustvian/trustvian/event"
	trustvianotel "github.com/trustvian/trustvian/internal/otel"
)

// Byte-for-byte degradation, asserted against this package's own real spans.
//
// Task 075's hardest compatibility requirement: "a span with no agent-oriented
// attribute produces exactly the behavior it produces today, asserted against the
// current mapping's own fixtures". The word that makes it hard is *exactly* — not
// "equivalently", and not "the fields we thought to check".
//
// The technique: build each span shape this package already tests, map it, and
// compare the whole event.Event against a value captured before the semantic
// layer existed. A field the normalization layer starts populating by accident
// shows up as an inequality here, including a field nobody thought to assert.
//
// These fixtures are deliberately the same shapes as otel_test.go's own cases
// rather than new ones. New fixtures would prove a new mapping is
// self-consistent; these prove the mapping did not move.

// mapped is the comparable projection of an Event.
//
// Everything except Attributes and the timestamps, which are compared
// separately: Attributes is a map (not comparable, and legitimately gains
// duration_ms), and the timestamps are inputs rather than mapping decisions.
type mapped struct {
	ID       string
	Actor    event.Actor
	Category event.OperationCategory
	OpName   string
	Dir      event.OperationDirection
	Target   event.Target
	Context  event.Context
}

func project(e event.Event) mapped {
	return mapped{
		ID:       e.ID,
		Actor:    e.Actor,
		Category: e.Operation.Category,
		OpName:   e.Operation.Name,
		Dir:      e.Operation.Direction,
		Target:   e.Target,
		Context:  e.Context,
	}
}

func TestTransportOnlySpansMapExactlyAsBefore(t *testing.T) {
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	end := start.Add(25 * time.Millisecond)
	res := testResource(t, "checkout-api", "production")

	tests := []struct {
		name  string
		kind  trace.SpanKind
		span  string
		attrs []attribute.KeyValue
		want  mapped
	}{
		{
			name: "HTTP server",
			kind: trace.SpanKindServer,
			span: "POST /orders",
			attrs: []attribute.KeyValue{
				semconv.HTTPRequestMethodKey.String("POST"),
				semconv.ServerAddressKey.String("checkout.internal"),
				semconv.URLPathKey.String("/orders"),
			},
			want: mapped{
				Actor:    event.Actor{ID: "checkout-api", Type: event.ActorTypeService, IdentityConfidence: 1},
				Category: event.OperationCategoryHTTP,
				OpName:   "POST /orders",
				Dir:      event.DirectionInbound,
				Target:   event.Target{Name: "checkout.internal"},
				Context:  event.Context{Environment: "production"},
			},
		},
		{
			name: "DB client",
			kind: trace.SpanKindClient,
			span: "SELECT orders",
			attrs: []attribute.KeyValue{
				semconv.DBSystemNameKey.String("postgresql"),
				semconv.DBNamespaceKey.String("orders"),
			},
			want: mapped{
				Actor:    event.Actor{ID: "checkout-api", Type: event.ActorTypeService, IdentityConfidence: 1},
				Category: event.OperationCategoryDB,
				OpName:   "SELECT orders",
				Dir:      event.DirectionOutbound,
				Target:   event.Target{Name: "orders"},
				Context:  event.Context{Environment: "production"},
			},
		},
		{
			name: "RPC client via peer service",
			kind: trace.SpanKindClient,
			span: "Checkout/Pay",
			attrs: []attribute.KeyValue{
				semconv.ServicePeerNameKey.String("payments"),
			},
			want: mapped{
				Actor:    event.Actor{ID: "checkout-api", Type: event.ActorTypeService, IdentityConfidence: 1},
				Category: event.OperationCategoryRPC,
				OpName:   "Checkout/Pay",
				Dir:      event.DirectionOutbound,
				Target:   event.Target{Name: "payments"},
				Context:  event.Context{Environment: "production"},
			},
		},
		{
			name:  "unclassified falls back to RPC with no target",
			kind:  trace.SpanKindInternal,
			span:  "background-work",
			attrs: nil,
			want: mapped{
				Actor:    event.Actor{ID: "checkout-api", Type: event.ActorTypeService, IdentityConfidence: 1},
				Category: event.OperationCategoryRPC,
				OpName:   "background-work",
				Dir:      event.DirectionUnspecified,
				Context:  event.Context{Environment: "production"},
			},
		},
		{
			name: "explicit trustvian overrides",
			kind: trace.SpanKindClient,
			span: "do-the-thing",
			attrs: []attribute.KeyValue{
				attribute.String(trustvianotel.AttrActorID, "agent-7"),
				attribute.String(trustvianotel.AttrActorType, "ai_agent"),
				attribute.Float64(trustvianotel.AttrIdentityConfidence, 0.5),
				attribute.String(trustvianotel.AttrOperationCategory, "tool"),
			},
			want: mapped{
				Actor:    event.Actor{ID: "agent-7", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.5},
				Category: event.OperationCategoryTool,
				OpName:   "do-the-thing",
				Dir:      event.DirectionOutbound,
				Context:  event.Context{Environment: "production"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := recordSpan(t, res, tt.kind, tt.span, tt.attrs, false, start, end)
			got := project(trustvianotel.EventFromSpan(span))

			// The span/trace ids are generated, so they are filled in rather
			// than predicted — everything else is compared as a whole value.
			sc := span.SpanContext()
			expected := tt.want
			expected.ID = sc.SpanID().String()
			expected.Context.TraceID = sc.TraceID().String()
			expected.Context.SpanID = sc.SpanID().String()
			// Task 084: every fixture in this table is started from a background
			// context, so every one of them is a trace root. Asserted rather than
			// copied from the result, and the same for all cases rather than per
			// case, because a fixture that stopped being a root would be a change
			// to this table that should be noticed.
			//
			// This field appearing is not a degradation regression. Degradation is
			// about what Trustvian *concludes* — category, name, target, actor and
			// therefore the fingerprint — and none of those moved. Correlation is
			// evidence about the observation, and a transport-only span carries it
			// exactly as a semantic one does.
			expected.Context.SpanLineage = event.LineageRoot

			if got != expected {
				t.Errorf("EventFromSpan mapping changed for a transport-only span\n got  %+v\n want %+v", got, expected)
			}
		})
	}
}

// TestTransportOnlySpanCarriesNoFidelityClaim is the other half of degradation.
//
// A span with no convention must not gain a semantic claim — and, once slice 2
// adds the outbound attribute, must be marked transport rather than left
// ambiguous. Asserted on Event.Attributes because that is where the adapter puts
// its derived values; it is not a core field.
func TestTransportOnlySpanCarriesNoFidelityClaim(t *testing.T) {
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	span := recordSpan(t, testResource(t, "checkout-api", "production"),
		trace.SpanKindClient, "POST /orders", []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String("POST"),
			semconv.ServerAddressKey.String("export.localhost"),
		}, false, start, start.Add(time.Millisecond))

	e := trustvianotel.EventFromSpan(span)
	if got, ok := e.Attributes[event.AttrFidelity]; ok && got != string(event.FidelityTransport) {
		t.Errorf("a transport-only span claimed fidelity %q", got)
	}
	if e.Operation.Category == event.OperationCategoryTool {
		t.Error("a transport-only span was categorized as a tool call")
	}
}
