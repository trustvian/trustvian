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
	"unicode/utf8"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/config"
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
	a, err := platform.NewEvaluationAggregate(newTestRun(t))
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	return a
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
	a, err := platform.NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}

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

	recordType := reflect.TypeOf(trustvian.DecisionRecord{})

	// inspect checks one type, descending through structs and through the
	// element type of fixed-size arrays.
	//
	// Arrays are not rejected on principle: [4]uint64 is O(1) and perfectly
	// fine. What must not survive anywhere — at any container depth — is
	// retained evidence, so [4]DecisionRecord is caught by looking at what an
	// array holds rather than at the array itself.
	var inspect func(t *testing.T, typ reflect.Type, path string, depth int)
	inspect = func(t *testing.T, typ reflect.Type, path string, depth int) {
		if depth > 6 {
			t.Fatalf("%s: type nesting deeper than expected", path)
		}
		if typ == recordType {
			t.Errorf("%s retains a DecisionRecord; the aggregate is a summary, not an archive", path)
			return
		}
		if reason, bad := forbidden[typ.Kind()]; bad {
			t.Errorf("%s is a %s: %s", path, typ.Kind(), reason)
			return
		}

		switch typ.Kind() {
		case reflect.Array:
			// Fixed-size, so the array itself is bounded; what it contains
			// still has to be.
			inspect(t, typ.Elem(), path+"[...]", depth+1)
		case reflect.Struct:
			if typ == reflect.TypeOf(time.Time{}) {
				return // an opaque stdlib value, not somewhere records hide
			}
			for i := range typ.NumField() {
				f := typ.Field(i)
				inspect(t, f.Type, path+"."+f.Name, depth+1)
			}
		}
	}
	inspect(t, reflect.TypeOf(platform.EvaluationAggregate{}), "EvaluationAggregate", 0)

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

	got, err := newTestAggregate(t).AddRecord(record)
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
	a, err := platform.NewEvaluationAggregate(run)
	if err != nil {
		b.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
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
			a, _ = platform.NewEvaluationAggregate(run)
		}
	}
}

// ---------------------------------------------------------------------
// Policy-selection consistency
// ---------------------------------------------------------------------

// TestPolicySelectionConsistencyIsEnforced closes a hole where a hand-built
// record could assert "a rule matched" while naming no rule.
//
// MatchedDefault and PolicyRule are one piece of evidence. DecisionRecord
// documents the pairing, and policy.Evaluate produces exactly it: a matched
// rule carries its name, the default carries none. Counting the boolean alone
// would let fabricated selection evidence through, and a later scorecard
// would read it as real.
func TestPolicySelectionConsistencyIsEnforced(t *testing.T) {
	base := add(t, newTestAggregate(t), validRecord())

	malformed := map[string]func(*trustvian.DecisionRecord){
		"matched a rule but names none": func(r *trustvian.DecisionRecord) {
			r.MatchedDefault = false
			r.PolicyRule = ""
		},
		"matched the default but names a rule": func(r *trustvian.DecisionRecord) {
			r.MatchedDefault = true
			r.PolicyRule = "block-prod-shell"
		},
	}
	for name, mutate := range malformed {
		t.Run(name, func(t *testing.T) {
			rec := validRecord()
			rec.EventID = "evt-malformed"
			mutate(&rec)

			got, err := base.AddRecord(rec)
			if !errors.Is(err, platform.ErrInvalidDecisionRecord) {
				t.Fatalf("AddRecord() error = %v, want one wrapping ErrInvalidDecisionRecord", err)
			}
			if got != base {
				t.Errorf("a rejected record changed the aggregate:\n got %+v\nwant %+v", got, base)
			}
		})
	}

	// And the two well-formed combinations still count, in the right bucket.
	t.Run("default with no rule counts as default", func(t *testing.T) {
		rec := validRecord()
		rec.MatchedDefault, rec.PolicyRule = true, ""
		if got := add(t, newTestAggregate(t), rec).PolicySelection(); got != (platform.PolicySelection{MatchedDefault: 1}) {
			t.Fatalf("PolicySelection() = %+v, want MatchedDefault=1", got)
		}
	})
	t.Run("named rule counts as a rule", func(t *testing.T) {
		rec := validRecord()
		rec.MatchedDefault, rec.PolicyRule = false, "block-prod-shell"
		if got := add(t, newTestAggregate(t), rec).PolicySelection(); got != (platform.PolicySelection{MatchedRule: 1}) {
			t.Fatalf("PolicySelection() = %+v, want MatchedRule=1", got)
		}
	})
}

