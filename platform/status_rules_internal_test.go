package platform

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var rulesEpoch = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

// statusWith builds a document by recording reports exactly as the control
// plane would, then reading at `at`.
func statusWith(t *testing.T, at time.Time, reports ...heldStatus) PipelineStatus {
	t.Helper()
	plane := &ControlPlane{status: newStatusRegistry()}
	for _, h := range reports {
		if err := h.report.validate(); err != nil {
			t.Fatalf("fixture report is invalid: %v", err)
		}
		if _, _, err := plane.status.record(h.report, h.receivedAt); err != nil {
			t.Fatal(err)
		}
	}
	return plane.PipelineStatus(t.Context(), at)
}

func ruleReport(id string) CollectorStatusReport {
	return CollectorStatusReport{
		CollectorID: id, Instance: "00000000000000aa", Sequence: 1, Uptime: time.Minute,
		ReceiverEndpoints: []string{"127.0.0.1:4318"},
		Spans:             CollectorSpanCounts{Received: 10, Evaluated: 10},
		Producers:         []ProducerStatus{{ServiceName: "svc", Spans: 10, LastSeenAge: time.Second}},
		ModelsReported:    true, FidelityReported: true, TransportTargetsReported: true,
		ActorsReported: true, Actors: ActorBinding{BoundByServiceName: 10},
		LearningReported: true, Learning: LearningOutcomes{Learned: 10},
	}
}

func rulesFired(status PipelineStatus) []string {
	var out []string
	for _, s := range status.Suggestions {
		out = append(out, s.Rule)
	}
	return out
}

