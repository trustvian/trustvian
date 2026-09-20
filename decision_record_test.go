package trustvian_test

// Task 050: DecisionRecord is the public, serializable projection of one
// Analyze outcome — the boundary a consumer persists, streams, or sends over
// an API without importing internal packages.
//
// These tests drive a real Engine rather than hand-building a Result. A
// hand-built fixture would prove the struct copies fields; running the real
// pipeline proves the projection stays correct as the pipeline evolves, which
// is the property that actually matters for a boundary.
//
// The proof that it is usable from *outside* the module is
// examples/configured-engine, which is a separate Go module.

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/config"
	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/anomaly"
)

const sensitiveMarker = "zz-do-not-leak-7f3a91-payload"

// recordEvent is an agent-shaped event carrying every correlation field the
// record projects, plus a deliberately distinctive attribute value used to
// prove raw payload does not cross the boundary.
func recordEvent() event.Event {
	return event.Event{
		ID:        "evt-record-1",
		Timestamp: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		Actor: event.Actor{
			ID:                 "agent-refund",
			Type:               event.ActorTypeAIAgent,
			IdentityConfidence: 0.91,
		},
		Operation: event.Operation{Category: event.OperationCategoryTool, Name: "shell.execute"},
		Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
		Context: event.Context{
			Environment:    "production",
			TraceID:        "trace-abc",
			SpanID:         "span-def",
			SessionID:      "sess-123",
			DelegatedFrom:  "agent-orchestrator",
			ApprovalStatus: event.ApprovalRequired,
		},
		Attributes: map[string]any{
			"tool_arguments": sensitiveMarker,
			"duration_ms":    float64(12),
		},
	}
}

func analyze(t *testing.T, e *trustvian.Engine, ev event.Event) trustvian.Result {
	t.Helper()
	r, err := e.Analyze(context.Background(), ev)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	return r
}

