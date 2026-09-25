package platform

// The promotion registry's storage logic, written once for both backends.
//
// What differs between SQLite and PostgreSQL is exactly one thing: how a
// transaction takes the write intent on the two environment rows a decision
// was built against. Everything after that — the revalidation, the ordering
// re-ask, the row shape, the page query — is the same text, because two
// copies of "is this decision still valid" is precisely where the two
// backends would drift apart while both passing their own tests.
//
// See docs/tasks/v1.0/066-promotion-workflow.md § Concurrency for the
// invariant the transaction exists to hold.

import (
	"context"
	"fmt"
)

// promotionWriter is one backend's transaction, seen through the operations a
// promotion insert needs.
//
// Deliberately not a general transaction abstraction: each backend owns its
// own transaction lifecycle in its own file, where the dialect is visible.
type promotionWriter interface {
	rowQuerier

	exec(ctx context.Context, query string, args ...any) (int64, error)

	// writeError maps one backend's constraint failure onto the shared
	// sentinels.
	//
	// The promotion insert relies on the primary key rather than a
	// check-then-insert, so a duplicate identifier surfaces as a dialect's own
	// constraint error — and each dialect spells that differently. Mapping it
	// here would mean this file knowing both.
	writeError(kind, id string, err error) error

	// lockEnvironment takes the write intent on one environment row for the
	// rest of the transaction, and reports ErrStoreNotFound when it is gone.
	//
	// Callers acquire both of a promotion's environments in (project_id, ref)
	// byte order, so two promotions over an overlapping pair reach the shared
	// row at the same point in their sequence and one waits. On SQLite the
	// write transaction serializes writers anyway and the order is for
	// symmetry. See promotionEnvironmentOrder for why the order is derived
	// from the rows rather than from source and target.
	lockEnvironment(ctx context.Context, projectID, ref string) error
}

