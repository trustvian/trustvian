// Command alert-webhook demonstrates the v0.4 Alert & Notification
// Foundation end to end, entirely through the direct Go SDK — no OTel
// adapter and no Collector processor involved anywhere in this program,
// proving the alert package's independence from both (see
// docs/archive/tasks/v0.4/018-alert-notification-foundation.md's
// Acceptance Criteria).
//
// It runs a cold-start (never-before-seen) event through the normal
// Engine pipeline, evaluates one Alert rule against the resulting
// Result, and delivers the matching Alert to a signed HTTPS webhook.
//
// The event's Decision is OBSERVE_ONLY, not BLOCK — this program uses
// trustvian.NewEngine() with no custom Policy (an external module can
// compile one with config.CompilePolicy; this one keeps the default on
// purpose, see the examples index's "A note on Decision"); OBSERVE_ONLY
// is the only Decision a default, rule-less Policy ever produces. The Alert rule below
// deliberately matches on anomaly score rather than Decision, which is
// the point this example exists to make concrete: Decision and Alert
// are different questions (spec § 18.1). A routine OBSERVE_ONLY
// decision on a highly novel, never-before-seen action can still be
// worth an operator's attention — Alert Evaluation is free to say so
// even though Decision said "let it through."
//
// The "external system" receiving the webhook is a local httptest
// server started by this program itself, so the example needs no
// network access or setup — the same "no example requires network
// access" invariant every other program under examples/ already
// follows (see ../README.md) — while still exercising a real HTTP POST
// with a real HMAC signature.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/alert"
	"github.com/trustvian/trustvian/event"
)

const webhookSecret = "example-signing-secret"

func main() {
	receiver := startReceiver()
	defer receiver.Close()

	engine := trustvian.NewEngine()

	ev := event.Event{
		ID:        "evt-001",
		Timestamp: time.Now(),
		Actor: event.Actor{
			ID:                 "svc-payment",
			Type:               event.ActorTypeService,
			IdentityConfidence: 0.9,
		},
		Operation: event.Operation{
			Category: event.OperationCategoryExternal,
			Name:     "GET /secrets/db-password",
		},
		Target: event.Target{Name: "secrets-manager", Category: event.TargetCategoryExternal},
	}

	result, err := engine.Analyze(context.Background(), ev)
	if err != nil {
		log.Fatalf("analyze: %v", err)
	}
	fmt.Println(result.Explain())

	// One Alert rule: a highly novel action is HIGH severity, regardless
	// of what Decision it produced. This is the operator's own rule, not
	// a built-in mapping — severity is never inferred from
	// Decision/Risk/scores automatically (see alert.Severity's doc
	// comment).
	rules := []alert.Rule{
		{Name: "novel-action-is-high", When: alert.Condition{MinAnomalyScore: 0.8}, Severity: alert.SeverityHigh},
	}

	a, matched := alert.Evaluate(result, rules)
	if !matched {
		fmt.Println("No alert rule matched this Result; nothing delivered.")
		return
	}
	fmt.Printf("Alert matched: severity=%s decision=%s reasons=%v\n", a.Severity, a.Decision, a.Reasons)

	sink, err := alert.NewWebhookSink(receiver.URL, webhookSecret,
		alert.WithHTTPClient(receiver.Client()),
		alert.WithAllowLoopback(), // this example's receiver is a local httptest server
	)
	if err != nil {
		log.Fatalf("new webhook sink: %v", err)
	}

	if err := sink.Send(context.Background(), a); err != nil {
		log.Fatalf("send: %v", err)
	}
	fmt.Println("Webhook delivered and signature verified by the receiver.")
}

// startReceiver is a stand-in for a real external system (n8n, a SIEM, a
// ticketing system) — it independently recomputes the HMAC signature
// exactly as a real receiver would, and prints the delivered payload.
func startReceiver() *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		ts := r.Header.Get("X-Trustvian-Timestamp")
		mac := hmac.New(sha256.New, []byte(webhookSecret))
		mac.Write([]byte(ts))
		mac.Write([]byte("."))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))

		got := r.Header.Get("X-Trustvian-Signature")
		if !hmac.Equal([]byte(got), []byte(want)) {
			http.Error(w, "signature mismatch", http.StatusUnauthorized)
			return
		}
		if age, err := strconv.ParseInt(ts, 10, 64); err == nil {
			_ = age // a real receiver would enforce a replay window here
		}

		var env alert.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			http.Error(w, "malformed payload", http.StatusBadRequest)
			return
		}
		fmt.Printf("Receiver: signature verified, payload version=%s alert.id=%s\n", env.Version, env.Alert.ID)
		w.WriteHeader(http.StatusOK)
	}))
}