// TestDecisionRecordProjectsEveryField walks the whole record against the
// Result it came from. Every field the boundary promises is checked against
// its source rather than against a literal, so a change to the pipeline that
// alters a value fails here instead of silently shipping.
func TestDecisionRecordProjectsEveryField(t *testing.T) {
	e := trustvian.NewEngine()
	ev := recordEvent()
	r := analyze(t, e, ev)

	rec := r.DecisionRecord()

	checks := []struct {
		name      string
		got, want any
	}{
		{"EventID", rec.EventID, ev.ID},
		{"Timestamp", rec.Timestamp, ev.Timestamp},
		{"ActorID", rec.ActorID, ev.Actor.ID},
		{"ActorType", rec.ActorType, ev.Actor.Type},
		{"IdentityConfidence", rec.IdentityConfidence, ev.Actor.IdentityConfidence},
		{"Environment", rec.Environment, r.BaselineKey.Environment},
		{"FingerprintID", rec.FingerprintID, r.Fingerprint.ID},
		{"AnomalyScore", rec.AnomalyScore, r.Anomaly.Score},
		{"AnomalyConfidence", rec.AnomalyConfidence, r.Anomaly.Confidence},
		{"TrustScore", rec.TrustScore, r.Trust.Score},
		{"RiskLevel", rec.RiskLevel, string(r.Trust.Risk)},
		{"ContextRisk", rec.ContextRisk, r.Trust.ContextRisk},
		{"Decision", rec.Decision, string(r.Decision)},
		{"PolicyRule", rec.PolicyRule, r.Explanation.RuleName},
		{"PolicyReason", rec.PolicyReason, r.Explanation.Reason},
		{"MatchedDefault", rec.MatchedDefault, r.Explanation.MatchedDefault},
		{"TraceID", rec.TraceID, ev.Context.TraceID},
		{"SpanID", rec.SpanID, ev.Context.SpanID},
		{"SessionID", rec.SessionID, ev.Context.SessionID},
		{"DelegatedFrom", rec.DelegatedFrom, ev.Context.DelegatedFrom},
		{"ApprovalStatus", rec.ApprovalStatus, ev.Context.ApprovalStatus},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	// Behavior mirrors the fingerprint's stable dimensions, which is what
	// lets a consumer explain a change without reversing the hash.
	want := trustvian.StableFeatures{
		ActorType:         ev.Actor.Type,
		OperationCategory: ev.Operation.Category,
		OperationName:     ev.Operation.Name,
		TargetName:        ev.Target.Name,
		TargetCategory:    ev.Target.Category,
		Environment:       ev.Context.Environment,
	}
	if rec.Behavior != want {
		t.Errorf("Behavior = %+v, want %+v", rec.Behavior, want)
	}
}

// TestDecisionRecordPreservesContributors: a score nobody can explain is a
// number, not evidence. Order, value, weight and detail all have to survive.
func TestDecisionRecordPreservesContributors(t *testing.T) {
	e := trustvian.NewEngine()
	r := analyze(t, e, recordEvent()) // cold start: maximally novel

	if len(r.Anomaly.Contributors) == 0 {
		t.Fatal("fixture produced no contributors; the test cannot prove preservation")
	}

	rec := r.DecisionRecord()
	if len(rec.Contributors) != len(r.Anomaly.Contributors) {
		t.Fatalf("len(Contributors) = %d, want %d", len(rec.Contributors), len(r.Anomaly.Contributors))
	}
	for i, src := range r.Anomaly.Contributors {
		got := rec.Contributors[i]
		if got.Name != src.Name || got.Value != src.Value || got.Weight != src.Weight || got.Detail != src.Detail {
			t.Errorf("contributor %d = %+v, want {%s %v %v %s}", i, got, src.Name, src.Value, src.Weight, src.Detail)
		}
	}

	// Novel behavior specifically: confidence is the cold-start half of the
	// pair, and dropping it would make a first sighting look like a verdict.
	if rec.AnomalyConfidence != 0 {
		t.Errorf("AnomalyConfidence = %v on first sighting, want 0", rec.AnomalyConfidence)
	}
	if rec.AnomalyScore == 0 {
		t.Error("AnomalyScore = 0 on a never-seen fingerprint, want a novelty reading")
	}
}

// TestDecisionRecordProjectsMatchedRule and its default counterpart cover
// both halves of the explanation contract: a named rule, and the fallback.
func TestDecisionRecordProjectsMatchedRule(t *testing.T) {
	policy, err := config.CompilePolicy(config.PolicyConfig{
		Version:         config.SchemaVersionV1,
		DefaultDecision: "allow",
		DefaultReason:   "nothing matched",
		Rules: []config.PolicyRule{{
			Name:     "block-agent-shell",
			When:     config.PolicyCondition{ActorType: string(event.ActorTypeAIAgent)},
			Decision: "block",
			Reason:   "agents may not run shell commands here",
		}},
	})
	if err != nil {
		t.Fatalf("CompilePolicy() error = %v", err)
	}

	r := analyze(t, trustvian.NewEngine(trustvian.WithPolicy(policy)), recordEvent())
	rec := r.DecisionRecord()

	if rec.Decision != "block" {
		t.Fatalf("Decision = %q, want %q", rec.Decision, "block")
	}
	if rec.PolicyRule != "block-agent-shell" {
		t.Errorf("PolicyRule = %q, want %q", rec.PolicyRule, "block-agent-shell")
	}
	if rec.MatchedDefault {
		t.Error("MatchedDefault = true for a matched rule")
	}
	if rec.PolicyReason != "agents may not run shell commands here" {
		t.Errorf("PolicyReason = %q", rec.PolicyReason)
	}
}

func TestDecisionRecordProjectsDefaultExplanation(t *testing.T) {
	policy, err := config.CompilePolicy(config.PolicyConfig{
		Version:         config.SchemaVersionV1,
		DefaultDecision: "observe_only",
		DefaultReason:   "no rule matched",
	})
	if err != nil {
		t.Fatalf("CompilePolicy() error = %v", err)
	}

	rec := analyze(t, trustvian.NewEngine(trustvian.WithPolicy(policy)), recordEvent()).DecisionRecord()

	if !rec.MatchedDefault {
		t.Error("MatchedDefault = false when the default applied")
	}
	if rec.PolicyRule != "" {
		t.Errorf("PolicyRule = %q, want empty when the default applied", rec.PolicyRule)
	}
	if rec.PolicyReason == "" {
		t.Error("PolicyReason is empty; the explanation must always be present")
	}
}

// TestDecisionRecordDoesNotLeakAttributes is the privacy boundary stated as a
// test. The record carries security evidence, not producer payload — so a
// distinctive attribute value must not appear anywhere in the JSON, including
// inside a field that merely happened to stringify it.
func TestDecisionRecordDoesNotLeakAttributes(t *testing.T) {
	r := analyze(t, trustvian.NewEngine(), recordEvent())

	raw, err := json.Marshal(r.DecisionRecord())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(raw), sensitiveMarker) {
		t.Fatalf("DecisionRecord JSON contains the raw attribute value %q — it is not a payload archive:\n%s", sensitiveMarker, raw)
	}
	if strings.Contains(string(raw), "tool_arguments") {
		t.Fatalf("DecisionRecord JSON contains an attribute key:\n%s", raw)
	}
}

