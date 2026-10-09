package platform

// Sameness on the repeated comparison (task 086).
//
// "Did I change the model, the prompt, the scenario or the inputs?" is answered
// on the comparison itself, from what each side's execution recorded. Each
// answer is true, false or not_recorded. A value missing on either side is not
// a different value, so a missing one is never false.
//
// Sameness is evidence beside the gate: no check reads it, it never changes the
// verdict and it never refuses a comparison. A difference adds a warning,
// which states the fact and gives no advice — a changed model is often the
// point of the comparison, and only the developer knows whether it was
// intended. Warnings are not ADR 0064 suggestions.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SamenessState is one sameness answer.
type SamenessState string

const (
	SameTrue        SamenessState = "true"
	SameFalse       SamenessState = "false"
	SameNotRecorded SamenessState = "not_recorded"
)

// ComparisonSameness says whether both sides ran the same scenario, inputs,
// model and prompt, with both sides' recorded values so a reader can check.
type ComparisonSameness struct {
	SameScenario  SamenessState
	SameInputs    SamenessState
	SameModel     SamenessState
	SamePromptRef SamenessState

	Reference, Candidate SideProvenance
}

// ComparisonWarning is one stated difference. Code is the stable field to
// branch on; Text is a fixed sentence per code.
type ComparisonWarning struct {
	Code string
	Text string
}

// Warning codes, one per sameness answer, in that order.
const (
	WarningScenarioDiffers  = "scenario_differs"
	WarningInputsDiffer     = "inputs_differ"
	WarningModelDiffers     = "model_differs"
	WarningPromptRefDiffers = "prompt_ref_differs"
)

func sameWhen(recorded, equal bool) SamenessState {
	switch {
	case !recorded:
		return SameNotRecorded
	case equal:
		return SameTrue
	}
	return SameFalse
}

// newComparisonSameness compares two sides' provenance.
//
// Inputs are recorded with the scenario: both sides recorded, two scenarios
// that declared no inputs have the same inputs, and one that declared inputs
// and one that did not have different ones.
func newComparisonSameness(reference, candidate SideProvenance) (ComparisonSameness, []ComparisonWarning) {
	scenarios := reference.ScenarioRecorded() && candidate.ScenarioRecorded()
	s := ComparisonSameness{
		SameScenario: sameWhen(scenarios, reference.ScenarioDigest == candidate.ScenarioDigest),
		SameInputs: sameWhen(scenarios, reference.InputsDeclared == candidate.InputsDeclared &&
			reference.InputDigest == candidate.InputDigest),
		SameModel: sameWhen(reference.Model != "" && candidate.Model != "", reference.Model == candidate.Model),
		SamePromptRef: sameWhen(reference.PromptRef.Stated() && candidate.PromptRef.Stated(),
			reference.PromptRef == candidate.PromptRef),
		Reference: reference, Candidate: candidate,
	}
	warnings := []ComparisonWarning{}
	for _, w := range []struct {
		state      SamenessState
		code, text string
	}{
		{s.SameScenario, WarningScenarioDiffers, "The reference and candidate sides ran different scenario definitions."},
		{s.SameInputs, WarningInputsDiffer, "The reference and candidate sides ran with different inputs."},
		{s.SameModel, WarningModelDiffers, "The reference and candidate sides declared different models."},
		{s.SamePromptRef, WarningPromptRefDiffers, "The reference and candidate sides declared different prompt references."},
	} {
		if w.state == SameFalse {
			warnings = append(warnings, ComparisonWarning{Code: w.code, Text: w.text})
		}
	}
	return s, warnings
}

// RunProvenanceStore reads what the executions that ran some runs recorded
// about them. Both shipped backends implement it.
type RunProvenanceStore interface {
	// RunProvenance returns, for each run any execution recorded, the
	// distinct provenance values of the sides it was recorded on. A run no
	// execution recorded is absent. Bounded: at most maxRunProvenanceRows
	// values in all, and complete reports whether every value fit.
	RunProvenance(ctx context.Context, runIDs []EvaluationRunID) (
		provenance map[EvaluationRunID][]SideProvenance, complete bool, err error)
}

// maxRunProvenanceRows bounds one lookup. In a valid database a run has one
// provenance value, however many executions reused it, because a reused side
// carries a copy; the bound is room for that with a margin, never a sample.
const maxRunProvenanceRows = 4 * MaxRepetitions