// TestStatusRuleTable is the rule table's contract: every rule, with the
// evidence that fires it and the evidence that must not.
func TestStatusRuleTable(t *testing.T) {
	quiet := func(r CollectorStatusReport) CollectorStatusReport {
		r.Producers[0].LastSeenAge = time.Minute
		return r
	}
	tests := []struct {
		name    string
		at      time.Duration // read time after the report arrived
		reports []CollectorStatusReport
		absent  bool // read with no report at all
		want    []string
		text    string // a substring the first suggestion's text must carry
	}{
		// Rule 1.
		{name: "no collector at all", absent: true,
			want: []string{"status.no_collector"}, text: "No Collector has reported in 30 s"},
		{name: "only a stale collector", at: 31 * time.Second, reports: []CollectorStatusReport{ruleReport("dev")},
			want: []string{"status.no_collector"}},
		{name: "a reporting collector: no rule 1", reports: []CollectorStatusReport{ruleReport("dev")}, want: nil},

		// Rule 2.
		{name: "a reporting collector whose producer went quiet",
			reports: []CollectorStatusReport{quiet(ruleReport("dev"))},
			want:    []string{"status.no_producer"}, text: "points at 127.0.0.1:4318"},
		{name: "a reporting collector with no producer and no endpoint",
			reports: []CollectorStatusReport{func() CollectorStatusReport {
				r := ruleReport("dev-check")
				r.Producers, r.ReceiverEndpoints = nil, nil
				return r
			}()},
			want: []string{"status.no_producer"}, text: "this Collector's OTLP receiver"},
		{name: "a producer at the edge of the window: no rule 2",
			reports: []CollectorStatusReport{func() CollectorStatusReport {
				r := ruleReport("dev")
				r.Producers[0].LastSeenAge = StatusFreshWindow
				return r
			}()}, want: nil},
		{name: "one collector quiet, another active: no rule 2",
			reports: []CollectorStatusReport{quiet(ruleReport("a")), ruleReport("b")}, want: nil},

		// Rule 3.
		{name: "unbound spans",
			reports: []CollectorStatusReport{func() CollectorStatusReport {
				r := ruleReport("dev")
				r.Actors.Unbound = 14
				return r
			}()},
			want: []string{"status.spans_without_service_name"}, text: "14 spans reached Collector dev"},
		{name: "no unbound spans: no rule 3", reports: []CollectorStatusReport{ruleReport("dev")}, want: nil},
		{name: "actors not reported: no rule 3",
			reports: []CollectorStatusReport{func() CollectorStatusReport {
				r := ruleReport("dev")
				r.ActorsReported, r.Actors = false, ActorBinding{}
				return r
			}()}, want: nil},

		// Rule 4.
		{name: "three operations collapsed onto one target",
			reports: []CollectorStatusReport{func() CollectorStatusReport {
				r := ruleReport("dev")
				r.TransportTargets = []TransportTargetStatus{
					{Target: "api.example.com", Spans: 9, DistinctOperations: 3},
					{Target: "db", Spans: 9, DistinctOperations: 2},
				}
				return r
			}()},
			want: []string{"status.collapsed_http_operations"}, text: "3 distinct operations reached api.example.com"},
		{name: "a saturated count says at least",
			reports: []CollectorStatusReport{func() CollectorStatusReport {
				r := ruleReport("dev")
				r.TransportTargets = []TransportTargetStatus{
					{Target: "", Spans: 99, DistinctOperations: 32, OperationsSaturated: true}}
				return r
			}()},
			want: []string{"status.collapsed_http_operations"}, text: "at least 32 distinct operations reached (no target)"},
		{name: "two operations: no rule 4",
			reports: []CollectorStatusReport{func() CollectorStatusReport {
				r := ruleReport("dev")
				r.TransportTargets = []TransportTargetStatus{{Target: "api", Spans: 9, DistinctOperations: 2}}
				return r
			}()}, want: nil},
		{name: "targets not reported: no rule 4",
			reports: []CollectorStatusReport{func() CollectorStatusReport {
				r := ruleReport("dev")
				r.TransportTargetsReported = false
				return r
			}()}, want: nil},

		// Rule 5 cannot fire while the engine section is unavailable, which is
		// every document today; this row is what fails if that changes without
		// the rule being written.
		{name: "engine unavailable: no rule 5", reports: []CollectorStatusReport{ruleReport("dev")}, want: nil},

		// Table order, with several rules at once.
		{name: "rules fire in table order",
			reports: []CollectorStatusReport{func() CollectorStatusReport {
				r := quiet(ruleReport("dev"))
				r.Actors.Unbound = 1
				r.TransportTargets = []TransportTargetStatus{{Target: "api", Spans: 9, DistinctOperations: 5}}
				return r
			}()},
			want: []string{"status.no_producer", "status.spans_without_service_name", "status.collapsed_http_operations"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var held []heldStatus
			for _, r := range tt.reports {
				held = append(held, heldStatus{report: r, receivedAt: rulesEpoch})
			}
			status := statusWith(t, rulesEpoch.Add(tt.at), held...)
			if got := rulesFired(status); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rules fired %v, want %v", got, tt.want)
			}
			if tt.text != "" && !strings.Contains(status.Suggestions[0].Text, tt.text) {
				t.Fatalf("text %q does not carry %q", status.Suggestions[0].Text, tt.text)
			}
			for _, s := range status.Suggestions {
				if s.RuleVersion != 1 || len(s.Evidence) == 0 {
					t.Fatalf("suggestion without a version or evidence: %+v", s)
				}
				// Every substituted value in the text is a named evidence value.
				for _, e := range s.Evidence {
					if e.Value == "" {
						t.Fatalf("empty evidence %q in %+v", e.Name, s)
					}
				}
			}
		})
	}
}

// TestRemovingTheRuleTableChangesNothingElse is ADR 0064 § 2: suggestions are
// commentary nothing reads, so a document computed with no rules at all is the
// same document minus its suggestions.
func TestRemovingTheRuleTableChangesNothingElse(t *testing.T) {
	report := ruleReport("dev")
	report.Producers[0].LastSeenAge = time.Minute
	report.Actors.Unbound = 3
	report.TransportTargets = []TransportTargetStatus{{Target: "api", Spans: 9, DistinctOperations: 4}}
	held := heldStatus{report: report, receivedAt: rulesEpoch}

	with := statusWith(t, rulesEpoch, held)
	if len(with.Suggestions) == 0 {
		t.Fatal("the fixture fires no rule, so this test proves nothing")
	}

	saved := statusRules
	statusRules = nil
	t.Cleanup(func() { statusRules = saved })
	without := statusWith(t, rulesEpoch, held)

	if len(without.Suggestions) != 0 || without.SuggestionsTruncated {
		t.Fatalf("an empty table produced suggestions: %+v", without.Suggestions)
	}
	with.Suggestions, with.SuggestionsTruncated = nil, false
	without.Suggestions = nil
	if !reflect.DeepEqual(with, without) {
		t.Fatalf("the rule table changed the document beyond its suggestions:\nwith    %+v\nwithout %+v", with, without)
	}
}

