package metrics_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	tvmetrics "trustvian-processor/internal/metrics"
)

// collect builds instruments over an in-memory reader and returns them
// with a function that gathers what has been recorded. No live backend and
// no exporter — the SDK's manual reader is the supported way to assert on
// instrumentation.
func collect(t *testing.T) (*tvmetrics.Metrics, func() metricdata.ResourceMetrics) {
	t.Helper()

	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	m, err := tvmetrics.New(provider.Meter(tvmetrics.ScopeName))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return m, func() metricdata.ResourceMetrics {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("Collect() error = %v", err)
		}
		return rm
	}
}

// findMetric returns the named metric, failing the test when it is absent.
func findMetric(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	t.Fatalf("metric %q not recorded", name)
	return metricdata.Metrics{}
}

// counterPoints returns a metric's data points as attribute-set → value.
func counterPoints(t *testing.T, m metricdata.Metrics) map[string]int64 {
	t.Helper()

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %q is %T, want an int64 sum", m.Name, m.Data)
	}
	out := make(map[string]int64, len(sum.DataPoints))
	for _, dp := range sum.DataPoints {
		// DefaultEncoder, not nil: a nil encoder renders every attribute
		// set as the empty string, which would silently collapse distinct
		// series into one map entry and make a cardinality test pass
		// vacuously.
		out[dp.Attributes.Encoded(attribute.DefaultEncoder())] = dp.Value
	}
	return out
}

func TestAnalysisCounterAndLatency(t *testing.T) {
	m, gather := collect(t)
	ctx := context.Background()

	m.RecordAnalysis(ctx, tvmetrics.OutcomeAnalyzed, 5*time.Millisecond)
	m.RecordAnalysis(ctx, tvmetrics.OutcomeAnalyzed, 7*time.Millisecond)
	m.RecordAnalysis(ctx, tvmetrics.OutcomeInvalidEvent, 0)
	m.RecordAnalysis(ctx, tvmetrics.OutcomeError, time.Millisecond)

	rm := gather()

	counts := counterPoints(t, findMetric(t, rm, "trustvian.analyses"))
	if len(counts) != 3 {
		t.Errorf("trustvian.analyses has %d series, want 3 (one per outcome): %v", len(counts), counts)
	}
	var total int64
	for _, v := range counts {
		total += v
	}
	if total != 4 {
		t.Errorf("trustvian.analyses total = %d, want 4", total)
	}

	// Latency is recorded only for analyses that produced a result, so two
	// of the four calls above.
	hist, ok := findMetric(t, rm, "trustvian.analysis.duration").Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatal("trustvian.analysis.duration is not a float64 histogram")
	}
	if len(hist.DataPoints) != 1 {
		t.Fatalf("analysis.duration has %d series, want 1 (no attributes)", len(hist.DataPoints))
	}
	if got := hist.DataPoints[0].Count; got != 2 {
		t.Errorf("analysis.duration count = %d, want 2 — only successful analyses record latency", got)
	}
	// Range, not exact timing: the assertion is that a plausible duration
	// was recorded in seconds, not how fast the machine is.
	if sum := hist.DataPoints[0].Sum; sum <= 0 || sum > 1 {
		t.Errorf("analysis.duration sum = %v seconds, want a small positive value", sum)
	}
}

func TestDecisionCounter(t *testing.T) {
	m, gather := collect(t)
	ctx := context.Background()

	for _, d := range []string{"allow", "allow", "block", "observe_only"} {
		m.RecordDecision(ctx, d)
	}

	counts := counterPoints(t, findMetric(t, gather(), "trustvian.decisions"))
	if len(counts) != 3 {
		t.Errorf("trustvian.decisions has %d series, want 3 distinct decisions: %v", len(counts), counts)
	}
}

