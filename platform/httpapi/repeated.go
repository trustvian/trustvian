package httpapi

// POST /v1/evaluations/compare-repeated — task 078's repeated evaluation.
//
// The handler translates and nothing else (ADR 0031 § 2): it decodes, calls
// ControlPlane.CompareRepeatedEvaluations once, and renders what came back. It
// counts no presence, classifies nothing and evaluates no check.
// POST /v1/evaluations/compare is untouched.

import (
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"

	platform "trustvian-platform"
)

// repeatedLimitsDTO carries the five required thresholds as canonical decimal
// strings, like every other uint64 crossing /v1. Pointers, so an omitted field
// is distinguishable from "0": there is no default k, j or maximum.
type repeatedLimitsDTO struct {
	AddedCandidatePresenceMinimum     *string `json:"added_candidate_presence_minimum"`
	AddedReferencePresenceMaximum     *string `json:"added_reference_presence_maximum"`
	MaxRepeatedAddedBehaviors         *string `json:"max_repeated_added_behaviors"`
	MaxBlockDecisionsPerRun           *string `json:"max_block_decisions_per_run"`
	MaxCriticalRiskObservationsPerRun *string `json:"max_critical_risk_observations_per_run"`

	// Task 106's optional limits. Omitted means not evaluated; there is no
	// default. max_calls_per_run, when present, lists 1..16 targets.
	MinCandidateFrequency *string               `json:"min_candidate_frequency,omitempty"`
	MaxLostBehaviors      *string               `json:"max_lost_behaviors,omitempty"`
	MaxCallsPerRun        *[]targetCallLimitDTO `json:"max_calls_per_run,omitempty"`
}

// targetCallLimitDTO is one max_calls_per_run entry.
type targetCallLimitDTO struct {
	Target string `json:"target"`
	Max    string `json:"max"`
}

type compareRepeatedRequest struct {
	ReferenceRunIDs []string          `json:"reference_run_ids"`
	CandidateRunIDs []string          `json:"candidate_run_ids"`
	GateLimits      repeatedLimitsDTO `json:"gate_limits"`
}

// repeatedLimitsResponseDTO echoes the limits the result was evaluated under:
// a reader cannot check a k/N count against a rule the document does not carry.
type repeatedLimitsResponseDTO struct {
	AddedCandidatePresenceMinimum     string `json:"added_candidate_presence_minimum"`
	AddedReferencePresenceMaximum     string `json:"added_reference_presence_maximum"`
	MaxRepeatedAddedBehaviors         string `json:"max_repeated_added_behaviors"`
	MaxBlockDecisionsPerRun           string `json:"max_block_decisions_per_run"`
	MaxCriticalRiskObservationsPerRun string `json:"max_critical_risk_observations_per_run"`

	// Task 106's optional limits, echoed only when supplied: an omitted limit
	// leaves this object exactly as it was before 106.
	MinCandidateFrequency *string              `json:"min_candidate_frequency,omitempty"`
	MaxLostBehaviors      *string              `json:"max_lost_behaviors,omitempty"`
	MaxCallsPerRun        []targetCallLimitDTO `json:"max_calls_per_run,omitempty"`
}

// repetitionDTO is one repetition as the control plane found it, including its
// engine evidence, so the repetition that blocked can be found.
type repetitionDTO struct {
	Side                     string `json:"side"`
	Index                    int    `json:"index"`
	RunID                    string `json:"run_id"`
	Status                   string `json:"status"`
	BehavioralProfile        string `json:"behavioral_profile"`
	RecordCount              string `json:"record_count"`
	DistinctBehaviorCount    int    `json:"distinct_behavior_count"`
	BlockDecisions           string `json:"block_decisions"`
	CriticalRiskObservations string `json:"critical_risk_observations"`
}

// repeatedBehaviorDTO is one behavior across the repetitions. Every observed
// behavior appears, not only the classified ones. The descriptor is
// producer-supplied text: a consumer rendering it treats it as untrusted.
type repeatedBehaviorDTO struct {
	FingerprintID        string                `json:"fingerprint_id"`
	Behavior             behaviorDescriptorDTO `json:"behavior"`
	ReferenceRunsPresent string                `json:"reference_runs_present"`
	CandidateRunsPresent string                `json:"candidate_runs_present"`
	Classification       string                `json:"classification"`

	// Task 106. Reference and candidate frequency over each side's completed
	// repetitions, and the lost classification as its own field: 078's
	// classification is a closed vocabulary its consumers do not extend.
	ReferenceFrequency frequencyDTO `json:"reference_frequency"`
	CandidateFrequency frequencyDTO `json:"candidate_frequency"`
	Lost               bool         `json:"lost"`
}

