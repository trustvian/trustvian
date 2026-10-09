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

import (
	"fmt"
	"strconv"
	"strings"
)

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

// ---------------------------------------------------------------------
// Schema 11: per-behavior operational evidence (task 087)
// ---------------------------------------------------------------------

// columnOperationalCounts is schema 11's one column on the behavior entry
// table: the entry's thirty-one operational counters as canonical decimal text,
// comma-separated, in operationalCounterNames order.
//
// One column rather than thirty-one, measured. Every ingest rewrites the run's
// entries and reads them back three times, and with a column per counter the
// SQLite driver's per-column decode doubled the cost of an ingest and
// multiplied its allocations by 2.6 (task 087's What shipped has the numbers).
// Nothing reads these counters in SQL — the control plane sums them in Go — so
// a column each bought no query and cost every ingest. Each counter is still
// canonical decimal text like every other counter, and each is validated on
// restore exactly as before.
const columnOperationalCounts = "operational_counts"

// operationalCounterCount is how many counters operational_counts holds.
const operationalCounterCount = 31

// operationalCounterNames names the counters in storage order: the eleven
// duration buckets, 084's duration statistics and span statuses, HTTP status
// classes and 429s, then tokens. The observed duration count is not stored: it
// is the bucket total, and storing it twice would be a second value to keep in
// step. Used for the storage order and for naming a corrupt counter.
var operationalCounterNames = func() [operationalCounterCount]string {
	names := make([]string, 0, operationalCounterCount)
	for _, upper := range durationBucketUpperMillis {
		names = append(names, "duration_le_"+strconv.FormatUint(upper, 10)+"ms")
	}
	names = append(names,
		"duration_gt_"+strconv.FormatUint(durationBucketUpperMillis[len(durationBucketUpperMillis)-1], 10)+"ms",
		"duration_unobserved", "duration_sum", "duration_min", "duration_max",
		"span_status_unavailable", "span_status_unset", "span_status_ok", "span_status_error",
		"http_1xx", "http_2xx", "http_3xx", "http_4xx", "http_5xx", "http_status_unavailable",
		"http_429",
		"tokens_input", "tokens_output", "tokens_unsplit", "tokens_observed", "tokens_unobserved",
	)
	if len(names) != operationalCounterCount {
		panic("platform: schema 11 counter list is not thirty-one counters")
	}
	var out [operationalCounterCount]string
	copy(out[:], names)
	return out
}()

// operationalCounters is a summary's counters in storage order.
func operationalCounters(s OperationalSummary) [operationalCounterCount]uint64 {
	var c [operationalCounterCount]uint64
	copy(c[:DurationBucketCount], s.Buckets[:])
	copy(c[DurationBucketCount:], []uint64{
		s.Durations.Unobserved, s.Durations.Sum, s.Durations.Min, s.Durations.Max,
		s.SpanStatus.Unavailable, s.SpanStatus.Unset, s.SpanStatus.OK, s.SpanStatus.Error,
		s.HTTPStatus.Informational, s.HTTPStatus.Success, s.HTTPStatus.Redirection,
		s.HTTPStatus.ClientError, s.HTTPStatus.ServerError, s.HTTPStatus.Unavailable,
		s.HTTP429,
		s.Tokens.Input, s.Tokens.Output, s.Tokens.Unsplit, s.Tokens.Observed, s.Tokens.Unobserved,
	})
	return c
}

// encodeOperationalCounts renders a summary as operational_counts stores it.
func encodeOperationalCounts(s OperationalSummary) string {
	counters := operationalCounters(s)
	buf := make([]byte, 0, 2*operationalCounterCount)
	for i, n := range counters {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendUint(buf, n, 10)
	}
	return string(buf)
}

