package platform_test

// Task 053: evaluation result aggregation.
//
// Most of these tests build DecisionRecords by hand, because that is the
// cheapest way to drive every category and every malformed-input class. The
// risk of doing only that is assuming a boundary composes when it does not —
// so TestRealEngineRecordAggregates runs a genuine Event through a genuine
// Engine and feeds the result in, using the public API only.
//
// Two properties are asserted structurally rather than behaviorally, because
// behavior cannot notice them being removed: that the aggregate retains no
// record (its O(1) shape), and that it grew no interpretive accessor.

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

const (
	testEnvironment = "staging"
	testProfile     = "profile-reference"
)

var aggEpoch = time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)

// newTestRun builds a run in the environment the aggregate tests use.
func newTestRun(t *testing.T) platform.EvaluationRun {
	t.Helper()
	run, err := platform.NewEvaluationRun("run-1", "cand-1", testEnvironment, testProfile, aggEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	return run
}

func newTestAggregate(t *testing.T) platform.EvaluationAggregate {
	t.Helper()
	return platform.NewEvaluationAggregate(newTestRun(t))
}

// validRecord is a well-formed record of the shape a real Engine produces —
// verified against actual engine output, including that Behavior.Environment
// equals Environment.
func validRecord() trustvian.DecisionRecord {
	return trustvian.DecisionRecord{
		EventID:            "evt-1",
		Timestamp:          aggEpoch,
		ActorID:            "agent-1",
		ActorType:          event.ActorTypeAIAgent,
		IdentityConfidence: 0.9,
		Environment:        testEnvironment,
		Behavior: trustvian.StableFeatures{
			ActorType:         event.ActorTypeAIAgent,
			OperationCategory: event.OperationCategoryTool,
			OperationName:     "shell.execute",
			TargetName:        "build-host",
			TargetCategory:    event.TargetCategoryExternal,
			Environment:       testEnvironment,
		},
		FingerprintID:     "9397e4656b21ff8c",
		AnomalyScore:      0.4,
		AnomalyConfidence: 0.5,
		TrustScore:        0.8,
		RiskLevel:         "low",
		ContextRisk:       0.1,
		Decision:          "observe_only",
		PolicyReason:      "no policy rules configured; observing by default",
		MatchedDefault:    true,
		ApprovalStatus:    event.ApprovalRequired,
	}
}

func add(t *testing.T, a platform.EvaluationAggregate, r trustvian.DecisionRecord) platform.EvaluationAggregate {
	t.Helper()
	got, err := a.AddRecord(r)
	if err != nil {
		t.Fatalf("AddRecord() error = %v", err)
	}
	return got
}

// ---------------------------------------------------------------------
// Construction and empty state
// ---------------------------------------------------------------------

func TestNewEvaluationAggregateCapturesRunIdentity(t *testing.T) {
	run := newTestRun(t)
	a := platform.NewEvaluationAggregate(run)

	if a.RunID() != run.ID() || a.CandidateID() != run.CandidateID() ||
		a.Environment() != run.Environment() || a.BehavioralProfile() != run.BehavioralProfile() {
		t.Fatalf("aggregate did not capture the run's identity: %+v", a)
	}

	if a.RecordCount() != 0 {
		t.Errorf("RecordCount() = %d, want 0", a.RecordCount())
	}
	if !a.FirstObservedAt().IsZero() || !a.LastObservedAt().IsZero() {
		t.Error("an empty aggregate must report the zero time for both bounds")
	}
	if a.Decisions().Total() != 0 || a.Risks().Total() != 0 ||
		a.Approvals().Total() != 0 || a.PolicySelection().Total() != 0 {
		t.Error("an empty aggregate must have zero categorical counts")
	}
	for name, s := range allSummaries(a) {
		if s.Count != 0 {
			t.Errorf("%s.Count = %d on an empty aggregate, want 0", name, s.Count)
		}
		if mean, ok := s.Mean(); ok {
			t.Errorf("%s.Mean() reported %v as present on an empty aggregate; an absent mean is not zero", name, mean)
		}
	}
}

func allSummaries(a platform.EvaluationAggregate) map[string]platform.MetricSummary {
	return map[string]platform.MetricSummary{
		"IdentityConfidence": a.IdentityConfidence(),
		"AnomalyScore":       a.AnomalyScore(),
		"AnomalyConfidence":  a.AnomalyConfidence(),
		"TrustScore":         a.TrustScore(),
		"ContextRisk":        a.ContextRisk(),
	}
}

// ---------------------------------------------------------------------
// One record
// ---------------------------------------------------------------------

func TestAddRecordCountsOneObservation(t *testing.T) {
	got := add(t, newTestAggregate(t), validRecord())

	if got.RecordCount() != 1 {
		t.Fatalf("RecordCount() = %d, want 1", got.RecordCount())
	}
	if !got.FirstObservedAt().Equal(aggEpoch) || !got.LastObservedAt().Equal(aggEpoch) {
		t.Errorf("time range = [%v, %v], want both %v", got.FirstObservedAt(), got.LastObservedAt(), aggEpoch)
	}
	if got.Decisions() != (platform.DecisionCounts{ObserveOnly: 1}) {
		t.Errorf("Decisions() = %+v, want only ObserveOnly=1", got.Decisions())
	}
	if got.Risks() != (platform.RiskCounts{Low: 1}) {
		t.Errorf("Risks() = %+v, want only Low=1", got.Risks())
	}
	if got.Approvals() != (platform.ApprovalCounts{Required: 1}) {
		t.Errorf("Approvals() = %+v, want only Required=1", got.Approvals())
	}
	if got.PolicySelection() != (platform.PolicySelection{MatchedDefault: 1}) {
		t.Errorf("PolicySelection() = %+v, want only MatchedDefault=1", got.PolicySelection())
	}

	// Each summary initializes to the record's own value.
	for name, want := range map[string]float64{
		"IdentityConfidence": 0.9, "AnomalyScore": 0.4, "AnomalyConfidence": 0.5,
		"TrustScore": 0.8, "ContextRisk": 0.1,
	} {
		s := allSummaries(got)[name]
		if s.Count != 1 || s.Sum != want || s.Min != want || s.Max != want {
			t.Errorf("%s = %+v, want Count=1 Sum=Min=Max=%v", name, s, want)
		}
		if mean, ok := s.Mean(); !ok || mean != want {
			t.Errorf("%s.Mean() = (%v, %v), want (%v, true)", name, mean, ok, want)
		}
	}
}

func TestMatchedRuleIsCountedSeparatelyFromDefault(t *testing.T) {
	rec := validRecord()
	rec.MatchedDefault = false
	rec.PolicyRule = "block-prod-shell"

	got := add(t, newTestAggregate(t), rec)
	if got.PolicySelection() != (platform.PolicySelection{MatchedRule: 1}) {
		t.Fatalf("PolicySelection() = %+v, want only MatchedRule=1", got.PolicySelection())
	}
}

// ---------------------------------------------------------------------
// Every category
// ---------------------------------------------------------------------

// TestEveryDecisionCategoryIsCountedDistinctly proves no category aliases
// another: each of the six increments exactly its own field.
func TestEveryDecisionCategoryIsCountedDistinctly(t *testing.T) {
	tests := map[string]platform.DecisionCounts{
		"allow":            {Allow: 1},
		"observe_only":     {ObserveOnly: 1},
		"alert":            {Alert: 1},
		"challenge":        {Challenge: 1},
		"require_approval": {RequireApproval: 1},
		"block":            {Block: 1},
	}
	for decision, want := range tests {
		t.Run(decision, func(t *testing.T) {
			rec := validRecord()
			rec.Decision = decision
			if got := add(t, newTestAggregate(t), rec).Decisions(); got != want {
				t.Fatalf("Decisions() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestEveryRiskLevelIsCountedDistinctly(t *testing.T) {
	tests := map[string]platform.RiskCounts{
		"low":      {Low: 1},
		"medium":   {Medium: 1},
		"high":     {High: 1},
		"critical": {Critical: 1},
	}
	for risk, want := range tests {
		t.Run(risk, func(t *testing.T) {
			rec := validRecord()
			rec.RiskLevel = risk
			if got := add(t, newTestAggregate(t), rec).Risks(); got != want {
				t.Fatalf("Risks() = %+v, want %+v", got, want)
			}
		})
	}
}

// TestEveryApprovalStatusIsCountedDistinctly includes the empty string, which
// is event.ApprovalUnspecified — a producer that said nothing about approval.
// It is a valid state, not malformed input, and must count rather than reject.
func TestEveryApprovalStatusIsCountedDistinctly(t *testing.T) {
	tests := map[event.ApprovalStatus]platform.ApprovalCounts{
		event.ApprovalUnspecified: {Unspecified: 1},
		event.ApprovalNotRequired: {NotRequired: 1},
		event.ApprovalRequired:    {Required: 1},
		event.ApprovalApproved:    {Approved: 1},
		event.ApprovalDenied:      {Denied: 1},
	}
	for status, want := range tests {
		name := string(status)
		if name == "" {
			name = "unspecified (empty)"
		}
		t.Run(name, func(t *testing.T) {
			rec := validRecord()
			rec.ApprovalStatus = status
			if got := add(t, newTestAggregate(t), rec).Approvals(); got != want {
				t.Fatalf("Approvals() = %+v, want %+v", got, want)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Numeric summaries and the time range
// ---------------------------------------------------------------------

func TestMetricSummariesAcrossSeveralRecords(t *testing.T) {
	values := []float64{0, 0.25, 1, 0.5}
	a := newTestAggregate(t)
	for i, v := range values {
		rec := validRecord()
		rec.EventID = "evt-" + string(rune('a'+i))
		rec.IdentityConfidence, rec.AnomalyScore = v, v
		rec.AnomalyConfidence, rec.TrustScore, rec.ContextRisk = v, v, v
		a = add(t, a, rec)
	}

	const wantSum = 1.75
	for name, s := range allSummaries(a) {
		if s.Count != uint64(len(values)) {
			t.Errorf("%s.Count = %d, want %d", name, s.Count, len(values))
		}
		if s.Sum != wantSum {
			t.Errorf("%s.Sum = %v, want %v", name, s.Sum, wantSum)
		}
		if s.Min != 0 || s.Max != 1 {
			t.Errorf("%s Min/Max = %v/%v, want 0/1 — the boundary values must survive", name, s.Min, s.Max)
		}
		mean, ok := s.Mean()
		if !ok || mean != wantSum/float64(len(values)) {
			t.Errorf("%s.Mean() = (%v, %v), want (%v, true)", name, mean, ok, wantSum/float64(len(values)))
		}
	}
}

// TestTimeRangeIsIndependentOfArrivalOrder: the range is the minimum and
// maximum event timestamp, not the first and last record handed over.
func TestTimeRangeIsIndependentOfArrivalOrder(t *testing.T) {
	earliest := aggEpoch.Add(-2 * time.Hour)
	latest := aggEpoch.Add(3 * time.Hour)

	// Deliberately out of order: middle, latest, earliest.
	offsets := []time.Time{aggEpoch, latest, earliest}

	a := newTestAggregate(t)
	for i, ts := range offsets {
		rec := validRecord()
		rec.EventID = "evt-" + string(rune('a'+i))
		rec.Timestamp = ts
		a = add(t, a, rec)
	}

	if !a.FirstObservedAt().Equal(earliest) {
		t.Errorf("FirstObservedAt() = %v, want %v", a.FirstObservedAt(), earliest)
	}
	if !a.LastObservedAt().Equal(latest) {
		t.Errorf("LastObservedAt() = %v, want %v", a.LastObservedAt(), latest)
	}
}

// ---------------------------------------------------------------------
// Value semantics
// ---------------------------------------------------------------------

func TestAddRecordLeavesTheSourceAggregateUnchanged(t *testing.T) {
	before := add(t, newTestAggregate(t), validRecord())
	snapshot := before

	rec := validRecord()
	rec.EventID = "evt-2"
	rec.Decision = "block"
	after, err := before.AddRecord(rec)
	if err != nil {
		t.Fatalf("AddRecord() error = %v", err)
	}

	if before != snapshot {
		t.Fatalf("AddRecord mutated its receiver:\n got %+v\nwant %+v", before, snapshot)
	}
	if after.RecordCount() != 2 || after.Decisions().Block != 1 {
		t.Errorf("the returned aggregate did not advance: %+v", after)
	}
}

// TestDuplicateRecordsCountTwice documents the semantic rather than asserting
// a wish: one AddRecord call is one observation, and no deduplication happens.
// A dedup set would be unbounded, and replay belongs to an ingest boundary
// that can define a retention window. See ADR 0026.
func TestDuplicateRecordsCountTwice(t *testing.T) {
	rec := validRecord()
	a := add(t, add(t, newTestAggregate(t), rec), rec)

	if a.RecordCount() != 2 {
		t.Fatalf("RecordCount() = %d, want 2: the aggregator does not deduplicate", a.RecordCount())
	}
	if a.Decisions().ObserveOnly != 2 {
		t.Errorf("Decisions().ObserveOnly = %d, want 2", a.Decisions().ObserveOnly)
	}
	if s := a.TrustScore(); s.Count != 2 || s.Sum != 1.6 {
		t.Errorf("TrustScore = %+v, want Count=2 Sum=1.6", s)
	}
}

// ---------------------------------------------------------------------
// Rejection
// ---------------------------------------------------------------------

// TestRejectedRecordsLeaveTheAggregateIdentical is the fail-closed contract,
// asserted for every malformed class at once: the returned aggregate must
// equal the original exactly, with no partial count and no advanced range.
func TestRejectedRecordsLeaveTheAggregateIdentical(t *testing.T) {
	numericFields := map[string]func(*trustvian.DecisionRecord, float64){
		"identity_confidence": func(r *trustvian.DecisionRecord, v float64) { r.IdentityConfidence = v },
		"anomaly_score":       func(r *trustvian.DecisionRecord, v float64) { r.AnomalyScore = v },
		"anomaly_confidence":  func(r *trustvian.DecisionRecord, v float64) { r.AnomalyConfidence = v },
		"trust_score":         func(r *trustvian.DecisionRecord, v float64) { r.TrustScore = v },
		"context_risk":        func(r *trustvian.DecisionRecord, v float64) { r.ContextRisk = v },
	}
	badNumbers := map[string]float64{
		"NaN": math.NaN(), "+Inf": math.Inf(1), "-Inf": math.Inf(-1),
		"below zero": -0.0001, "above one": 1.0001,
	}

	tests := map[string]struct {
		mutate  func(*trustvian.DecisionRecord)
		wantErr error
	}{
		"empty event id":     {func(r *trustvian.DecisionRecord) { r.EventID = "" }, platform.ErrInvalidDecisionRecord},
		"zero timestamp":     {func(r *trustvian.DecisionRecord) { r.Timestamp = time.Time{} }, platform.ErrInvalidDecisionRecord},
		"unknown decision":   {func(r *trustvian.DecisionRecord) { r.Decision = "permit" }, platform.ErrInvalidDecisionRecord},
		"decision 'drop'":    {func(r *trustvian.DecisionRecord) { r.Decision = "drop" }, platform.ErrInvalidDecisionRecord},
		"decision 'banana'":  {func(r *trustvian.DecisionRecord) { r.Decision = "banana" }, platform.ErrInvalidDecisionRecord},
		"empty decision":     {func(r *trustvian.DecisionRecord) { r.Decision = "" }, platform.ErrInvalidDecisionRecord},
		"unknown risk":       {func(r *trustvian.DecisionRecord) { r.RiskLevel = "severe" }, platform.ErrInvalidDecisionRecord},
		"empty risk":         {func(r *trustvian.DecisionRecord) { r.RiskLevel = "" }, platform.ErrInvalidDecisionRecord},
		"unknown approval":   {func(r *trustvian.DecisionRecord) { r.ApprovalStatus = "maybe" }, platform.ErrInvalidDecisionRecord},
		"other environment":  {func(r *trustvian.DecisionRecord) { r.Environment = "production" }, platform.ErrEnvironmentMismatch},
		"empty environment":  {func(r *trustvian.DecisionRecord) { r.Environment = "" }, platform.ErrEnvironmentMismatch},
		"behavior env drift": {func(r *trustvian.DecisionRecord) { r.Behavior.Environment = "production" }, platform.ErrInvalidDecisionRecord},
	}
	for field, set := range numericFields {
		for label, value := range badNumbers {
			tests[field+" "+label] = struct {
				mutate  func(*trustvian.DecisionRecord)
				wantErr error
			}{func(r *trustvian.DecisionRecord) { set(r, value) }, platform.ErrInvalidDecisionRecord}
		}
	}

	// Start from a non-empty aggregate: a partial update is far easier to
	// spot against existing counts than against zeroes.
	base := add(t, newTestAggregate(t), validRecord())

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := validRecord()
			rec.EventID = "evt-bad"
			tt.mutate(&rec)

			got, err := base.AddRecord(rec)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("AddRecord() error = %v, want one wrapping %v", err, tt.wantErr)
			}
			if got != base {
				t.Errorf("a rejected record changed the aggregate:\n got %+v\nwant %+v", got, base)
			}
		})
	}
}

// TestRejectionMessagesNameTheField: a sentinel says what kind of thing went
// wrong; the message must say which field and which value, or a caller is
// left reading their own code instead of their data.
func TestRejectionMessagesNameTheField(t *testing.T) {
	tests := map[string]struct {
		mutate func(*trustvian.DecisionRecord)
		want   []string
	}{
		"decision":    {func(r *trustvian.DecisionRecord) { r.Decision = "permit" }, []string{"decision", `"permit"`}},
		"risk":        {func(r *trustvian.DecisionRecord) { r.RiskLevel = "severe" }, []string{"risk level", `"severe"`}},
		"environment": {func(r *trustvian.DecisionRecord) { r.Environment = "production" }, []string{"production", testEnvironment}},
		"numeric":     {func(r *trustvian.DecisionRecord) { r.TrustScore = 4 }, []string{"trust_score", "4"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := validRecord()
			tt.mutate(&rec)
			_, err := newTestAggregate(t).AddRecord(rec)
			if err == nil {
				t.Fatal("AddRecord() succeeded, want an error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------
// Structural guarantees
// ---------------------------------------------------------------------

// TestAggregateRetainsNoRecords proves the O(1) shape from the type itself.
//
// Measuring heap growth over ten records would prove nothing about ten
// million; what makes the bound real is that there is nowhere for a record to
// go. Every field must be a fixed-size value — no slice, no map, no pointer,
// no channel, and no DecisionRecord.
func TestAggregateRetainsNoRecords(t *testing.T) {
	forbidden := map[reflect.Kind]string{
		reflect.Slice:         "a slice grows with the stream",
		reflect.Map:           "a map is keyed by a caller-controlled value and is unbounded",
		reflect.Pointer:       "a pointer can reach something that grows",
		reflect.Chan:          "a channel is not a value",
		reflect.Interface:     "an interface can hold anything, including a record",
		reflect.UnsafePointer: "unsafe",
	}

	var walk func(t *testing.T, typ reflect.Type, path string, depth int)
	walk = func(t *testing.T, typ reflect.Type, path string, depth int) {
		if depth > 4 {
			t.Fatalf("%s: struct nesting deeper than expected", path)
		}
		for i := range typ.NumField() {
			f := typ.Field(i)
			where := path + "." + f.Name

			if reason, bad := forbidden[f.Type.Kind()]; bad {
				t.Errorf("%s is a %s: %s", where, f.Type.Kind(), reason)
				continue
			}
			if f.Type == reflect.TypeOf(trustvian.DecisionRecord{}) {
				t.Errorf("%s retains a DecisionRecord; the aggregate is a summary, not an archive", where)
				continue
			}
			if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(time.Time{}) {
				walk(t, f.Type, where, depth+1)
			}
		}
	}
	walk(t, reflect.TypeOf(platform.EvaluationAggregate{}), "EvaluationAggregate", 0)

	// And the shape is genuinely constant: an aggregate that has seen many
	// records is the same size as an empty one.
	empty := newTestAggregate(t)
	full := empty
	for i := range 500 {
		rec := validRecord()
		rec.EventID = "evt-" + string(rune('a'+i%26))
		rec.Timestamp = aggEpoch.Add(time.Duration(i) * time.Second)
		full = add(t, full, rec)
	}
	if got, want := reflect.TypeOf(full).Size(), reflect.TypeOf(empty).Size(); got != want {
		t.Errorf("aggregate size changed from %d to %d bytes", want, got)
	}
	if full.RecordCount() != 500 {
		t.Errorf("RecordCount() = %d, want 500", full.RecordCount())
	}
}

// TestAggregateExposesNoInterpretation guards the boundary between evidence
// and judgement. Each of these names is a question the aggregate has no
// context to answer: a rule name does not prove severity, "new" behavior needs
// a comparison, and pass/fail needs thresholds that are configuration.
//
// They belong to tasks 054, 055 and 056. If one appears here, it will be the
// field everyone reads instead of the gate.
func TestAggregateExposesNoInterpretation(t *testing.T) {
	forbidden := []string{
		"Passed", "Pass", "Failed", "Promotable", "AllowedToShip", "SafeToShip",
		"Score", "Grade", "Rating",
		"CriticalPolicyViolations", "BlockedSensitiveActions", "UnapprovedSensitiveActions",
		"NewBehaviorCount", "BehavioralDrift", "DelegationStability",
		"ApprovalCompliance", "PolicyComplianceScore", "BehavioralStabilityScore",
		"Diff", "Compare", "NewFingerprints", "RemovedFingerprints",
	}

	typ := reflect.TypeOf(platform.EvaluationAggregate{})
	for i := range typ.NumMethod() {
		name := typ.Method(i).Name
		for _, bad := range forbidden {
			if name == bad {
				t.Errorf("EvaluationAggregate.%s exists; interpretation belongs to the scorecard and gate tasks, not to evidence", name)
			}
		}
	}

	// Score is a prefix worth catching even when spelled differently, but
	// TrustScore and AnomalyScore are legitimate evidence names — so this
	// checks for a bare judgement rather than any occurrence.
	for _, bad := range []string{"Score", "Grade", "Verdict", "Outcome", "Result"} {
		if _, ok := typ.MethodByName(bad); ok {
			t.Errorf("EvaluationAggregate.%s exists and reads as a judgement", bad)
		}
	}
}

// ---------------------------------------------------------------------
// The cross-module proof
// ---------------------------------------------------------------------

// TestRealEngineRecordAggregates is the test that matters most in this file.
//
// Everything above builds records by hand, which is fast and exhaustive and
// assumes the boundary composes. This one does not assume: a real Event goes
// through a real Engine, the public DecisionRecord projection comes out, and
// the platform aggregates it — using nothing but the core's public API.
//
// It is also the first proof of task 050's claim in practice: the platform
// began consuming engine evidence with no core change at all.
func TestRealEngineRecordAggregates(t *testing.T) {
	engine := trustvian.NewEngine(trustvian.WithLearningScope(testProfile))

	ev := event.Event{
		ID:        "evt-real-1",
		Timestamp: aggEpoch,
		Actor:     event.Actor{ID: "agent-deploy", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9},
		Operation: event.Operation{Category: event.OperationCategoryTool, Name: "shell.execute"},
		Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
		Context: event.Context{
			Environment:    testEnvironment,
			SessionID:      "sess-1",
			ApprovalStatus: event.ApprovalRequired,
		},
	}

	result, err := engine.Analyze(t.Context(), ev)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	record := result.DecisionRecord()

	got, err := platform.NewEvaluationAggregate(newTestRun(t)).AddRecord(record)
	if err != nil {
		t.Fatalf("AddRecord() rejected a record a real Engine produced: %v", err)
	}

	if got.RecordCount() != 1 {
		t.Fatalf("RecordCount() = %d, want 1", got.RecordCount())
	}
	if !got.FirstObservedAt().Equal(ev.Timestamp) {
		t.Errorf("FirstObservedAt() = %v, want the event's own timestamp %v", got.FirstObservedAt(), ev.Timestamp)
	}

	// The engine's actual verdict is counted, whatever it was — asserting a
	// specific decision here would couple this test to the default policy
	// rather than to the boundary it exists to prove.
	if got.Decisions().Total() != 1 {
		t.Errorf("Decisions().Total() = %d, want 1", got.Decisions().Total())
	}
	if got.Risks().Total() != 1 {
		t.Errorf("Risks().Total() = %d, want 1", got.Risks().Total())
	}
	if got.Approvals().Required != 1 {
		t.Errorf("Approvals().Required = %d, want 1 — the event's approval evidence", got.Approvals().Required)
	}

	// The numeric evidence round-trips from the engine's own values.
	if s := got.IdentityConfidence(); s.Count != 1 || s.Sum != ev.Actor.IdentityConfidence {
		t.Errorf("IdentityConfidence = %+v, want Sum=%v", s, ev.Actor.IdentityConfidence)
	}
	if s := got.TrustScore(); s.Count != 1 || s.Sum != record.TrustScore {
		t.Errorf("TrustScore = %+v, want Sum=%v", s, record.TrustScore)
	}

	// A never-before-seen fingerprint scores maximally novel with zero
	// confidence — cold start, exactly as the core documents it. Aggregating
	// must not smooth that away.
	if s := got.AnomalyScore(); s.Max != record.AnomalyScore {
		t.Errorf("AnomalyScore.Max = %v, want the record's %v", s.Max, record.AnomalyScore)
	}
}

// TestRealEngineStreamAggregates runs several analyses through one engine and
// aggregates the stream, which is the shape ingest will actually have.
func TestRealEngineStreamAggregates(t *testing.T) {
	engine := trustvian.NewEngine(trustvian.WithLearningScope(testProfile))
	ctx := t.Context()

	a := newTestAggregate(t)
	const observations = 12
	clock := aggEpoch

	for i := range observations {
		clock = clock.Add(90 * time.Second)
		ev := event.Event{
			ID:        "evt-" + string(rune('a'+i)),
			Timestamp: clock,
			Actor:     event.Actor{ID: "agent-deploy", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9},
			Operation: event.Operation{Category: event.OperationCategoryTool, Name: "shell.execute"},
			Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
			Context:   event.Context{Environment: testEnvironment},
		}
		result, err := engine.Analyze(ctx, ev)
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		a = add(t, a, result.DecisionRecord())
	}

	if a.RecordCount() != observations {
		t.Fatalf("RecordCount() = %d, want %d", a.RecordCount(), observations)
	}
	if a.Decisions().Total() != observations || a.Risks().Total() != observations {
		t.Errorf("category totals disagree with the record count: %+v / %+v", a.Decisions(), a.Risks())
	}
	if !a.FirstObservedAt().Before(a.LastObservedAt()) {
		t.Errorf("time range [%v, %v] did not widen across the stream", a.FirstObservedAt(), a.LastObservedAt())
	}

	// As the baseline learns this behavior, anomaly confidence rises from
	// zero — so the aggregate's own evidence should span a range rather than
	// being one repeated number. This is what makes the summary informative.
	if s := a.AnomalyConfidence(); s.Min >= s.Max {
		t.Errorf("AnomalyConfidence Min/Max = %v/%v; expected the stream to span a range", s.Min, s.Max)
	}
}

func BenchmarkEvaluationAggregateAddRecord(b *testing.B) {
	run, err := platform.NewEvaluationRun("run-1", "cand-1", testEnvironment, testProfile, aggEpoch)
	if err != nil {
		b.Fatalf("NewEvaluationRun() error = %v", err)
	}
	a := platform.NewEvaluationAggregate(run)
	record := validRecord()

	b.ReportAllocs()
	for b.Loop() {
		var err error
		a, err = a.AddRecord(record)
		if err != nil {
			b.Fatalf("AddRecord() error = %v", err)
		}
		// The counter is bounded by uint64; a benchmark cannot reach it, but
		// resetting keeps the aggregate honest across very long runs.
		if a.RecordCount() == math.MaxUint64 {
			a = platform.NewEvaluationAggregate(run)
		}
	}
}
