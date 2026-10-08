package platform

// The comparison rule table (task 106, ADR 0064).
//
// Like the status table, a suggestion here is the output of one named rule over
// this comparison's own evidence and nothing else: integer and categorical
// conditions only, nothing fires on evidence that was not reported, and every
// text is a fixed template whose only substitutions are the evidence it names.
// Suggestions sit beside the gate and are read by nothing — no check, no
// verdict, no stored record — which a test proves by removing them.

import "fmt"

// MaxComparisonSuggestions bounds the suggestions one comparison carries.
// Rules 1 and 3 fire per behavior and rule 2 per target, so without it a
// comparison of 512 behaviors could carry 1,000 sentences.
const MaxComparisonSuggestions = 64

// LoadRatioThresholdPermille is rule 2's threshold: a target receiving at
// least twice the reference's calls. Stated, not measured; changing it is a
// rule_version bump.
const LoadRatioThresholdPermille = 2000

type comparisonRule struct {
	id      string
	version int
	fire    func(RepeatedEvaluationComparison) []Suggestion
}

// comparisonRules is the table, in its documented, fixed order — the only
// ordering suggestions have.
var comparisonRules = []comparisonRule{
	{id: "compare.added_in_all_runs", version: 1, fire: ruleAddedInAllRuns},
	{id: "compare.target_load_with_429", version: 1, fire: ruleTargetLoadWith429},
	{id: "compare.lost_in_half", version: 1, fire: ruleLostInHalf},
}

// evaluateComparisonRules runs the table over a finished comparison and returns
// at most MaxComparisonSuggestions, in table order.
func evaluateComparisonRules(c RepeatedEvaluationComparison) ([]Suggestion, bool) {
	out := []Suggestion{}
	for _, rule := range comparisonRules {
		for _, s := range rule.fire(c) {
			if len(out) == MaxComparisonSuggestions {
				return out, true
			}
			s.Rule, s.RuleVersion = rule.id, rule.version
			out = append(out, s)
		}
	}
	return out, false
}

// describeBehavior renders a behavior as "category/operation → target", the
// shape the CLI prints. Producer-supplied names, never content.
func describeBehavior(b RepeatedBehaviorPresence) string {
	text := string(b.Behavior.OperationCategory) + "/" + b.Behavior.OperationName
	if b.Behavior.TargetName != "" {
		text += " → " + b.Behavior.TargetName
	}
	return text
}

// RenderRatio renders an integer permille as a multiple to one decimal place,
// truncated: 2400 → "2.4×", 2000 → "2.0×", 2999 → "2.9×". Computed from the
// integer, so it is the same everywhere it is rendered.
func RenderRatio(permille uint64) string {
	return fmt.Sprintf("%d.%d×", permille/1000, permille%1000/100)
}

// Rule 1 — compare.added_in_all_runs: a behavior in every candidate run and
// no reference run.
func ruleAddedInAllRuns(c RepeatedEvaluationComparison) []Suggestion {
	n := uint64(c.Runs)
	var out []Suggestion
	for _, b := range c.Behaviors {
		if b.CandidateRunsPresent != n || b.ReferenceRunsPresent != 0 {
			continue
		}
		behavior := describeBehavior(b)
		out = append(out, Suggestion{
			Evidence: []SuggestionEvidence{
				{Name: "fingerprint_id", Value: b.FingerprintID},
				{Name: "behavior", Value: behavior},
				{Name: "candidate_runs_present", Value: uint64Text(b.CandidateRunsPresent)},
				{Name: "reference_runs_present", Value: "0"},
				{Name: "runs", Value: uint64Text(n)},
			},
			Text: fmt.Sprintf("%s was added in %d/%d candidate runs. If this was expected, add it to the "+
				"scenario; otherwise investigate before promoting.", behavior, n, n),
		})
	}
	return out
}

// Rule 2 — compare.target_load_with_429: a target receiving at least twice the
// reference's calls, and answering the candidate with 429s. It never fires
// without status evidence: with no status code reported, http_429 is 0.
func ruleTargetLoadWith429(c RepeatedEvaluationComparison) []Suggestion {
	var out []Suggestion
	for _, t := range c.Targets {
		if !t.RatioAvailable || t.CallRatioPermille < LoadRatioThresholdPermille || t.CandidateHTTP429 == 0 {
			continue
		}
		ratio := RenderRatio(t.CallRatioPermille)
		out = append(out, Suggestion{
			Evidence: []SuggestionEvidence{
				{Name: "target_name", Value: t.Target.Name},
				{Name: "target_category", Value: t.Target.Category},
				{Name: "call_ratio_permille", Value: uint64Text(t.CallRatioPermille)},
				{Name: "candidate_http_429", Value: uint64Text(t.CandidateHTTP429)},
				{Name: "threshold_permille", Value: uint64Text(LoadRatioThresholdPermille)},
			},
			Text: fmt.Sprintf("%s received %s the reference's calls and returned %d 429 responses. "+
				"Check its rate limit.", t.Target.Name, ratio, t.CandidateHTTP429),
		})
	}
	return out
}

// Rule 3 — compare.lost_in_half: a behavior in every reference run and missing
// from at least half of the candidate runs.
func ruleLostInHalf(c RepeatedEvaluationComparison) []Suggestion {
	n := uint64(c.Runs)
	var out []Suggestion
	for _, b := range c.Behaviors {
		if n == 0 || b.ReferenceRunsPresent != n || b.CandidateRunsPresent*2 > n {
			continue
		}
		behavior := describeBehavior(b)
		missing := n - b.CandidateRunsPresent
		out = append(out, Suggestion{
			Evidence: []SuggestionEvidence{
				{Name: "fingerprint_id", Value: b.FingerprintID},
				{Name: "behavior", Value: behavior},
				{Name: "reference_runs_present", Value: uint64Text(b.ReferenceRunsPresent)},
				{Name: "candidate_runs_present", Value: uint64Text(b.CandidateRunsPresent)},
				{Name: "runs", Value: uint64Text(n)},
			},
			Text: fmt.Sprintf("%s was in every reference run and is missing from %d of %d candidate runs. "+
				"Task completion may have regressed.", behavior, missing, n),
		})
	}
	return out
}