// sideProvenanceOfRuns is the provenance one side of a comparison can state:
// the one value every run on the side was recorded with. A run no execution
// recorded, a run recorded with two different values, or two runs recorded
// differently leave the side with nothing it can state, which reads as
// not_recorded.
func sideProvenanceOfRuns(ids []EvaluationRunID, recorded map[EvaluationRunID][]SideProvenance) SideProvenance {
	var out SideProvenance
	for i, id := range ids {
		values := recorded[id]
		if len(values) != 1 || (i > 0 && values[0] != out) {
			return SideProvenance{}
		}
		out = values[0]
	}
	return out
}

// samenessOfRuns is the sameness of a comparison of arbitrary runs: each side
// as recorded by the executions that ran its runs.
func (c *ControlPlane) samenessOfRuns(
	ctx context.Context, request RepeatedEvaluationRequest,
) (ComparisonSameness, []ComparisonWarning, error) {
	store, ok := c.control.(RunProvenanceStore)
	if !ok {
		s, w := newComparisonSameness(SideProvenance{}, SideProvenance{})
		return s, w, nil
	}
	ids := append(append([]EvaluationRunID(nil), request.ReferenceRunIDs...), request.CandidateRunIDs...)
	recorded, complete, err := store.RunProvenance(ctx, ids)
	if err != nil {
		return ComparisonSameness{}, nil, err
	}
	if !complete {
		s, w := newComparisonSameness(SideProvenance{}, SideProvenance{})
		return s, w, nil
	}
	s, w := newComparisonSameness(sideProvenanceOfRuns(request.ReferenceRunIDs, recorded),
		sideProvenanceOfRuns(request.CandidateRunIDs, recorded))
	return s, w, nil
}

// loadRunProvenance is RunProvenance for both backends: one bounded query,
// each association's own side's columns, distinct per run.
func loadRunProvenance(
	ctx context.Context, q evidenceQuerier, runIDs []EvaluationRunID,
) (map[EvaluationRunID][]SideProvenance, bool, error) {
	out := map[EvaluationRunID][]SideProvenance{}
	if len(runIDs) == 0 {
		return out, true, nil
	}
	columns := make([]string, 0, len(scenarioProvenanceColumnNames))
	for _, name := range scenarioProvenanceColumnNames {
		columns = append(columns, fmt.Sprintf("CASE r.side WHEN '%s' THEN e.%s_%s ELSE e.%s_%s END",
			SideReference, SideReference, name, SideCandidate, name))
	}
	args := make([]any, 0, len(runIDs)+1)
	for _, id := range runIDs {
		args = append(args, string(id))
	}
	args = append(args, maxRunProvenanceRows+1)
	rows, err := q.query(ctx, q.rebind(
		`SELECT DISTINCT r.run_id, `+strings.Join(columns, ", ")+`
		   FROM `+tableScenarioRepetitions+` r
		   JOIN `+tableScenarioExecutions+` e ON e.id = r.execution_id
		  WHERE r.run_id IN (?`+strings.Repeat(", ?", len(runIDs)-1)+`)
		  ORDER BY r.run_id
		  LIMIT ?`), args...)
	if err != nil {
		return nil, false, fmt.Errorf("platform: load run provenance: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		if n > maxRunProvenanceRows {
			return nil, false, nil
		}
		var runID string
		var columns [5]sql.NullString
		if err := rows.Scan(&runID, &columns[0], &columns[1], &columns[2], &columns[3], &columns[4]); err != nil {
			return nil, false, fmt.Errorf("platform: load run provenance: %w", err)
		}
		for _, column := range columns {
			if column.Valid && column.String == "" {
				return nil, false, fmt.Errorf("%w: run %s has an empty provenance value", ErrStoreCorrupt, preview(runID))
			}
		}
		p := sideProvenanceFromColumns(columns[:])
		if err := p.validate(); err != nil {
			return nil, false, fmt.Errorf("%w: run %s provenance: %v", ErrStoreCorrupt, preview(runID), err)
		}
		out[EvaluationRunID(runID)] = append(out[EvaluationRunID(runID)], p)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("platform: load run provenance: %w", err)
	}
	return out, true, nil
}