// insertPromotionLocked revalidates the decision's environment state and
// inserts, with both rows already locked by the caller.
//
// The store revalidates; it does not re-decide. It does not recompute the
// gate, re-read evaluation evidence, reconstruct a scorecard or choose an
// outcome. Its entire job is the environment-staleness invariant:
//
//	both revisions still match the snapshot  → otherwise ErrStoreConflict
//	CanPromote still true on current values  → otherwise ErrStoreConflict
//	insert                                   → duplicate is ErrStoreAlreadyExists
//
// The revision check is the primary guard and the CanPromote call is the
// independent one. Task 065 guarantees every environment mutation advances
// the revision by exactly one, which makes the ordering call redundant *if
// that discipline holds everywhere, forever*. It is kept anyway, because it
// costs one function call and does not depend on the discipline: an
// out-of-band UPDATE or a future mutation path that forgot to bump would slip
// past a revision check alone and would not slip past this one.
func insertPromotionLocked(ctx context.Context, w promotionWriter, p Promotion) error {
	current := func(position EnvironmentPosition) (Environment, error) {
		return loadEnvironment(ctx, w, p.ProjectID(), position.Ref)
	}

	source, err := current(p.Source())
	if err != nil {
		return err
	}
	target, err := current(p.Target())
	if err != nil {
		return err
	}

	if source.Revision() != p.Source().Revision {
		return fmt.Errorf(
			"%w: environment %s is at revision %d, the decision used %d",
			ErrStoreConflict, preview(string(p.Source().Ref)),
			source.Revision(), p.Source().Revision)
	}
	if target.Revision() != p.Target().Revision {
		return fmt.Errorf(
			"%w: environment %s is at revision %d, the decision used %d",
			ErrStoreConflict, preview(string(p.Target().Ref)),
			target.Revision(), p.Target().Revision)
	}

	if !CanPromote(source, target) {
		return fmt.Errorf(
			"%w: %s no longer promotes toward %s",
			ErrStoreConflict, preview(string(p.Source().Ref)), preview(string(p.Target().Ref)))
	}

	gate := p.GateResult()
	if _, err := w.exec(ctx, w.rebind(
		`INSERT INTO `+tablePromotions+` (
		   id, project_id, candidate_id, reference_candidate_id,
		   reference_run_id, candidate_run_id,
		   source_environment_ref, source_environment_rank, source_environment_revision,
		   target_environment_ref, target_environment_rank, target_environment_revision,
		   max_added_behaviors, max_block_decisions, max_critical_risk_observations,
		   gate_reference_evidence_actual, gate_reference_evidence_minimum,
		   gate_reference_evidence_passed,
		   gate_candidate_evidence_actual, gate_candidate_evidence_minimum,
		   gate_candidate_evidence_passed,
		   gate_added_behaviors_actual, gate_added_behaviors_passed,
		   gate_block_decisions_actual, gate_block_decisions_passed,
		   gate_critical_risk_actual, gate_critical_risk_passed,
		   gate_verdict, outcome, decided_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		           ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		string(p.ID()), string(p.ProjectID()), string(p.CandidateID()),
		string(gate.ReferenceCandidateID()),
		string(p.ReferenceRunID()), string(p.CandidateRunID()),

		string(p.Source().Ref), int64(p.Source().Rank), uint64Text(p.Source().Revision),
		string(p.Target().Ref), int64(p.Target().Rank), uint64Text(p.Target().Revision),

		uint64Text(p.GateLimits().MaxAddedBehaviors),
		uint64Text(p.GateLimits().MaxBlockDecisions),
		uint64Text(p.GateLimits().MaxCriticalRiskObservations),

		uint64Text(gate.ReferenceEvidence().Actual),
		uint64Text(gate.ReferenceEvidence().Minimum),
		boolInt(gate.ReferenceEvidence().Passed),
		uint64Text(gate.CandidateEvidence().Actual),
		uint64Text(gate.CandidateEvidence().Minimum),
		boolInt(gate.CandidateEvidence().Passed),
		uint64Text(gate.AddedBehaviors().Actual), boolInt(gate.AddedBehaviors().Passed),
		uint64Text(gate.BlockDecisions().Actual), boolInt(gate.BlockDecisions().Passed),
		uint64Text(gate.CriticalRiskObservations().Actual),
		boolInt(gate.CriticalRiskObservations().Passed),

		string(gate.Verdict()), string(p.Outcome()), timeText(p.DecidedAt()),
	); err != nil {
		// The primary key is what refuses a duplicate identifier, so the
		// dialect's constraint error is the one that has to become
		// ErrStoreAlreadyExists.
		return w.writeError("promotion", string(p.ID()), err)
	}
	return nil
}

// promotionColumns is the select list every promotion read shares, in the
// order scanPromotionRow expects.
const promotionColumns = `
	id, project_id, candidate_id, reference_candidate_id,
	reference_run_id, candidate_run_id,
	source_environment_ref, source_environment_rank, source_environment_revision,
	target_environment_ref, target_environment_rank, target_environment_revision,
	max_added_behaviors, max_block_decisions, max_critical_risk_observations,
	gate_reference_evidence_actual, gate_reference_evidence_minimum,
	gate_reference_evidence_passed,
	gate_candidate_evidence_actual, gate_candidate_evidence_minimum,
	gate_candidate_evidence_passed,
	gate_added_behaviors_actual, gate_added_behaviors_passed,
	gate_block_decisions_actual, gate_block_decisions_passed,
	gate_critical_risk_actual, gate_critical_risk_passed,
	gate_verdict, outcome, decided_at`

// promotionRow is one scanned row, before the domain sees it.
type promotionRow struct {
	id, projectID, candidateID, referenceCandidateID string
	referenceRunID, candidateRunID                   string

	sourceRef      string
	sourceRank     int64
	sourceRevision string
	targetRef      string
	targetRank     int64
	targetRevision string

	maxAdded, maxBlock, maxCritical string

	refEvidenceActual, refEvidenceMinimum string
	refEvidencePassed                     int
	candEvidenceActual, candEvidenceMin   string
	candEvidencePassed                    int
	addedActual                           string
	addedPassed                           int
	blockActual                           string
	blockPassed                           int
	criticalActual                        string
	criticalPassed                        int

	verdict, outcome, decidedAt string
}

func (r *promotionRow) targets() []any {
	return []any{
		&r.id, &r.projectID, &r.candidateID, &r.referenceCandidateID,
		&r.referenceRunID, &r.candidateRunID,
		&r.sourceRef, &r.sourceRank, &r.sourceRevision,
		&r.targetRef, &r.targetRank, &r.targetRevision,
		&r.maxAdded, &r.maxBlock, &r.maxCritical,
		&r.refEvidenceActual, &r.refEvidenceMinimum, &r.refEvidencePassed,
		&r.candEvidenceActual, &r.candEvidenceMin, &r.candEvidencePassed,
		&r.addedActual, &r.addedPassed,
		&r.blockActual, &r.blockPassed,
		&r.criticalActual, &r.criticalPassed,
		&r.verdict, &r.outcome, &r.decidedAt,
	}
}

// restorePromotionRow turns one scanned row into a domain value.
//
// Every scalar is parsed by the helper that refuses anything this code would
// not have written — a non-canonical uint64, a flag that is neither 0 nor 1,
// an unparseable timestamp. What is *not* re-derived is any Passed flag or
// the verdict: those are historical evidence and come back exactly as stored.
func restorePromotionRow(r promotionRow) (Promotion, error) {
	corrupt := func(err error) (Promotion, error) {
		return Promotion{}, fmt.Errorf("%w: promotion %s: %w",
			ErrStoreCorrupt, preview(r.id), err)
	}

	rank := func(field string, v int64) (uint16, error) {
		if v < 0 || v > maxEnvironmentRank {
			return 0, fmt.Errorf("%s %d is outside 0..%d", field, v, maxEnvironmentRank)
		}
		return uint16(v), nil
	}
	sourceRank, err := rank("source rank", r.sourceRank)
	if err != nil {
		return corrupt(err)
	}
	targetRank, err := rank("target rank", r.targetRank)
	if err != nil {
		return corrupt(err)
	}

	type counter struct {
		field string
		text  string
		into  *uint64
	}
	var (
		sourceRev, targetRev                     uint64
		maxAdded, maxBlock, maxCritical          uint64
		refActual, refMinimum                    uint64
		candActual, candMinimum                  uint64
		addedActual, blockActual, criticalActual uint64
	)
	for _, c := range []counter{
		{"source revision", r.sourceRevision, &sourceRev},
		{"target revision", r.targetRevision, &targetRev},
		{"max added behaviors", r.maxAdded, &maxAdded},
		{"max block decisions", r.maxBlock, &maxBlock},
		{"max critical risk observations", r.maxCritical, &maxCritical},
		{"reference evidence actual", r.refEvidenceActual, &refActual},
		{"reference evidence minimum", r.refEvidenceMinimum, &refMinimum},
		{"candidate evidence actual", r.candEvidenceActual, &candActual},
		{"candidate evidence minimum", r.candEvidenceMin, &candMinimum},
		{"added behaviors actual", r.addedActual, &addedActual},
		{"block decisions actual", r.blockActual, &blockActual},
		{"critical risk actual", r.criticalActual, &criticalActual},
	} {
		value, err := parseUint64Text(c.field, c.text)
		if err != nil {
			return corrupt(err)
		}
		*c.into = value
	}

	type flag struct {
		field string
		value int
		into  *bool
	}
	var refPassed, candPassed, addedPassed, blockPassed, criticalPassed bool
	for _, f := range []flag{
		{"reference evidence passed", r.refEvidencePassed, &refPassed},
		{"candidate evidence passed", r.candEvidencePassed, &candPassed},
		{"added behaviors passed", r.addedPassed, &addedPassed},
		{"block decisions passed", r.blockPassed, &blockPassed},
		{"critical risk passed", r.criticalPassed, &criticalPassed},
	} {
		value, err := parseStoredBool(f.field, f.value)
		if err != nil {
			return corrupt(err)
		}
		*f.into = value
	}

	decidedAt, err := parseTimeText("promotion decided_at", r.decidedAt)
	if err != nil {
		return corrupt(err)
	}

	gate, err := restoreEvaluationGateResult(
		EvaluationRunID(r.referenceRunID), CandidateID(r.referenceCandidateID),
		EvaluationRunID(r.candidateRunID), CandidateID(r.candidateID),
		EnvironmentRef(r.sourceRef),
		MinimumCountGate{Actual: refActual, Minimum: refMinimum, Passed: refPassed},
		MinimumCountGate{Actual: candActual, Minimum: candMinimum, Passed: candPassed},
		MaximumCountGate{Actual: addedActual, Maximum: maxAdded, Passed: addedPassed},
		MaximumCountGate{Actual: blockActual, Maximum: maxBlock, Passed: blockPassed},
		MaximumCountGate{Actual: criticalActual, Maximum: maxCritical, Passed: criticalPassed},
		GateVerdict(r.verdict),
	)
	if err != nil {
		return corrupt(err)
	}

	return restorePromotion(
		PromotionID(r.id), ProjectID(r.projectID), CandidateID(r.candidateID),
		EnvironmentPosition{
			Ref: EnvironmentRef(r.sourceRef), Rank: sourceRank, Revision: sourceRev},
		EnvironmentPosition{
			Ref: EnvironmentRef(r.targetRef), Rank: targetRank, Revision: targetRev},
		EvaluationGateLimits{
			MaxAddedBehaviors:           maxAdded,
			MaxBlockDecisions:           maxBlock,
			MaxCriticalRiskObservations: maxCritical,
		},
		gate, PromotionOutcome(r.outcome), decidedAt,
	)
}

// loadPromotion reads one promotion by identifier.
func loadPromotion(ctx context.Context, q rowQuerier, id PromotionID) (Promotion, error) {
	var row promotionRow
	err := q.queryRow(ctx, q.rebind(
		`SELECT `+promotionColumns+` FROM `+tablePromotions+` WHERE id = ?`),
		string(id)).Scan(row.targets()...)
	switch {
	case q.noRows(err):
		return Promotion{}, fmt.Errorf("%w: promotion %s", ErrStoreNotFound, preview(string(id)))
	case err != nil:
		return Promotion{}, fmt.Errorf("platform: load promotion: %w", err)
	}
	return restorePromotionRow(row)
}

// queryPromotionPage reads at most limit rows in identifier byte order.
//
// The bound is applied in SQL, so a project with a long history costs one page
// of memory rather than all of it. Traversal is by id because id is immutable
// and caller-owned — see ControlStore.ProjectPromotions for why not by time.
func queryPromotionPage(
	ctx context.Context, q evidenceQuerier, projectID ProjectID, after PromotionID, limit int,
) ([]Promotion, error) {
	rows, err := q.query(ctx, q.rebind(
		`SELECT `+promotionColumns+` FROM `+tablePromotions+`
		 WHERE project_id = ? AND id > ?
		 ORDER BY id
		 LIMIT ?`),
		string(projectID), string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("platform: list promotions: %w", err)
	}
	defer rows.Close()

	promotions := make([]Promotion, 0, limit)
	for rows.Next() {
		var row promotionRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("platform: list promotions: %w", err)
		}
		promotion, err := restorePromotionRow(row)
		if err != nil {
			return nil, err
		}
		promotions = append(promotions, promotion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform: list promotions: %w", err)
	}
	return promotions, nil
}

// promotionEnvironmentOrder returns the promotion's two environment refs in
// the order their rows must be locked: (project_id, ref) byte order.
//
// Byte order rather than source-then-target, and not because the roles are
// unsafe today. They are provably safe: a promotion's source always ranks
// strictly below its target, so no two transactions can ever take the same two
// rows in opposite orders, and a deadlock cycle cannot form. But that proof is
// a property of CanPromote's rule rather than of the locking, and it stops
// holding the moment the rule admits anything that is not strictly
// rank-forward — a sideways move between equally ranked environments, say.
// Byte order is a property of the two rows themselves, so it survives a change
// to the promotion rule, and it costs one comparison.
func promotionEnvironmentOrder(p Promotion) [2]EnvironmentRef {
	first, second := p.Source().Ref, p.Target().Ref
	if second < first {
		first, second = second, first
	}
	return [2]EnvironmentRef{first, second}
}