// TestCorrelationFieldsDoNotAffectIdentity: the record exposes session,
// delegation, approval and trace identifiers, and none of them may become
// behavioral identity by virtue of being exposed.
func TestCorrelationFieldsDoNotAffectIdentity(t *testing.T) {
	e := trustvian.NewEngine()

	base := recordEvent()
	other := recordEvent()
	other.ID = "evt-record-2"
	other.Context.TraceID = "trace-zzz"
	other.Context.SpanID = "span-zzz"
	other.Context.SessionID = "sess-999"
	other.Context.DelegatedFrom = "agent-someone-else"
	other.Context.ApprovalStatus = event.ApprovalApproved

	a := analyze(t, e, base).DecisionRecord()
	b := analyze(t, e, other).DecisionRecord()

	if a.FingerprintID != b.FingerprintID {
		t.Fatalf("FingerprintID differs (%s vs %s): correlation fields must not enter behavioral identity", a.FingerprintID, b.FingerprintID)
	}
	if b.SessionID != "sess-999" || b.DelegatedFrom != "agent-someone-else" {
		t.Errorf("correlation fields were not projected: %+v", b)
	}
}

// TestDecisionRecordOwnsItsContributors proves the record is not a window
// onto data someone else still holds.
func TestDecisionRecordOwnsItsContributors(t *testing.T) {
	r := analyze(t, trustvian.NewEngine(), recordEvent())
	if len(r.Anomaly.Contributors) == 0 {
		t.Fatal("fixture produced no contributors")
	}

	rec := r.DecisionRecord()
	before := rec.Contributors[0].Name

	// Mutating the source must not reach the record.
	r.Anomaly.Contributors[0] = anomaly.Signal{Name: "mutated", Value: 9, Weight: 9, Detail: "mutated"}
	if rec.Contributors[0].Name != before {
		t.Fatal("mutating the Result's contributors changed the record: the slice is aliased")
	}

	// And mutating the record must not reach the source.
	rec.Contributors[0].Name = "changed-in-record"
	if r.Anomaly.Contributors[0].Name == "changed-in-record" {
		t.Fatal("mutating the record changed the Result: the slice is aliased")
	}
}