// frequencyDTO is how often one behavior or target was called on one side.
// With no completed repetition there are no figures: runs is "0" and the
// per-run fields are absent rather than zero.
type frequencyDTO struct {
	Runs                 string  `json:"runs"`
	CallsTotal           string  `json:"calls_total"`
	CallsPerRunMin       *string `json:"calls_per_run_min,omitempty"`
	CallsPerRunMax       *string `json:"calls_per_run_max,omitempty"`
	CallsPerRunMeanMilli *string `json:"calls_per_run_mean_milli,omitempty"`
}

func newFrequencyDTO(s platform.FrequencyStats) frequencyDTO {
	dto := frequencyDTO{Runs: u64(s.Runs), CallsTotal: u64(s.CallsTotal)}
	if s.Available() {
		low, high, mean := u64(s.CallsPerRunMin), u64(s.CallsPerRunMax), u64(s.CallsPerRunMeanMilli)
		dto.CallsPerRunMin, dto.CallsPerRunMax, dto.CallsPerRunMeanMilli = &low, &high, &mean
	}
	return dto
}

// targetFrequencyDTO is one target's frequency. call_ratio_permille is
// present only when the reference called the target at all; at a reference
// total of zero there is no ratio, and call_ratio_available says so.
type targetFrequencyDTO struct {
	TargetName         string       `json:"target_name"`
	TargetCategory     string       `json:"target_category"`
	ReferenceFrequency frequencyDTO `json:"reference_frequency"`
	CandidateFrequency frequencyDTO `json:"candidate_frequency"`
	CallRatioAvailable bool         `json:"call_ratio_available"`
	CallRatioPermille  *string      `json:"call_ratio_permille,omitempty"`
}

type repeatedCheckDTO struct {
	Name   string `json:"name"`
	Actual string `json:"actual"`
	Rule   string `json:"rule"`
	Bound  string `json:"bound"`
	Passed bool   `json:"passed"`
	// Advisory is "fresh_scope" on checks 5 and 6 at N > 1, and absent
	// otherwise. It changes no verdict.
	Advisory string `json:"advisory,omitempty"`
}

type repeatedGateDTO struct {
	// Checks are all six, always, in their stable order.
	Checks  []repeatedCheckDTO `json:"checks"`
	Verdict string             `json:"verdict"`

	// FrequencyChecks are task 106's three optional checks, always all three
	// in their stable order, in a field of their own: checks is exactly six,
	// and its consumers refuse a seventh. The verdict accounts for both.
	FrequencyChecks []frequencyCheckDTO `json:"frequency_checks"`
}

// frequencyCheckDTO is one optional check. state is not_evaluated, evaluated
// or deferred. The outcome fields are present only when evaluated, and
// missing_evidence only when deferred — a check that did not run has no
// outcome, and "passed": false there would read as a failure that happened.
type frequencyCheckDTO struct {
	Name            string               `json:"name"`
	State           string               `json:"state"`
	Rule            *string              `json:"rule,omitempty"`
	Actual          *string              `json:"actual,omitempty"`
	Bound           *string              `json:"bound,omitempty"`
	Passed          *bool                `json:"passed,omitempty"`
	Targets         []targetCallCheckDTO `json:"targets,omitempty"`
	MissingEvidence string               `json:"missing_evidence,omitempty"`
}

// targetCallCheckDTO is one target within max_calls_per_run, in
// configuration order. outcome is pass, fail or not_observed.
type targetCallCheckDTO struct {
	Target  string `json:"target"`
	Actual  string `json:"actual"`
	Max     string `json:"max"`
	Outcome string `json:"outcome"`
}

func newFrequencyCheckDTOs(checks []platform.FrequencyGateCheck) []frequencyCheckDTO {
	out := make([]frequencyCheckDTO, 0, len(checks))
	for _, c := range checks {
		dto := frequencyCheckDTO{Name: string(c.Name), State: string(c.State), MissingEvidence: c.MissingEvidence}
		if c.State == platform.GateCheckEvaluated {
			rule, actual, passed := string(c.Rule), u64(c.Actual), c.Passed
			dto.Rule, dto.Actual, dto.Passed = &rule, &actual, &passed
			if c.Name != platform.CheckMaxCallsPerRun {
				bound := u64(c.Bound)
				dto.Bound = &bound
			} else {
				dto.Actual = nil // per target, below
			}
			for _, t := range c.Targets {
				dto.Targets = append(dto.Targets, targetCallCheckDTO{
					Target: t.Target, Actual: u64(t.Actual), Max: u64(t.Max), Outcome: string(t.Outcome)})
			}
		}
		out = append(out, dto)
	}
	return out
}

type producerDTO struct {
	ControlPlaneVersion string `json:"control_plane_version"`
}

