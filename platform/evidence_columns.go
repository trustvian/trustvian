package platform

// The evaluation aggregate's column list, once.
//
// Forty-four columns, and before Task 064 they were written out three times in
// one file: a SELECT list, an INSERT list, and an ON CONFLICT assignment block.
// A second backend written the obvious way copies all three, and a column added
// to five of those six places is a bug no test is looking for — the values land
// in the wrong columns, or one backend quietly stops persisting a counter.
//
// So the order lives here and both backends derive everything from it. The
// dialects still differ in placeholder syntax and upsert spelling, which stays
// in each backend's own file where it is visible.

import "strings"

// aggregateInsertColumns is the authoritative column order.
//
// run_id is first because both backends' upsert needs to treat it separately:
// it is the conflict target and must not appear in the assignment list.
func aggregateInsertColumns() []string {
	columns := []string{
		"run_id", "candidate_id", "environment", "behavioral_profile",
		"record_count", "first_observed_at", "last_observed_at",

		"decision_allow", "decision_observe_only", "decision_alert",
		"decision_challenge", "decision_require_approval", "decision_block",

		"risk_low", "risk_medium", "risk_high", "risk_critical",

		"approval_unspecified", "approval_not_required", "approval_required",
		"approval_approved", "approval_denied",

		"policy_matched_rule", "policy_matched_default",
	}
	// The five metric summaries, each contributing count, sum, min and max in
	// that order. Generated rather than listed so the loop in
	// aggregateInsertArgs cannot fall out of step with the names.
	for _, metric := range aggregateMetricPrefixes() {
		columns = append(columns,
			metric+"_count", metric+"_sum", metric+"_min", metric+"_max")
	}
	// Schema 6, task 084. Appended last so every earlier column keeps its
	// position: both backends bind positionally against this order, and
	// inserting in the middle would silently shift every metric one place.
	return append(columns, aggregateOperationalColumns()...)
}

// aggregateOperationalColumns names the schema-6 operational counters, in
// storage order.
//
// Declared separately because the v5-to-v6 migration adds exactly these and
// nothing else, and deriving the ALTER TABLE from the same list is what keeps a
// migrated database column-for-column identical to a freshly created one.
func aggregateOperationalColumns() []string {
	return []string{
		"duration_count", "duration_unobserved",
		"duration_sum", "duration_min", "duration_max",
		"span_status_unavailable", "span_status_unset",
		"span_status_ok", "span_status_error",
	}
}

// aggregateMetricPrefixes names the five metric summaries in storage order.
//
// The same order loadAggregate reconstructs them in, and the same order
// aggregateInsertArgs appends them in.
func aggregateMetricPrefixes() []string {
	return []string{
		"identity_confidence", "anomaly_score", "anomaly_confidence",
		"trust_score", "context_risk",
	}
}

// aggregateInsertArgs renders one aggregate as bind arguments.
//
// Positionally identical to aggregateInsertColumns. Counters are canonical
// decimal text and timestamps are RFC3339Nano text on both backends, for the
// reasons uint64Text and timeText give: a signed 64-bit column would corrupt
// large counters, and a native timestamp type would rewrite the caller's zone
// offset and truncate nanoseconds.
func aggregateInsertArgs(a EvaluationAggregate) []any {
	args := []any{
		string(a.RunID()), string(a.CandidateID()), string(a.Environment()),
		string(a.BehavioralProfile()),
		uint64Text(a.RecordCount()),
		nullTimeText(a.FirstObservedAt()), nullTimeText(a.LastObservedAt()),

		uint64Text(a.Decisions().Allow), uint64Text(a.Decisions().ObserveOnly),
		uint64Text(a.Decisions().Alert), uint64Text(a.Decisions().Challenge),
		uint64Text(a.Decisions().RequireApproval), uint64Text(a.Decisions().Block),

		uint64Text(a.Risks().Low), uint64Text(a.Risks().Medium),
		uint64Text(a.Risks().High), uint64Text(a.Risks().Critical),

		uint64Text(a.Approvals().Unspecified), uint64Text(a.Approvals().NotRequired),
		uint64Text(a.Approvals().Required), uint64Text(a.Approvals().Approved),
		uint64Text(a.Approvals().Denied),

		uint64Text(a.PolicySelection().MatchedRule),
		uint64Text(a.PolicySelection().MatchedDefault),
	}
	for _, m := range aggregateMetrics(a) {
		args = append(args, uint64Text(m.Count), m.Sum, m.Min, m.Max)
	}
	// Schema 6, task 084. Canonical decimal text like every other counter:
	// a nanosecond sum exceeds what a signed 64-bit column holds.
	d, st := a.Durations(), a.SpanStatuses()
	return append(args,
		uint64Text(d.Count), uint64Text(d.Unobserved),
		uint64Text(d.Sum), uint64Text(d.Min), uint64Text(d.Max),
		uint64Text(st.Unavailable), uint64Text(st.Unset),
		uint64Text(st.OK), uint64Text(st.Error))
}

// aggregateMetrics returns the five summaries in storage order.
func aggregateMetrics(a EvaluationAggregate) []MetricSummary {
	return []MetricSummary{
		a.IdentityConfidence(), a.AnomalyScore(), a.AnomalyConfidence(),
		a.TrustScore(), a.ContextRisk(),
	}
}

// aggregateSelectList renders the column list a restore reads, run_id excluded
// because the caller already knows it and passes it as the predicate.
func aggregateSelectList() string {
	return strings.Join(aggregateInsertColumns()[1:], ", ")
}

// operationalColumnDDL renders the schema-6 column definitions.
//
// One definition per column, used by both the CREATE TABLE in each backend and
// by each backend's ALTER TABLE, so a migrated database and a freshly created
// one hold column-for-column identical tables. `textType` differs only because
// PostgreSQL needs an explicit C collation for the byte-ordered text columns
// this schema compares.
//
// DEFAULT '0' is what makes ADD COLUMN work on a table that already has rows.
// The migration then *replaces* those defaults for existing rows — see
// operationalBackfillStatement, and task 084 for why zero would be the wrong
// resting value.
func operationalColumnDDL(textType string) []string {
	columns := aggregateOperationalColumns()
	out := make([]string, 0, len(columns))
	for _, name := range columns {
		out = append(out, name+" "+textType+" NOT NULL DEFAULT '0'")
	}
	return out
}

// operationalBackfillStatement makes an existing run say *unknown* rather than
// *zero*.
//
// A row written before schema 6 observed no duration and no status for any of
// its records, because the fields did not exist. Leaving the DEFAULT '0' would
// claim the opposite of that: a run whose every observation was instantaneous
// and unstatused. Setting the two "nothing was observed" counters to
// record_count states what actually happened, and preserves the invariants
// Count+Unobserved == RecordCount and Unavailable+Unset+OK+Error == RecordCount
// that make a migrated run internally consistent.
func operationalBackfillStatement(table string) string {
	return `UPDATE ` + table + `
	           SET duration_unobserved = record_count,
	               span_status_unavailable = record_count`
}
