package httpapi

// Domain values to wire DTOs. Mechanical, exhaustive, and decision-free.
//
// Nothing here computes a comparison, derives a verdict, or interprets a
// number. Every value comes from a public accessor on a value the control
// plane already produced.

import (
	"encoding/json"
	"time"

	trustvian "github.com/trustvian/trustvian"
	platform "trustvian-platform"
)

// jsonRaw keeps the DecisionRecord undecoded until it is handled separately.
type jsonRaw = json.RawMessage

// formatTime renders a timestamp, preserving offset and nanoseconds. A zero
// time becomes empty rather than a fabricated instant.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// u64 renders a counter as canonical decimal text, for the same reason the
// sequence travels that way: uint64 exceeds exact JSON number precision.
func u64(v uint64) string { return platform.FormatSequence(v) }

func newRateDTO(r platform.EvidenceRate) rateDTO {
	return rateDTO{Count: u64(r.Count()), Total: u64(r.Total())}
}

func newRateComparisonDTO(c platform.RateComparison) rateComparisonDTO {
	return rateComparisonDTO{
		Reference: newRateDTO(c.Reference()),
		Candidate: newRateDTO(c.Candidate()),
	}
}

func newMetricSummaryDTO(s platform.MetricSummary) metricSummaryDTO {
	mean, known := s.Mean()
	return metricSummaryDTO{
		Count: u64(s.Count), Sum: s.Sum, Min: s.Min, Max: s.Max,
		Mean: mean, Known: known,
	}
}

func newMetricComparisonDTO(c platform.MetricComparison) metricComparisonDTO {
	delta, known := c.MeanDelta()
	return metricComparisonDTO{
		Reference:      newMetricSummaryDTO(c.Reference()),
		Candidate:      newMetricSummaryDTO(c.Candidate()),
		MeanDelta:      delta,
		MeanDeltaKnown: known,
	}
}

func newRatioDTO(value float64, known bool) ratioDTO {
	return ratioDTO{Value: value, Known: known}
}

func newBehaviorDescriptorDTO(b trustvian.StableFeatures) behaviorDescriptorDTO {
	return behaviorDescriptorDTO{
		ActorType:         string(b.ActorType),
		OperationCategory: string(b.OperationCategory),
		OperationName:     b.OperationName,
		TargetName:        b.TargetName,
		TargetCategory:    string(b.TargetCategory),
		Environment:       b.Environment,
	}
}

// newBehaviorDiffDTO renders the diff, deltas included.
//
// Task 054 already bounds deltas at 1024, so returning them is bounded by
// construction. They are returned and never stored — ADR 0030 recomputes
// derived values rather than keeping a second copy.
func newBehaviorDiffDTO(d platform.BehaviorDiff) behaviorDiffDTO {
	deltas := d.Deltas()
	out := make([]behaviorDeltaDTO, 0, len(deltas))
	for _, delta := range deltas {
		out = append(out, behaviorDeltaDTO{
			Change:                string(delta.Presence),
			FingerprintID:         delta.FingerprintID,
			Behavior:              newBehaviorDescriptorDTO(delta.Behavior),
			ReferenceObservations: u64(delta.ReferenceCount),
			CandidateObservations: u64(delta.CandidateCount),
		})
	}
	return behaviorDiffDTO{
		ReferenceObservationCount: u64(d.ReferenceObservationCount()),
		CandidateObservationCount: u64(d.CandidateObservationCount()),
		AddedCount:                d.AddedCount(),
		RemovedCount:              d.RemovedCount(),
		SharedCount:               d.SharedCount(),
		Deltas:                    out,
	}
}

