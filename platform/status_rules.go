package platform

// The pipeline status rule table (task 105, ADR 0064).
//
// A suggestion is the output of one named rule over the status document's own
// evidence, and nothing else. Every rule:
//
//   - reads only fields already in the document it is evaluated over;
//   - compares integers and closed categorical values, and never combines
//     evidence into a score;
//   - does not fire on evidence that was not reported — absence never
//     satisfies a condition;
//   - names its evidence, and renders a fixed template whose only
//     substitutions are those evidence values.
//
// Suggestions sit beside the evidence in their own field. Nothing reads them:
// no gate, no exit code, no stored record. Removing this table changes
// nothing else in the document, which a test proves.

import (
	"fmt"
	"strconv"
	"time"
)

// MaxStatusSuggestions bounds the suggestions one document carries. Rules 3
// and 4 fire per Collector and per target, so the bound is what keeps a
// document over 16 Collectors and 64 targets each from becoming unbounded.
const MaxStatusSuggestions = 64

// CollapsedOperationsThreshold is rule 4's threshold: this many distinct
// operations reaching one target at transport fidelity is the shape of several
// model or tool calls a convention could have named. Stated, not measured; a
// change to it is a rule_version bump.
const CollapsedOperationsThreshold = 3

// Suggestion is one rule's output.
type Suggestion struct {
	Rule        string
	RuleVersion int
	Evidence    []SuggestionEvidence
	Text        string
}

// SuggestionEvidence is one field a rule read, by name and rendered value.
type SuggestionEvidence struct {
	Name  string
	Value string
}

// statusRule is one row of the table.
type statusRule struct {
	id      string
	version int
	fire    func(PipelineStatus) []Suggestion
}

// statusRules is the table, in its documented, fixed order. The order is the
// only ordering suggestions have: there is no severity and no ranking.
var statusRules = []statusRule{
	{id: "status.no_collector", version: 1, fire: ruleNoCollector},
	{id: "status.no_producer", version: 1, fire: ruleNoProducer},
	{id: "status.spans_without_service_name", version: 1, fire: ruleSpansWithoutServiceName},
	{id: "status.collapsed_http_operations", version: 1, fire: ruleCollapsedHTTPOperations},
	{id: "status.admission_near_bound", version: 1, fire: ruleAdmissionNearBound},
}

// evaluateStatusRules runs the table over a document whose Suggestions are not
// yet set, and returns at most MaxStatusSuggestions in table order.
func evaluateStatusRules(status PipelineStatus) ([]Suggestion, bool) {
	out := []Suggestion{}
	for _, rule := range statusRules {
		for _, s := range rule.fire(status) {
			if len(out) == MaxStatusSuggestions {
				return out, true
			}
			s.Rule, s.RuleVersion = rule.id, rule.version
			out = append(out, s)
		}
	}
	return out, false
}

func freshSeconds() string { return strconv.Itoa(int(StatusFreshWindow / time.Second)) }

// Rule 1 — status.no_collector: no Collector has reported within the fresh
// window.
func ruleNoCollector(status PipelineStatus) []Suggestion {
	for _, c := range status.Collectors {
		if c.State == CollectorReporting {
			return nil
		}
	}
	return []Suggestion{{
		Evidence: []SuggestionEvidence{
			{Name: "collectors_reporting", Value: "0"},
			{Name: "fresh_window_seconds", Value: freshSeconds()},
		},
		Text: fmt.Sprintf("No Collector has reported in %s s. Is 'trustvian dev' running, and is "+
			"trustvian-collector on its path?", freshSeconds()),
	}}
}