// TestRealEnginePolicySelectionSemantics proves the pairing above is the
// core's actual behavior rather than a reading of its documentation — one
// engine with a matching rule, one falling through to the default, both
// analyzed for real and both accepted by the aggregator.
//
// This is the only test importing `config`, and it does so because
// `config.CompilePolicy` is the single public way to build a rule-bearing
// Policy: `trustvian.WithPolicy` takes an internal type, deliberately (task
// 048). The cost is that the platform's *test* binary pulls in config's own
// dependencies, pgx among them. The non-test build links none of them —
// `go list -deps .` reports zero — so nothing reaches a platform binary, and
// the module's third-party confinement is unaffected.
//
// Worth the cost: an invariant believed from documentation is how three
// earlier false claims in this project got written down. This one is
// observed.
func TestRealEnginePolicySelectionSemantics(t *testing.T) {
	ruleEngine := func(t *testing.T) *trustvian.Engine {
		t.Helper()
		compiled, err := config.CompilePolicy(config.PolicyConfig{
			Version:         config.SchemaVersionV1,
			DefaultDecision: "observe_only",
			DefaultReason:   "no rule matched",
			Rules: []config.PolicyRule{{
				Name:     "flag-tool-use",
				Reason:   "tool calls are reviewed",
				Decision: "alert",
				When:     config.PolicyCondition{OperationCategory: "tool"},
			}},
		})
		if err != nil {
			t.Fatalf("CompilePolicy() error = %v", err)
		}
		return trustvian.NewEngine(trustvian.WithPolicy(compiled))
	}

	analyze := func(t *testing.T, engine *trustvian.Engine, category event.OperationCategory, name string) trustvian.DecisionRecord {
		t.Helper()
		ev := event.Event{
			ID:        "evt-policy",
			Timestamp: aggEpoch,
			Actor:     event.Actor{ID: "agent-1", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9},
			Operation: event.Operation{Category: category, Name: name},
			Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
			Context:   event.Context{Environment: testEnvironment},
		}
		result, err := engine.Analyze(t.Context(), ev)
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		return result.DecisionRecord()
	}

	t.Run("a matched rule names itself", func(t *testing.T) {
		rec := analyze(t, ruleEngine(t), event.OperationCategoryTool, "shell.execute")
		if rec.MatchedDefault {
			t.Fatalf("expected the rule to match: %+v", rec)
		}
		if rec.PolicyRule == "" {
			t.Fatal("a matched rule produced an empty PolicyRule; the aggregator's invariant is wrong")
		}
		if got := add(t, newTestAggregate(t), rec).PolicySelection(); got != (platform.PolicySelection{MatchedRule: 1}) {
			t.Errorf("PolicySelection() = %+v, want MatchedRule=1", got)
		}
	})

	t.Run("the default names nothing", func(t *testing.T) {
		rec := analyze(t, ruleEngine(t), event.OperationCategoryHTTP, "GET /health")
		if !rec.MatchedDefault {
			t.Fatalf("expected the default to apply: %+v", rec)
		}
		if rec.PolicyRule != "" {
			t.Fatalf("the default produced PolicyRule %q; the aggregator's invariant is wrong", rec.PolicyRule)
		}
		if got := add(t, newTestAggregate(t), rec).PolicySelection(); got != (platform.PolicySelection{MatchedDefault: 1}) {
			t.Errorf("PolicySelection() = %+v, want MatchedDefault=1", got)
		}
	})
}

// ---------------------------------------------------------------------
// Aggregate construction
// ---------------------------------------------------------------------

// TestNewEvaluationAggregateRejectsAnInvalidRun: one aggregate is evidence for
// exactly one valid run. A zero-value EvaluationRun is constructible from any
// package — task 052's unexported fields prevent mutation, not
// `platform.EvaluationRun{}` — and copying its accessors would produce an
// aggregate with four empty identifiers, belonging to no run.
func TestNewEvaluationAggregateRejectsAnInvalidRun(t *testing.T) {
	t.Run("zero-value run", func(t *testing.T) {
		got, err := platform.NewEvaluationAggregate(platform.EvaluationRun{})
		if err == nil {
			t.Fatalf("NewEvaluationAggregate(zero run) succeeded and produced %+v", got)
		}
		if !errors.Is(err, platform.ErrInvalidID) {
			t.Errorf("error = %v, want one wrapping ErrInvalidID", err)
		}
		if got != (platform.EvaluationAggregate{}) {
			t.Error("a refused construction returned a non-zero aggregate")
		}
	})

	// Every lifecycle state is acceptable: aggregation happens *during*
	// execution, so pending and running are the common cases, and a terminal
	// run is equally valid evidence.
	t.Run("every lifecycle state is accepted", func(t *testing.T) {
		pending := newTestRun(t)
		running, err := pending.Start(aggEpoch.Add(time.Minute))
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		completed, err := running.Complete(aggEpoch.Add(2 * time.Minute))
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		failed, err := running.Fail(aggEpoch.Add(2*time.Minute), "sandbox died")
		if err != nil {
			t.Fatalf("Fail() error = %v", err)
		}
		cancelled, err := running.Cancel(aggEpoch.Add(2 * time.Minute))
		if err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}

		for name, run := range map[string]platform.EvaluationRun{
			"pending": pending, "running": running,
			"completed": completed, "failed": failed, "cancelled": cancelled,
		} {
			t.Run(name, func(t *testing.T) {
				a, err := platform.NewEvaluationAggregate(run)
				if err != nil {
					t.Fatalf("NewEvaluationAggregate(%s run) error = %v", name, err)
				}
				if a.RunID() != run.ID() {
					t.Errorf("RunID() = %q, want %q", a.RunID(), run.ID())
				}
			})
		}
	})
}