func newScorecardDTO(s platform.EvaluationScorecard) scorecardDTO {
	decisions := s.Decisions()
	risks := s.Risks()
	approvals := s.Approvals()
	policy := s.PolicySelection()
	metrics := s.Metrics()
	behavior := s.Behavior()

	addedRate, addedKnown := behavior.AddedCandidateRate()
	removedRate, removedKnown := behavior.RemovedReferenceRate()
	overlap, overlapKnown := behavior.PresenceOverlap()

	return scorecardDTO{
		ReferenceRunID:       string(s.ReferenceRunID()),
		ReferenceCandidateID: string(s.ReferenceCandidateID()),
		CandidateRunID:       string(s.CandidateRunID()),
		CandidateCandidateID: string(s.CandidateCandidateID()),
		Environment:          string(s.Environment()),

		ReferenceRecordCount: u64(s.ReferenceRecordCount()),
		CandidateRecordCount: u64(s.CandidateRecordCount()),

		Decisions: decisionComparisonDTO{
			Allow:           newRateComparisonDTO(decisions.Allow),
			ObserveOnly:     newRateComparisonDTO(decisions.ObserveOnly),
			Alert:           newRateComparisonDTO(decisions.Alert),
			Challenge:       newRateComparisonDTO(decisions.Challenge),
			RequireApproval: newRateComparisonDTO(decisions.RequireApproval),
			Block:           newRateComparisonDTO(decisions.Block),
		},
		Risks: riskComparisonDTO{
			Low:      newRateComparisonDTO(risks.Low),
			Medium:   newRateComparisonDTO(risks.Medium),
			High:     newRateComparisonDTO(risks.High),
			Critical: newRateComparisonDTO(risks.Critical),
		},
		Approvals: approvalComparisonDTO{
			Unspecified: newRateComparisonDTO(approvals.Unspecified),
			NotRequired: newRateComparisonDTO(approvals.NotRequired),
			Required:    newRateComparisonDTO(approvals.Required),
			Approved:    newRateComparisonDTO(approvals.Approved),
			Denied:      newRateComparisonDTO(approvals.Denied),
		},
		PolicySelection: policySelectionComparisonDTO{
			MatchedRule:    newRateComparisonDTO(policy.MatchedRule),
			MatchedDefault: newRateComparisonDTO(policy.MatchedDefault),
		},
		Metrics: metricComparisonsDTO{
			IdentityConfidence: newMetricComparisonDTO(metrics.IdentityConfidence),
			AnomalyScore:       newMetricComparisonDTO(metrics.AnomalyScore),
			AnomalyConfidence:  newMetricComparisonDTO(metrics.AnomalyConfidence),
			TrustScore:         newMetricComparisonDTO(metrics.TrustScore),
			ContextRisk:        newMetricComparisonDTO(metrics.ContextRisk),
		},
		Behavior: behaviorSummaryDTO{
			ReferenceDistinctCount: behavior.ReferenceDistinctCount,
			CandidateDistinctCount: behavior.CandidateDistinctCount,
			AddedCount:             behavior.AddedCount,
			RemovedCount:           behavior.RemovedCount,
			SharedCount:            behavior.SharedCount,
			AddedCandidateRate:     newRatioDTO(addedRate, addedKnown),
			RemovedReferenceRate:   newRatioDTO(removedRate, removedKnown),
			PresenceOverlap:        newRatioDTO(overlap, overlapKnown),
		},
	}
}

func newMinimumGateDTO(g platform.MinimumCountGate) minimumGateDTO {
	return minimumGateDTO{Actual: u64(g.Actual), Minimum: u64(g.Minimum), Passed: g.Passed}
}

func newMaximumGateDTO(g platform.MaximumCountGate) maximumGateDTO {
	return maximumGateDTO{Actual: u64(g.Actual), Maximum: u64(g.Maximum), Passed: g.Passed}
}

func newGateResultDTO(r platform.EvaluationGateResult) gateResultDTO {
	return gateResultDTO{
		ReferenceEvidence:        newMinimumGateDTO(r.ReferenceEvidence()),
		CandidateEvidence:        newMinimumGateDTO(r.CandidateEvidence()),
		AddedBehaviors:           newMaximumGateDTO(r.AddedBehaviors()),
		BlockDecisions:           newMaximumGateDTO(r.BlockDecisions()),
		CriticalRiskObservations: newMaximumGateDTO(r.CriticalRiskObservations()),
		Verdict:                  string(r.Verdict()),
	}
}

func newCompareResponse(c platform.EvaluationComparison) compareResponse {
	return compareResponse{
		Version:   WireVersion,
		Reference: string(c.Reference.ID()),
		Candidate: string(c.Candidate.ID()),
		Diff:      newBehaviorDiffDTO(c.Diff),
		Scorecard: newScorecardDTO(c.Scorecard),
		Gate:      newGateResultDTO(c.Gate),
	}
}

func newProgressResponse(p platform.EvaluationProgressReport) progressResponse {
	return progressResponse{
		Version:                  WireVersion,
		RunID:                    string(p.Run.ID()),
		Status:                   string(p.Run.Status()),
		RecordCount:              u64(p.RecordCount),
		BehaviorObservationCount: u64(p.BehaviorObservationCount),
		DistinctBehaviorCount:    p.DistinctBehaviorCount,
		BehaviorComplete:         p.BehaviorComplete,
		NextIngestSequence:       u64(p.NextIngestSequence),
	}
}