// Rule 2 — status.no_producer: at least one Collector is reporting, and none
// of the reporting Collectors has seen a span within the fresh window.
//
// The endpoint named is the first reporting Collector's first receiver
// endpoint, in identifier order. A Collector that reported no endpoint gets
// the same sentence without one.
func ruleNoProducer(status PipelineStatus) []Suggestion {
	var first *CollectorStatus
	for i := range status.Collectors {
		c := &status.Collectors[i]
		if c.State != CollectorReporting {
			continue
		}
		if first == nil {
			first = c
		}
		for _, seen := range c.ProducerLastSeen {
			if status.ReadAt.Sub(seen) <= StatusFreshWindow {
				return nil
			}
		}
	}
	if first == nil {
		return nil // rule 1's case, not this one's
	}
	evidence := []SuggestionEvidence{
		{Name: "collector_id", Value: first.Report.CollectorID},
		{Name: "producers_seen_within_window", Value: "0"},
		{Name: "fresh_window_seconds", Value: freshSeconds()},
	}
	if len(first.Report.ReceiverEndpoints) == 0 {
		return []Suggestion{{
			Evidence: evidence,
			Text: fmt.Sprintf("No producer has sent spans to Collector %s in %s s. Check that the "+
				"producer's OTEL_EXPORTER_OTLP_ENDPOINT points at this Collector's OTLP receiver.",
				first.Report.CollectorID, freshSeconds()),
		}}
	}
	endpoint := first.Report.ReceiverEndpoints[0]
	evidence = append(evidence, SuggestionEvidence{Name: "receiver_endpoint", Value: endpoint})
	return []Suggestion{{
		Evidence: evidence,
		Text: fmt.Sprintf("No producer has sent spans to Collector %s in %s s. Check that the "+
			"producer's OTEL_EXPORTER_OTLP_ENDPOINT points at %s.",
			first.Report.CollectorID, freshSeconds(), endpoint),
	}}
}

// Rule 3 — status.spans_without_service_name: a Collector reports spans that
// carried neither service.name nor trustvian.actor.id. Those spans were never
// evaluated.
func ruleSpansWithoutServiceName(status PipelineStatus) []Suggestion {
	var out []Suggestion
	for _, c := range status.Collectors {
		if !c.Report.ActorsReported || c.Report.Actors.Unbound == 0 {
			continue
		}
		unbound := strconv.FormatUint(c.Report.Actors.Unbound, 10)
		out = append(out, Suggestion{
			Evidence: []SuggestionEvidence{
				{Name: "collector_id", Value: c.Report.CollectorID},
				{Name: "unbound", Value: unbound},
			},
			Text: fmt.Sprintf("%s spans reached Collector %s with no service.name and no "+
				"trustvian.actor.id, and were not evaluated. Set the service.name resource attribute "+
				"(OTEL_SERVICE_NAME).", unbound, c.Report.CollectorID),
		})
	}
	return out
}

// Rule 4 — status.collapsed_http_operations: CollapsedOperationsThreshold or
// more distinct operations reached one named HTTP target, all at transport
// fidelity. The Collector counts only HTTP spans with a target; an unnamed
// target is skipped here as a second guard.
func ruleCollapsedHTTPOperations(status PipelineStatus) []Suggestion {
	var out []Suggestion
	for _, c := range status.Collectors {
		if !c.Report.TransportTargetsReported {
			continue
		}
		for _, t := range c.Report.TransportTargets {
			// Never for an unnamed target, whatever a Collector reports: spans
			// with no target are not HTTP calls to one destination, and naming
			// "(no target)" in a sentence about HTTP would describe nothing.
			if t.Target == "" || t.DistinctOperations < CollapsedOperationsThreshold {
				continue
			}
			target := t.Target
			distinct := strconv.FormatUint(t.DistinctOperations, 10)
			if t.OperationsSaturated {
				distinct = "at least " + distinct
			}
			out = append(out, Suggestion{
				Evidence: []SuggestionEvidence{
					{Name: "collector_id", Value: c.Report.CollectorID},
					{Name: "target", Value: target},
					{Name: "distinct_operations", Value: distinct},
					{Name: "threshold", Value: strconv.Itoa(CollapsedOperationsThreshold)},
				},
				Text: fmt.Sprintf("%s distinct operations reached %s and are visible only as HTTP. Add "+
					"OpenInference or OpenTelemetry GenAI instrumentation to see them as model or tool calls.",
					distinct, target),
			})
		}
	}
	return out
}

// Rule 5 — status.admission_near_bound: an actor's baseline holds 90 % or more
// of the 512-fingerprint admission bound, past which the evaluation stops
// learning new behavior.
//
// It cannot fire today: the engine exposes no admission count, the engine
// section is unavailable, and absent evidence never satisfies a condition. It
// is in the table so the table is complete and the gap is visible; a reviewed
// engine accessor is what would let it fire.
func ruleAdmissionNearBound(PipelineStatus) []Suggestion {
	return nil
}
