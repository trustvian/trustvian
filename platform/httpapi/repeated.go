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
	Producer    producerDTO               `json:"producer"`
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
	return out, nil
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
		},
		Repetitions: repetitions,
		Behaviors:   behaviors,
		Gate:        repeatedGateDTO{Checks: checks, Verdict: string(c.Gate.Verdict())},
		Producer:    producerDTO{ControlPlaneVersion: producerVersion},
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