// TestUnknownDecisionIsFolded is the cardinality guard: a decision value
// this package does not know must not create an unbounded series. Without
// it, a decision type added upstream would silently expand cardinality
// before anyone updated the vocabulary.
func TestUnknownDecisionIsFolded(t *testing.T) {
	m, gather := collect(t)
	ctx := context.Background()

	for _, d := range []string{"quarantine", "escalate", "", "ALLOW", "allow"} {
		m.RecordDecision(ctx, d)
	}

	counts := counterPoints(t, findMetric(t, gather(), "trustvian.decisions"))
	// Four unknown values collapse into "other"; "allow" is its own series.
	if len(counts) != 2 {
		t.Errorf("trustvian.decisions has %d series for 4 unknown + 1 known value, want 2: %v", len(counts), counts)
	}

	var foundOther bool
	for encoded, v := range counts {
		if strings.Contains(encoded, tvmetrics.DecisionOther) {
			foundOther = true
			if v != 4 {
				t.Errorf("%q series = %d, want 4", tvmetrics.DecisionOther, v)
			}
		}
	}
	if !foundOther {
		t.Errorf("unknown decisions were not folded into %q: %v", tvmetrics.DecisionOther, counts)
	}
}

func TestObservationCounterAndLatency(t *testing.T) {
	m, gather := collect(t)
	ctx := context.Background()

	m.RecordObservation(ctx, tvmetrics.OutcomeLearned, 2*time.Millisecond)
	m.RecordObservation(ctx, tvmetrics.OutcomeNotEligible, time.Millisecond)
	m.RecordObservation(ctx, tvmetrics.OutcomeError, 3*time.Millisecond)

	rm := gather()
	if counts := counterPoints(t, findMetric(t, rm, "trustvian.observations")); len(counts) != 3 {
		t.Errorf("trustvian.observations has %d series, want 3: %v", len(counts), counts)
	}

	hist, ok := findMetric(t, rm, "trustvian.observe.duration").Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatal("trustvian.observe.duration is not a float64 histogram")
	}
	// Every outcome records latency, including errors: a failed Observe has
	// still paid the storage round trip, which is what an operator
	// investigating a slow database needs to see.
	if got := hist.DataPoints[0].Count; got != 3 {
		t.Errorf("observe.duration count = %d, want 3 — every outcome records latency", got)
	}
}

// TestNoForbiddenAttributes is the privacy guarantee, checked against
// recorded telemetry rather than against documentation.
//
// It records everything this package can record and then inspects every
// attribute key and value that reached the SDK. A future change that
// started labelling by actor, trace, or raw error would fail here.
func TestNoForbiddenAttributes(t *testing.T) {
	m, gather := collect(t)
	ctx := context.Background()

	// Every value this package can record, so the series count below is
	// the real bound rather than a count of whatever this test happened
	// to exercise.
	for _, o := range []string{tvmetrics.OutcomeAnalyzed, tvmetrics.OutcomeInvalidEvent, tvmetrics.OutcomeError} {
		m.RecordAnalysis(ctx, o, time.Millisecond)
	}
	for _, o := range []string{tvmetrics.OutcomeLearned, tvmetrics.OutcomeNotEligible, tvmetrics.OutcomeError} {
		m.RecordObservation(ctx, o, time.Millisecond)
	}
	for _, d := range []string{
		"allow", "observe_only", "alert", "challenge", "require_approval", "block",
		"definitely-not-a-decision", "", "another-unknown",
	} {
		m.RecordDecision(ctx, d)
	}
	for _, o := range []string{
		tvmetrics.OutcomeApplied, tvmetrics.OutcomeReplayed, tvmetrics.OutcomeError,
	} {
		m.RecordEvaluationIngest(ctx, o, time.Millisecond)
	}

	allowedKeys := map[string]bool{
		"trustvian.outcome":  true,
		"trustvian.decision": true,
	}
	allowedValues := map[string]bool{
		tvmetrics.OutcomeAnalyzed: true, tvmetrics.OutcomeInvalidEvent: true,
		tvmetrics.OutcomeError: true, tvmetrics.OutcomeLearned: true,
		tvmetrics.OutcomeNotEligible: true, tvmetrics.DecisionOther: true,
		"allow": true, "observe_only": true, "alert": true,
		"challenge": true, "require_approval": true, "block": true,
		tvmetrics.OutcomeApplied: true, tvmetrics.OutcomeReplayed: true,
	}

	seriesCount := 0
	for _, sm := range gather().ScopeMetrics {
		for _, metric := range sm.Metrics {
			for _, attrs := range attributeSets(t, metric) {
				seriesCount++
				for _, kv := range attrs {
					key := string(kv.Key)
					if !allowedKeys[key] {
						t.Errorf("metric %q carries forbidden attribute key %q", metric.Name, key)
					}
					if v := kv.Value.Emit(); !allowedValues[v] {
						t.Errorf("metric %q attribute %q has unvocabularized value %q", metric.Name, key, v)
					}
				}
			}
		}
	}

	// Exact, not an upper bound: every vocabulary value was just
	// recorded, so this is the package's full cardinality. A new series
	// — or a lost one — fails here. 15 from analysis, decisions and
	// observations, plus 4 from evaluation ingest (three outcomes and one
	// histogram).
	if seriesCount != 19 {
		t.Errorf("package produces %d time series, want the documented 19", seriesCount)
	}
}