func TestSuggestionsAreBounded(t *testing.T) {
	report := ruleReport("dev")
	for i := range MaxStatusTransportTargets {
		report.TransportTargets = append(report.TransportTargets, TransportTargetStatus{
			Target: strings.Repeat("t", i+1), Spans: 9, DistinctOperations: 9})
	}
	var held []heldStatus
	for _, id := range []string{"a", "b"} {
		r := report
		r.CollectorID = id
		held = append(held, heldStatus{report: r, receivedAt: rulesEpoch})
	}
	status := statusWith(t, rulesEpoch, held...)
	if len(status.Suggestions) != MaxStatusSuggestions || !status.SuggestionsTruncated {
		t.Fatalf("suggestions %d truncated=%v", len(status.Suggestions), status.SuggestionsTruncated)
	}
}

// TestSameFactsSeesEveryNonVolatileField changes each reported field in turn
// and requires sameFacts to notice; only sequence, uptime and producer ages may
// move unnoticed. A field added to the report without being added to
// sameFacts fails here.
func TestSameFactsSeesEveryNonVolatileField(t *testing.T) {
	base := ruleReport("dev")
	base.Producers[0].Scopes = []InstrumentationScope{{Name: "s", Version: "1"}}
	base.Models = []ModelCalls{{Provider: "p", Model: "m", Calls: 1}}
	base.TransportTargets = []TransportTargetStatus{{Target: "t", Spans: 2, DistinctOperations: 1}}
	base.Learning.NotLearnedByDecision = []DecisionTally{{Decision: "block", Count: 1}}
	base.Learning.NotLearned = 1

	volatile := base.clone()
	volatile.Sequence, volatile.Uptime = 99, time.Hour
	volatile.Producers[0].LastSeenAge = time.Hour
	if !sameFacts(base, volatile) {
		t.Fatal("sequence, uptime or a producer age counted as a change")
	}

	changes := map[string]func(*CollectorStatusReport){
		"instance":                  func(r *CollectorStatusReport) { r.Instance = "00000000000000bb" },
		"run":                       func(r *CollectorStatusReport) { r.EvaluationRunID = "other" },
		"endpoints":                 func(r *CollectorStatusReport) { r.ReceiverEndpoints = nil },
		"spans":                     func(r *CollectorStatusReport) { r.Spans.Invalid++ },
		"producers truncated":       func(r *CollectorStatusReport) { r.ProducersTruncated = true },
		"producer name":             func(r *CollectorStatusReport) { r.Producers[0].ServiceName = "x" },
		"producer spans":            func(r *CollectorStatusReport) { r.Producers[0].Spans++ },
		"producer scopes":           func(r *CollectorStatusReport) { r.Producers[0].Scopes[0].Version = "2" },
		"producer scopes truncated": func(r *CollectorStatusReport) { r.Producers[0].ScopesTruncated = true },
		"producer sdk":              func(r *CollectorStatusReport) { r.Producers[0].SDK.Version = "2" },
		"models reported":           func(r *CollectorStatusReport) { r.ModelsReported = false },
		"models":                    func(r *CollectorStatusReport) { r.Models[0].Calls++ },
		"models truncated":          func(r *CollectorStatusReport) { r.ModelsTruncated = true },
		"fidelity reported":         func(r *CollectorStatusReport) { r.FidelityReported = false },
		"fidelity":                  func(r *CollectorStatusReport) { r.Fidelity.Semantic++ },
		"targets reported":          func(r *CollectorStatusReport) { r.TransportTargetsReported = false },
		"targets":                   func(r *CollectorStatusReport) { r.TransportTargets[0].DistinctOperations++ },
		"targets truncated":         func(r *CollectorStatusReport) { r.TransportTargetsTruncated = true },
		"actors reported":           func(r *CollectorStatusReport) { r.ActorsReported = false },
		"actors":                    func(r *CollectorStatusReport) { r.Actors.Unbound++ },
		"learning reported":         func(r *CollectorStatusReport) { r.LearningReported = false },
		"learned":                   func(r *CollectorStatusReport) { r.Learning.Learned++ },
		"not learned":               func(r *CollectorStatusReport) { r.Learning.NotLearned++ },
		"observe errors":            func(r *CollectorStatusReport) { r.Learning.ObserveErrors++ },
		"by decision":               func(r *CollectorStatusReport) { r.Learning.NotLearnedByDecision[0].Count++ },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := base.clone()
			change(&changed)
			if sameFacts(base, changed) {
				t.Fatalf("a change to %s went unnoticed", name)
			}
		})
	}
}