// decodeOperationalCounts parses operational_counts back into a summary. Every
// counter must be canonical decimal text, exactly thirty-one of them; anything
// else is corruption. Whether the summary is consistent is
// validateOperationalSummary's. Allocation-free on valid input: it runs for
// every entry on every ingest.
func decodeOperationalCounts(text string) (OperationalSummary, error) {
	var values [operationalCounterCount]uint64
	rest := text
	for i := range values {
		field, tail, more := strings.Cut(rest, ",")
		if more != (i < operationalCounterCount-1) {
			return OperationalSummary{}, fmt.Errorf("%w: entry %s holds the wrong number of counters",
				ErrStoreCorrupt, columnOperationalCounts)
		}
		v, ok := parseCanonicalUint64(field)
		if !ok {
			return OperationalSummary{}, fmt.Errorf("%w: entry %s is not a canonical uint64: %q",
				ErrStoreCorrupt, operationalCounterNames[i], preview(field))
		}
		values[i] = v
		rest = tail
	}
	var s OperationalSummary
	copy(s.Buckets[:], values[:DurationBucketCount])
	r := values[DurationBucketCount:]
	count, err := sumNoOverflow(s.Buckets[:])
	if err != nil {
		return OperationalSummary{}, fmt.Errorf("%w: entry duration buckets overflow", ErrStoreCorrupt)
	}
	s.Durations = DurationSummary{Count: count, Unobserved: r[0], Sum: r[1], Min: r[2], Max: r[3]}
	s.SpanStatus = SpanStatusCounts{Unavailable: r[4], Unset: r[5], OK: r[6], Error: r[7]}
	s.HTTPStatus = HTTPStatusClassCounts{
		Informational: r[8], Success: r[9], Redirection: r[10],
		ClientError: r[11], ServerError: r[12], Unavailable: r[13],
	}
	s.HTTP429 = r[14]
	s.Tokens = TokenCounts{Input: r[15], Output: r[16], Unsplit: r[17], Observed: r[18], Unobserved: r[19]}
	return s, nil
}

// parseCanonicalUint64 accepts exactly what uint64Text writes — decimal
// digits, no sign, no leading zero — without allocating.
func parseCanonicalUint64(s string) (uint64, bool) {
	if s == "" || s[0] < '0' || s[0] > '9' || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// behaviorOperationalSchemaStatements are schema 11's whole change, in either
// dialect, shipped on a fresh database too so a fresh and a migrated table are
// column-for-column identical (the v8 and v10 precedent). The empty default is
// what lets ADD COLUMN work on a table with rows; it is never left in place —
// the migration backfills every existing row, every write states the column,
// and the read path refuses an empty value as corruption.
func behaviorOperationalSchemaStatements(textType string) []string {
	return []string{
		`ALTER TABLE ` + tableEntries + ` ADD COLUMN ` + columnOperationalCounts + ` ` + textType + ` NOT NULL DEFAULT ''`,
	}
}

// behaviorOperationalBackfillStatement makes a behavior recorded before schema
// 11 say *not recorded* rather than *zero*: every "nothing was stated" counter
// — unobserved durations, unavailable span status and HTTP status, unobserved
// tokens — is the entry's observations, and every other counter is 0. That is
// the truth, since those fields did not exist when it was observed, and it
// satisfies every partition invariant. Spelled with `||`, which both dialects
// share. Applied by the migration only.
func behaviorOperationalBackfillStatement() string {
	var b strings.Builder
	b.WriteString(`UPDATE ` + tableEntries + ` SET ` + columnOperationalCounts + ` = '`)
	unavailable := map[string]bool{
		"duration_unobserved": true, "span_status_unavailable": true,
		"http_status_unavailable": true, "tokens_unobserved": true,
	}
	for i, name := range operationalCounterNames {
		if i > 0 {
			b.WriteString(",")
		}
		if unavailable[name] {
			b.WriteString("' || observations || '")
		} else {
			b.WriteString("0")
		}
	}
	b.WriteString("'")
	return b.String()
}

// The entry INSERT in each dialect, built once.
var (
	sqliteBehaviorEntryInsert   = behaviorEntryInsertStatement(func(int) string { return "?" })
	postgresBehaviorEntryInsert = behaviorEntryInsertStatement(func(i int) string { return "$" + strconv.Itoa(i) })
)

// behaviorEntryInsertStatement is the one INSERT both backends run for an
// entry, its placeholders spelled by the dialect (placeholder(i) for the i-th,
// from 1).
func behaviorEntryInsertStatement(placeholder func(int) string) string {
	columns := []string{
		"run_id", "fingerprint_id", "actor_type", "operation_category", "operation_name",
		"target_name", "target_category", "environment", "observations", columnOperationalCounts,
		columnFidelityCounts,
	}
	marks := make([]string, len(columns))
	for i := range marks {
		marks[i] = placeholder(i + 1)
	}
	return `INSERT INTO ` + tableEntries + ` (` + strings.Join(columns, ", ") +
		`) VALUES (` + strings.Join(marks, ", ") + `)`
}

// behaviorEntryInsertArgs renders one entry in behaviorEntryInsertStatement's
// order.
func behaviorEntryInsertArgs(runID EvaluationRunID, entry BehaviorEntry) []any {
	b := entry.Behavior
	return []any{
		string(runID), entry.FingerprintID,
		string(b.ActorType), string(b.OperationCategory), b.OperationName,
		b.TargetName, string(b.TargetCategory), b.Environment,
		uint64Text(entry.Observations), encodeOperationalCounts(entry.Operational),
		encodeFidelityCounts(entry.Fidelity),
	}
}