// TestDecisionRecordJSONRoundTrip: what is written is what is read back.
func TestDecisionRecordJSONRoundTrip(t *testing.T) {
	rec := analyze(t, trustvian.NewEngine(), recordEvent()).DecisionRecord()

	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var back trustvian.DecisionRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if !reflect.DeepEqual(back, rec) {
		t.Fatalf("round trip differs:\n got %+v\nwant %+v", back, rec)
	}
}

// TestDecisionRecordIsDeterministic: projecting twice yields the same record.
// A projection that read a clock or generated an identifier would fail here.
func TestDecisionRecordIsDeterministic(t *testing.T) {
	r := analyze(t, trustvian.NewEngine(), recordEvent())

	first, err := json.Marshal(r.DecisionRecord())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	second, err := json.Marshal(r.DecisionRecord())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("projection is not deterministic:\n%s\n%s", first, second)
	}
}

// TestDecisionRecordMarshalsDespitePathologicalContextRisk closes the hole
// that made this boundary conditional. WithContextRisk takes an arbitrary
// caller callback, and nothing between it and Trust validated what came back.
// A NaN reached Trust.ContextRisk and Trust.Score, and encoding/json refuses
// non-finite floats — so a successful Analyze could produce a record the
// public boundary could not serialize.
//
// Analyze still succeeds: a detector that refuses to decide because a
// callback misbehaved is worse than one that assumes the worst. The invalid
// reading resolves to maximum risk instead, which is the only direction that
// cannot turn a caller's bug into a permissive decision.
func TestDecisionRecordMarshalsDespitePathologicalContextRisk(t *testing.T) {
	for name, risk := range map[string]float64{
		"NaN":  math.NaN(),
		"+Inf": math.Inf(1),
		"-Inf": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			e := trustvian.NewEngine(trustvian.WithContextRisk(
				func(trustvian.StableFeatures) float64 { return risk },
			))

			r, err := e.Analyze(context.Background(), recordEvent())
			if err != nil {
				t.Fatalf("Analyze() error = %v; a pathological callback must not fail analysis", err)
			}

			rec := r.DecisionRecord()
			if _, err := json.Marshal(rec); err != nil {
				t.Fatalf("Marshal() error = %v — the record is not serializable, which is the whole point of it", err)
			}

			// Maximum risk, never "no risk": -Inf clamping to 0 would have
			// read an unusable value as safe.
			if rec.ContextRisk != 1 {
				t.Errorf("ContextRisk = %v for %s, want 1", rec.ContextRisk, name)
			}
			if rec.TrustScore != 0 {
				t.Errorf("TrustScore = %v, want 0 under maximum context risk", rec.TrustScore)
			}
		})
	}
}

// TestFiniteContextRiskStillClamps: ordinary out-of-range numbers keep the
// documented clamp behavior. Only non-finite input is treated as invalid.
func TestFiniteContextRiskStillClamps(t *testing.T) {
	for _, tt := range []struct {
		name string
		risk float64
		want float64
	}{
		{"above one", 4, 1},
		{"below zero", -4, 0},
		{"in range", 0.3, 0.3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := trustvian.NewEngine(trustvian.WithContextRisk(
				func(trustvian.StableFeatures) float64 { return tt.risk },
			))
			r := analyze(t, e, recordEvent())
			if got := r.DecisionRecord().ContextRisk; got != tt.want {
				t.Fatalf("ContextRisk = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDecisionRecordCarriesNoPlatformIdentifiers guards the core/platform
// boundary at the one place it is most tempting to cross: a consumer wanting
// its own identifiers on the record. They belong beside records, not on them.
func TestDecisionRecordCarriesNoPlatformIdentifiers(t *testing.T) {
	raw, err := json.Marshal(analyze(t, trustvian.NewEngine(), recordEvent()).DecisionRecord())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, forbidden := range []string{
		"project", "candidate", "evaluation", "scorecard", "promotion", "organization", "tenant",
	} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Errorf("DecisionRecord JSON mentions %q — platform concepts do not belong in the core:\n%s", forbidden, raw)
		}
	}
}