type compareRepeatedResponse struct {
	Version     string                    `json:"version"`
	Runs        int                       `json:"runs"`
	GateLimits  repeatedLimitsResponseDTO `json:"gate_limits"`
	Repetitions []repetitionDTO           `json:"repetitions"`
	Behaviors   []repeatedBehaviorDTO     `json:"behaviors"`
	Gate        repeatedGateDTO           `json:"gate"`

	// Operational is task 087's latency, errors and tokens, each side summed
	// over its completed repetitions with runs_with_evidence per side. No
	// check reads it.
	Operational operationalSectionsDTO `json:"operational"`

	// Task 106: per-target frequency, and what is known about lost
	// transitions — always "not_recorded".
	Targets         []targetFrequencyDTO `json:"targets"`
	LostTransitions string               `json:"lost_transitions"`

	Producer producerDTO `json:"producer"`
}

func (h *Handler) compareRepeated(w http.ResponseWriter, r *http.Request) {
	var request compareRepeatedRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	limits, err := request.GateLimits.decode()
	if err != nil {
		h.writeError(w, err)
		return
	}
	toIDs := func(raw []string) []platform.EvaluationRunID {
		out := make([]platform.EvaluationRunID, len(raw))
		for i, id := range raw {
			out[i] = platform.EvaluationRunID(id)
		}
		return out
	}
	result, err := h.controlPlane.CompareRepeatedEvaluations(r.Context(),
		platform.RepeatedEvaluationRequest{
			ReferenceRunIDs: toIDs(request.ReferenceRunIDs),
			CandidateRunIDs: toIDs(request.CandidateRunIDs),
			Limits:          limits,
		})
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newCompareRepeatedResponse(result, h.producerVersion))
}

// decode requires every limit and parses each as canonical decimal text.
func (l repeatedLimitsDTO) decode() (platform.RepeatedEvaluationGateLimits, error) {
	read := func(name string, value *string) (uint64, error) {
		if value == nil {
			return 0, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
				message: fmt.Sprintf("gate limit %q is required; there is no default", name)}
		}
		parsed, err := strconv.ParseUint(*value, 10, 64)
		if err != nil || strconv.FormatUint(parsed, 10) != *value {
			return 0, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
				message: fmt.Sprintf("gate limit %q must be a canonical decimal string", name)}
		}
		return parsed, nil
	}
	var out platform.RepeatedEvaluationGateLimits
	for _, field := range []struct {
		name  string
		value *string
		into  *uint64
	}{
		{"added_candidate_presence_minimum", l.AddedCandidatePresenceMinimum, &out.AddedCandidatePresenceMinimum},
		{"added_reference_presence_maximum", l.AddedReferencePresenceMaximum, &out.AddedReferencePresenceMaximum},
		{"max_repeated_added_behaviors", l.MaxRepeatedAddedBehaviors, &out.MaxRepeatedAddedBehaviors},
		{"max_block_decisions_per_run", l.MaxBlockDecisionsPerRun, &out.MaxBlockDecisionsPerRun},
		{"max_critical_risk_observations_per_run", l.MaxCriticalRiskObservationsPerRun,
			&out.MaxCriticalRiskObservationsPerRun},
	} {
		v, err := read(field.name, field.value)
		if err != nil {
			return platform.RepeatedEvaluationGateLimits{}, err
		}
		*field.into = v
	}
	// Task 106's optional limits: absent stays absent; present must be
	// canonical. How many targets, and which, is the control plane's to judge.
	for _, field := range []struct {
		name  string
		value *string
		into  *platform.OptionalGateLimit
	}{
		{"min_candidate_frequency", l.MinCandidateFrequency, &out.MinCandidateFrequency},
		{"max_lost_behaviors", l.MaxLostBehaviors, &out.MaxLostBehaviors},
	} {
		if field.value == nil {
			continue
		}
		v, err := read(field.name, field.value)
		if err != nil {
			return platform.RepeatedEvaluationGateLimits{}, err
		}
		*field.into = platform.NewOptionalGateLimit(v)
	}
	if l.MaxCallsPerRun != nil {
		out.MaxCallsPerRun = make([]platform.TargetCallLimit, 0, len(*l.MaxCallsPerRun))
		for _, entry := range *l.MaxCallsPerRun {
			maximum := entry.Max
			v, err := read("max_calls_per_run.max", &maximum)
			if err != nil {
				return platform.RepeatedEvaluationGateLimits{}, err
			}
			out.MaxCallsPerRun = append(out.MaxCallsPerRun, platform.TargetCallLimit{Target: entry.Target, Max: v})
		}
	}
	return out, nil
}

// optionalLimit renders a supplied optional limit, or nothing.
func optionalLimit(l platform.OptionalGateLimit) *string {
	if v, set := l.Maximum(); set {
		text := u64(v)
		return &text
	}
	return nil
}