// attributeSets returns every data point's attributes for a metric,
// regardless of instrument type — so the privacy check above covers
// counters and histograms alike without knowing which is which.
func attributeSets(t *testing.T, m metricdata.Metrics) [][]attribute.KeyValue {
	t.Helper()

	var out [][]attribute.KeyValue
	switch data := m.Data.(type) {
	case metricdata.Sum[int64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes.ToSlice())
		}
	case metricdata.Histogram[float64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes.ToSlice())
		}
	default:
		t.Fatalf("metric %q has unexpected type %T — extend this helper", m.Name, m.Data)
	}
	return out
}

// TestNilMetricsRecordsNothing proves the degrade path the processor
// depends on: if instrument construction ever fails, the processor keeps
// running with a nil *Metrics, and every record call must be a safe
// no-op. A panic here would mean a telemetry failure could take down a
// security component.
func TestNilMetricsRecordsNothing(t *testing.T) {
	var m *tvmetrics.Metrics
	ctx := context.Background()

	m.RecordAnalysis(ctx, tvmetrics.OutcomeAnalyzed, time.Millisecond)
	m.RecordDecision(ctx, "block")
	m.RecordObservation(ctx, tvmetrics.OutcomeLearned, time.Millisecond)
}

// TestZeroValueMetricsRecordsNothing covers the same contract for a
// non-nil but unconstructed value, which is what the doc comment on
// Metrics promises.
func TestZeroValueMetricsRecordsNothing(t *testing.T) {
	m := &tvmetrics.Metrics{}
	ctx := context.Background()

	m.RecordAnalysis(ctx, tvmetrics.OutcomeAnalyzed, time.Millisecond)
	m.RecordDecision(ctx, "block")
	m.RecordObservation(ctx, tvmetrics.OutcomeLearned, time.Millisecond)
}

// TestRecordEvaluationIngestUsesAClosedVocabulary proves an unrecognized
// outcome records nothing rather than opening a new series.
func TestRecordEvaluationIngestUsesAClosedVocabulary(t *testing.T) {
	m, gather := collect(t)
	ctx := context.Background()

	m.RecordEvaluationIngest(ctx, tvmetrics.OutcomeApplied, time.Millisecond)
	m.RecordEvaluationIngest(ctx, tvmetrics.OutcomeReplayed, time.Millisecond)
	m.RecordEvaluationIngest(ctx, tvmetrics.OutcomeError, time.Millisecond)
	m.RecordEvaluationIngest(ctx, "queued", time.Millisecond)

	points := counterPoints(t, findMetric(t, gather(), "trustvian.evaluation.records"))
	if len(points) != 3 {
		t.Errorf("trustvian.evaluation.records has %d data points, want 3; "+
			"an unrecognized outcome must not open a series", len(points))
	}
}

// TestEvaluationInstrumentsAreAbsentUntilRecorded proves the new instruments
// cost an unconfigured Collector nothing. Instruments exist; series do not,
// until something records one.
func TestEvaluationInstrumentsAreAbsentUntilRecorded(t *testing.T) {
	_, gather := collect(t)

	for _, sm := range gather().ScopeMetrics {
		for _, metric := range sm.Metrics {
			if strings.HasPrefix(metric.Name, "trustvian.evaluation.") {
				t.Errorf("%s exists with no recording; an unconfigured Collector must emit none",
					metric.Name)
			}
		}
	}
}