// ---------------------------------------------------------------------
// Realtime
// ---------------------------------------------------------------------

func newRealtimeScopeDTO(s platform.RealtimeScope) realtimeScopeDTO {
	return realtimeScopeDTO{
		ProjectID:         string(s.ProjectID),
		AgentID:           string(s.AgentID),
		CandidateID:       string(s.CandidateID),
		RunID:             string(s.RunID),
		Environment:       string(s.Environment),
		BehavioralProfile: string(s.BehavioralProfile),
	}
}

// newRealtimeEventPayload renders one event, populating only the half its kind
// uses so a client is never handed a zero-valued observation to interpret.
func newRealtimeEventPayload(e platform.RealtimeEvent) realtimeEventPayload {
	payload := realtimeEventPayload{
		Version: WireVersion,
		Kind:    string(e.Kind),
		Scope:   newRealtimeScopeDTO(e.Scope),
	}

	if e.Kind == platform.RealtimeObservationRecorded {
		o := e.Observation
		payload.Observation = &realtimeObservationDTO{
			Sequence:         u64(o.Sequence),
			RecordCount:      u64(o.RecordCount),
			BehaviorComplete: o.BehaviorComplete,

			FingerprintID: o.FingerprintID,
			Behavior:      newBehaviorDescriptorDTO(o.Behavior),

			Decision:       o.Decision,
			RiskLevel:      o.RiskLevel,
			ApprovalStatus: string(o.ApprovalStatus),

			TrustScore:        o.TrustScore,
			AnomalyScore:      o.AnomalyScore,
			AnomalyConfidence: o.AnomalyConfidence,

			NewBehavior: o.NewBehavior,
		}
		return payload
	}

	evaluation := e.Evaluation
	payload.Evaluation = &realtimeEvaluationDTO{
		Status:        string(evaluation.Status),
		CreatedAt:     formatTime(evaluation.CreatedAt),
		StartedAt:     formatTime(evaluation.StartedAt),
		FinishedAt:    formatTime(evaluation.FinishedAt),
		FailureReason: evaluation.FailureReason,
	}
	return payload
}

// newEnvironmentPositionDTO renders one decision-time environment snapshot.
func newEnvironmentPositionDTO(p platform.EnvironmentPosition) environmentPositionDTO {
	return environmentPositionDTO{
		Ref:      string(p.Ref),
		Rank:     p.Rank,
		Revision: u64(p.Revision),
	}
}

// newPromotionResponse renders one stored decision.
//
// Every field comes from the record. Nothing here recomputes a gate, a
// verdict or an outcome — the point of snapshotting the gate result is that a
// reader sees what the platform relied on, not what this build would derive.
func newPromotionResponse(p platform.Promotion) promotionResponse {
	limits := p.GateLimits()
	return promotionResponse{
		Version: WireVersion,

		ID:        string(p.ID()),
		ProjectID: string(p.ProjectID()),

		CandidateID:          string(p.CandidateID()),
		ReferenceCandidateID: string(p.GateResult().ReferenceCandidateID()),

		ReferenceRunID: string(p.ReferenceRunID()),
		CandidateRunID: string(p.CandidateRunID()),

		SourceEnvironment: newEnvironmentPositionDTO(p.Source()),
		TargetEnvironment: newEnvironmentPositionDTO(p.Target()),

		GateLimits: gateLimitsResponseDTO{
			MaxAddedBehaviors:           u64(limits.MaxAddedBehaviors),
			MaxBlockDecisions:           u64(limits.MaxBlockDecisions),
			MaxCriticalRiskObservations: u64(limits.MaxCriticalRiskObservations),
		},
		GateResult: newGateResultDTO(p.GateResult()),

		Outcome:   string(p.Outcome()),
		DecidedAt: p.DecidedAt().Format(time.RFC3339Nano),
	}
}

func newPromotionListResponse(
	projectID string, promotions []platform.Promotion, nextAfter string,
) promotionListResponse {
	rows := make([]promotionResponse, 0, len(promotions))
	for _, promotion := range promotions {
		rows = append(rows, newPromotionResponse(promotion))
	}
	return promotionListResponse{
		Version:    WireVersion,
		ProjectID:  projectID,
		Promotions: rows,
		NextAfter:  nextAfter,
	}
}