// ---------------------------------------------------------------------
// Bounded diagnostics
// ---------------------------------------------------------------------

// TestValidationErrorsAreBounded closes an amplification path.
//
// DecisionRecord is fixed-*shape*, not size-bounded — task 050 says so
// explicitly, because caller-supplied identifiers have no length limit. An
// error that echoed a field verbatim would let whoever built the record
// choose how much memory the rejection allocates and how much output a log
// absorbs, at exactly the moment the system is already unhappy.
func TestValidationErrorsAreBounded(t *testing.T) {
	const huge = 1 << 20 // 1 MiB
	const maxErrorBytes = 1024

	giant := strings.Repeat("A", huge)

	tests := map[string]func(*trustvian.DecisionRecord){
		"event id":             func(r *trustvian.DecisionRecord) { r.EventID = giant; r.Decision = "nope" },
		"decision":             func(r *trustvian.DecisionRecord) { r.Decision = giant },
		"environment":          func(r *trustvian.DecisionRecord) { r.Environment = giant },
		"risk level":           func(r *trustvian.DecisionRecord) { r.RiskLevel = giant },
		"approval status":      func(r *trustvian.DecisionRecord) { r.ApprovalStatus = event.ApprovalStatus(giant) },
		"policy rule":          func(r *trustvian.DecisionRecord) { r.MatchedDefault = true; r.PolicyRule = giant },
		"behavior environment": func(r *trustvian.DecisionRecord) { r.Behavior.Environment = giant },
	}

	base := add(t, newTestAggregate(t), validRecord())

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			rec := validRecord()
			mutate(&rec)

			got, err := base.AddRecord(rec)
			if err == nil {
				t.Fatal("AddRecord() accepted a record with a 1 MiB field")
			}
			if got != base {
				t.Error("a rejected record changed the aggregate")
			}
			if n := len(err.Error()); n > maxErrorBytes {
				t.Errorf("error is %d bytes for a %d-byte input; diagnostics must stay bounded", n, huge)
			}
			if strings.Contains(err.Error(), strings.Repeat("A", 200)) {
				t.Error("the error reproduced a long run of the untrusted value")
			}
			if !strings.Contains(err.Error(), "truncated") {
				t.Errorf("truncation is not visible in the message: %q", err)
			}
		})
	}
}

// TestPreviewTruncatesOnARuneBoundary: the preview claims to stay valid
// UTF-8, so a multi-byte rune straddling the cut must not be split.
func TestPreviewTruncatesOnARuneBoundary(t *testing.T) {
	// 3 bytes per rune: with a 64-byte budget the cut lands mid-rune at
	// several lengths, so sweep a window around the boundary.
	for runes := 20; runes <= 24; runes++ {
		value := strings.Repeat("→", runes) + strings.Repeat("A", 1<<16)

		rec := validRecord()
		rec.Decision = value
		_, err := newTestAggregate(t).AddRecord(rec)
		if err == nil {
			t.Fatalf("%d runes: AddRecord() accepted an invalid decision", runes)
		}
		if !utf8.ValidString(err.Error()) {
			t.Errorf("%d runes: the error is not valid UTF-8; truncation split a rune", runes)
		}
	}
}

// ---------------------------------------------------------------------
// The constructor cannot be bypassed
// ---------------------------------------------------------------------

