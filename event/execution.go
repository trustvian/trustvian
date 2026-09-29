package event

// Operational evidence about one observed action: how long it took, whether it
// failed, and where it sat in its trace.
//
// Task 084. These are properties of the *observation*, not of the behavior:
// none of them reaches StableFeatures, a Fingerprint or a baseline key, and a
// test asserts it. A slow call and a fast one are the same behavior.
//
// # Why these are typed fields rather than attributes
//
// Both telemetry adapters already bridge duration and error onto
// Event.Attributes, because features.Extract reads only that map. That bridge
// is unchanged and still feeds the anomaly signals. It is a poor *evidence*
// carrier though: it is a map with no schema, its duration is a float in
// milliseconds, and DecisionRecord deliberately excludes the attribute map
// entirely (see decision_record.go). So this task adds a typed, bounded path
// beside it rather than widening the map or putting the map on the record —
// which is the boundary ADR 0030 and task 050 both drew.

import "math"

// maxDurationNanos is the largest duration this type will report.
//
// 2^63 - 1 nanoseconds is about 292 years. A span longer than that is a clock
// fault rather than a slow call, so it is reported unobserved instead of
// wrapped — and the bound keeps every downstream sum inside the uint64
// arithmetic the platform's aggregates already have an overflow contract for.
const maxDurationNanos = uint64(math.MaxInt64)

// SpanStatus is what the producer said about whether the operation succeeded.
//
// Four states, because three of them are genuinely different claims and the
// fourth is the absence of any claim:
//
//	""       nothing established a status — an Event not built from a span
//	unset    the producer explicitly expressed no opinion
//	ok       the producer explicitly reported success
//	error    the producer explicitly reported failure
//
// **Neither "" nor unset is success.** OpenTelemetry's status defaults to UNSET
// and most instrumentation never sets OK at all, so treating either as a
// success would let a run of entirely unstatused spans report a zero error
// rate. That is the specific wrong answer this vocabulary exists to prevent, and
// it is why the platform counts the four separately rather than computing a rate.
type SpanStatus string

const (
	StatusUnavailable SpanStatus = ""
	StatusUnset       SpanStatus = "unset"
	StatusOK          SpanStatus = "ok"
	StatusError       SpanStatus = "error"
)

// Valid reports whether s is one of the three stated values.
//
// StatusUnavailable is not valid: it is a legitimate state to hold and render,
// and never a value to accept from a producer over a wire.
func (s SpanStatus) Valid() bool {
	switch s {
	case StatusUnset, StatusOK, StatusError:
		return true
	default:
		return false
	}
}

// SpanLineage says where an observation sat in its trace.
//
//	""       nothing established parentage — an Event not built from a span
//	root     the producer said this span starts its trace
//	child    ParentSpanID names this span's parent
//
// Both supported span formats can establish root versus child: an OTLP span
// carries an all-zero parent span id for a root, and the OpenTelemetry SDK
// returns an invalid parent SpanContext for one. **Neither can express "the
// producer does not know"**, so neither adapter emits LineageUnspecified — it
// exists for an Event that did not come from a span at all. That limit belongs
// to the formats and is documented rather than worked around.
//
// A child whose parent was sampled away, dropped or has not arrived yet is
// still a child. Lineage describes this observation's own claim about itself,
// not whether anything else exists, and nothing in this repository checks that a
// parent was received.
type SpanLineage string

const (
	LineageUnspecified SpanLineage = ""
	LineageRoot        SpanLineage = "root"
	LineageChild       SpanLineage = "child"
)

// Valid reports whether l is one of the two stated values.
func (l SpanLineage) Valid() bool {
	switch l {
	case LineageRoot, LineageChild:
		return true
	default:
		return false
	}
}

// Execution is the operational evidence one observation carries.
//
// A value with no pointers, no slice and no map: it is copied onto a
// DecisionRecord, and a record that could alias anything the caller still holds
// is the aliasing bug decision_record.go already avoids for Contributors.
type Execution struct {
	// DurationNanos is how long the operation took, in nanoseconds.
	//
	// **Meaningful only when DurationObserved is true.** Reading it otherwise
	// yields zero, which is a real measurement for a span that genuinely took no
	// measurable time — the two must not be confused, which is what the separate
	// bool is for.
	DurationNanos uint64

	// DurationObserved says whether DurationNanos is a measurement.
	//
	// False for a span with no end timestamp, no start timestamp, an end before
	// its start, or a span-to-span interval beyond maxDurationNanos. A malformed
	// interval is unobserved rather than clamped to zero: zero is a claim that
	// the operation was instantaneous, which malformed timing does not support.
	DurationObserved bool

	// Status is what the producer said about success. See SpanStatus.
	Status SpanStatus
}

// DurationFrom builds the duration half from a start and end in nanoseconds
// since an arbitrary but common epoch, with zero meaning "unset" for each.
//
// One function, called by both adapters, so the availability rules cannot drift
// into two readings of the same span. The epoch does not matter because only the
// difference is used; what matters is that both adapters agree that zero means
// unset, which each documents at its own call site for its own timestamp type.
func DurationFrom(startNanos, endNanos uint64) (uint64, bool) {
	if startNanos == 0 || endNanos == 0 {
		return 0, false
	}
	if endNanos < startNanos {
		return 0, false
	}
	elapsed := endNanos - startNanos
	if elapsed > maxDurationNanos {
		return 0, false
	}
	return elapsed, true
}