func targetCallLimitDTOs(limits []platform.TargetCallLimit) []targetCallLimitDTO {
	if limits == nil {
		return nil
	}
	out := make([]targetCallLimitDTO, 0, len(limits))
	for _, l := range limits {
		out = append(out, targetCallLimitDTO{Target: l.Target, Max: u64(l.Max)})
	}
	return out
}

func newCompareRepeatedResponse(
	c platform.RepeatedEvaluationComparison, producerVersion string,
) compareRepeatedResponse {
	repetitions := make([]repetitionDTO, 0, len(c.Repetitions))
	for _, r := range c.Repetitions {
		repetitions = append(repetitions, repetitionDTO{
			Side: string(r.Side), Index: r.Index, RunID: string(r.RunID),
			Status: string(r.Status), BehavioralProfile: string(r.BehavioralProfile),
			RecordCount: u64(r.RecordCount), DistinctBehaviorCount: r.DistinctBehaviors,
			BlockDecisions: u64(r.BlockDecisions), CriticalRiskObservations: u64(r.CriticalRiskObservations),
		})
	}
	behaviors := make([]repeatedBehaviorDTO, 0, len(c.Behaviors))
	for _, b := range c.Behaviors {
		behaviors = append(behaviors, repeatedBehaviorDTO{
			FingerprintID: b.FingerprintID, Behavior: newBehaviorDescriptorDTO(b.Behavior),
			ReferenceRunsPresent: u64(b.ReferenceRunsPresent),
			CandidateRunsPresent: u64(b.CandidateRunsPresent),
			Classification:       string(b.Classification),
			ReferenceFrequency:   newFrequencyDTO(b.Reference),
			CandidateFrequency:   newFrequencyDTO(b.Candidate),
			Lost:                 b.Lost,
		})
	}
	checks := make([]repeatedCheckDTO, 0, 6)
	for _, check := range c.Gate.Checks() {
		checks = append(checks, repeatedCheckDTO{
			Name: string(check.Name), Actual: u64(check.Actual), Rule: string(check.Rule),
			Bound: u64(check.Bound), Passed: check.Passed, Advisory: check.Advisory,
		})
	}
	l := c.Limits
	return compareRepeatedResponse{
		Version: WireVersion,
		Runs:    c.Runs,
		GateLimits: repeatedLimitsResponseDTO{
			AddedCandidatePresenceMinimum:     u64(l.AddedCandidatePresenceMinimum),
			AddedReferencePresenceMaximum:     u64(l.AddedReferencePresenceMaximum),
			MaxRepeatedAddedBehaviors:         u64(l.MaxRepeatedAddedBehaviors),
			MaxBlockDecisionsPerRun:           u64(l.MaxBlockDecisionsPerRun),
			MaxCriticalRiskObservationsPerRun: u64(l.MaxCriticalRiskObservationsPerRun),
			MinCandidateFrequency:             optionalLimit(l.MinCandidateFrequency),
			MaxLostBehaviors:                  optionalLimit(l.MaxLostBehaviors),
			MaxCallsPerRun:                    targetCallLimitDTOs(l.MaxCallsPerRun),
		},
		Repetitions:     repetitions,
		Behaviors:       behaviors,
		Targets:         newTargetFrequencyDTOs(c.Targets),
		LostTransitions: c.LostTransitions,
		Operational:     newOperationalSectionsDTO(c.Operational, c.Cost),
		Gate: repeatedGateDTO{Checks: checks, Verdict: string(c.Gate.Verdict()),
			FrequencyChecks: newFrequencyCheckDTOs(c.Gate.FrequencyChecks())},
		Producer: producerDTO{ControlPlaneVersion: producerVersion},
	}
}

// buildVersion is what this control plane reports as its producer version:
// the main module's version and its VCS revision, from Go's own build
// information — the same source `trustvian version` reads, without the
// link-time stamping contract that would add. "unknown" when the binary
// carries none (a test binary, or a build without VCS stamping).
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	version := info.Main.Version
	if version == "" {
		version = "unknown"
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			version += " " + setting.Value
		case "vcs.modified":
			if setting.Value == "true" {
				version += "+dirty"
			}
		}
	}
	return version
}

func newTargetFrequencyDTOs(targets []platform.RepeatedTargetFrequency) []targetFrequencyDTO {
	out := make([]targetFrequencyDTO, 0, len(targets))
	for _, t := range targets {
		dto := targetFrequencyDTO{
			TargetName: t.Target.Name, TargetCategory: t.Target.Category,
			ReferenceFrequency: newFrequencyDTO(t.Reference),
			CandidateFrequency: newFrequencyDTO(t.Candidate),
			CallRatioAvailable: t.RatioAvailable,
		}
		if t.RatioAvailable {
			ratio := u64(t.CallRatioPermille)
			dto.CallRatioPermille = &ratio
		}
		out = append(out, dto)
	}
	return out
}