// TestZeroValueEvaluationAggregateRejectsRecords is the last invariant bypass
// in this type, and the subtlest.
//
// Task 052's unexported fields stop a caller *mutating* an aggregate. They do
// not stop `platform.EvaluationAggregate{}`, which any package can write. An
// unbound aggregate has an empty environment — and so the environment guard,
// which compares the record's environment against the aggregate's, matches
// happily when the record's is empty too. Every other check passes on a
// perfectly well-formed record. The result was a populated evidence summary
// belonging to no run, no candidate, and no environment.
//
// The record below is deliberately valid in every other respect. If it failed
// for an unrelated reason — a bad decision, a mismatched environment — this
// test would pass while proving nothing.
func TestZeroValueEvaluationAggregateRejectsRecords(t *testing.T) {
	zero := platform.EvaluationAggregate{}

	rec := trustvian.DecisionRecord{
		EventID:   "evt-1",
		Timestamp: aggEpoch,
		// Empty on both sides, so the environment guard is satisfied and
		// cannot be what rejects this record.
		Environment:    "",
		Behavior:       trustvian.StableFeatures{Environment: ""},
		Decision:       "allow",
		RiskLevel:      "low",
		ApprovalStatus: event.ApprovalUnspecified,
		MatchedDefault: true,
		// Every numeric signal at 0, which is inside [0,1].
	}

	// Confirm the premise: this record is acceptable to a real aggregate
	// whose environment is also empty. Otherwise the assertion below could
	// pass for the wrong reason.
	run, err := platform.NewEvaluationRun("run-1", "cand-1", "local", "profile-1", aggEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	bound, err := platform.NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	inEnvironment := rec
	inEnvironment.Environment = "local"
	inEnvironment.Behavior.Environment = "local"
	if _, err := bound.AddRecord(inEnvironment); err != nil {
		t.Fatalf("premise broken: the record is malformed for an unrelated reason: %v", err)
	}

	got, err := zero.AddRecord(rec)
	if !errors.Is(err, platform.ErrUnboundAggregate) {
		t.Fatalf("AddRecord() on a zero-value aggregate: error = %v, want one wrapping ErrUnboundAggregate", err)
	}
	if got != zero {
		t.Errorf("a refused record changed the aggregate:\n got %+v\nwant %+v", got, zero)
	}
	if got.RecordCount() != 0 {
		t.Errorf("RecordCount() = %d, want 0", got.RecordCount())
	}
	if got.RunID() != "" || got.CandidateID() != "" || got.Environment() != "" || got.BehavioralProfile() != "" {
		t.Error("a refused record populated the aggregate's identity")
	}
}

// TestConstructorProducedAggregateStillAccepts is the positive half: the new
// guard must not reject the values the constructor makes.
func TestConstructorProducedAggregateStillAccepts(t *testing.T) {
	a := newTestAggregate(t)

	got, err := a.AddRecord(validRecord())
	if err != nil {
		t.Fatalf("AddRecord() on a constructor-produced aggregate: %v", err)
	}
	if got.RecordCount() != 1 {
		t.Fatalf("RecordCount() = %d, want 1", got.RecordCount())
	}

	// The binding survives folding, so a chain of AddRecord calls keeps
	// working rather than the second one rejecting the first one's output.
	second := validRecord()
	second.EventID = "evt-2"
	got, err = got.AddRecord(second)
	if err != nil {
		t.Fatalf("second AddRecord(): %v", err)
	}
	if got.RecordCount() != 2 {
		t.Errorf("RecordCount() = %d, want 2", got.RecordCount())
	}

	// And a copy of a bound aggregate is still bound — value semantics must
	// carry the marker, not just the identifiers.
	copied := got
	if _, err := copied.AddRecord(validRecord()); err != nil {
		t.Errorf("a copy of a bound aggregate was rejected: %v", err)
	}
}

// TestUnboundAggregateReportsBindingBeforeRecordFaults pins the ordering.
//
// When both the receiver and the record are unusable, the binding fault is
// the one to report: an unbound aggregate cannot accept *any* record, so
// telling the caller their decision string is unrecognized sends them to
// debug the wrong object. The guard therefore runs first, ahead of every
// record-derived check.
func TestUnboundAggregateReportsBindingBeforeRecordFaults(t *testing.T) {
	zero := platform.EvaluationAggregate{}

	// Wrong in several independent ways at once: no event id, no timestamp,
	// an unrecognized decision and risk, a malformed policy pairing, and a
	// non-finite metric. Any of these would be reported by a later check.
	hopeless := trustvian.DecisionRecord{
		Decision:       "banana",
		RiskLevel:      "severe",
		MatchedDefault: false,
		TrustScore:     math.NaN(),
	}

	got, err := zero.AddRecord(hopeless)
	if !errors.Is(err, platform.ErrUnboundAggregate) {
		t.Fatalf("AddRecord() error = %v, want ErrUnboundAggregate reported ahead of the record's own faults", err)
	}
	if errors.Is(err, platform.ErrInvalidDecisionRecord) {
		t.Error("the error reports a record fault; an unbound aggregate cannot accept any record")
	}
	if got != zero {
		t.Error("a refused record changed the aggregate")
	}
}
