package platform

// Recency ordering (task 101, ADR 0063).
//
// ADR 0041 orders every collection by immutable identifier because the stored
// timestamps are RFC3339Nano text, which does not sort: Go trims trailing
// fractional zeros and an offset reorders it again. "Newest first" needs a key
// that sorts, so schema v10 stores one beside each run and each scenario
// execution: the creation (or start) time as a 20-digit, zero-padded count of
// UTC Unix nanoseconds. Byte order and time order are then the same thing on
// both backends, the way observationSequenceKey makes byte and numeric order
// the same for sequences.
//
// The key is derived from the value the row already stores, at write time and
// once by the migration. It is never read back as a time — `created_at` stays
// the time — so the two cannot be made to disagree by anything but corruption,
// which the read path refuses.
//
// A key is not unique: two runs created in the same nanosecond share one. The
// collection's order is therefore (key DESC, id DESC), and its cursor carries
// both, so a page boundary between equal keys neither skips nor repeats a row.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ErrRunScope reports a recency read whose narrowing does not belong to the
// scope above it — an agent of another project, a candidate of another agent.
// Refused rather than widened: answering for the project when the reader
// named an agent that is not in it would be answering a question nobody asked.
var ErrRunScope = errors.New("platform: run scope is inconsistent")

// recencyKeyDigits is the fixed width of a stored recency key: enough for
// every positive int64 nanosecond count.
const recencyKeyDigits = 20

// recencyKey renders a time as its sortable storage key.
//
// A time before the Unix epoch has no key: no run or execution is created
// then, and a negative count would sort as text in the wrong place. Nor does a
// time past what an int64 of nanoseconds holds (2262-04-11): UnixNano is
// undefined there and would wrap to a key that sorts somewhere plausible.
var recencyKeyLimit = time.Unix(0, math.MaxInt64).UTC()

func recencyKey(t time.Time) (string, error) {
	if t.IsZero() || t.Before(time.Unix(0, 0)) || !t.Before(recencyKeyLimit) {
		return "", fmt.Errorf("%w: time %s has no recency key", ErrInvalidID, t.UTC().Format(time.RFC3339Nano))
	}
	nanos := t.UTC().UnixNano()
	if nanos < 0 {
		return "", fmt.Errorf("%w: time %s has no recency key", ErrInvalidID, t.UTC().Format(time.RFC3339Nano))
	}
	return fmt.Sprintf("%0*d", recencyKeyDigits, nanos), nil
}

// validRecencyKey reports whether s is a key this code could have written.
func validRecencyKey(s string) bool {
	if len(s) != recencyKeyDigits {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// RecencyCursor is a position in a newest-first collection: the key and the
// identifier of the last row a reader has seen. Both, because keys repeat.
type RecencyCursor struct {
	Key string
	ID  string
}

// IsZero reports whether the cursor names no position — the first page.
func (c RecencyCursor) IsZero() bool { return c.Key == "" && c.ID == "" }

// FormatRecencyCursor renders a cursor for the wire: the fixed-width key, a
// dot, then the identifier. The key's width is what makes this unambiguous
// whatever the identifier contains, dots included.
func FormatRecencyCursor(c RecencyCursor) string {
	if c.IsZero() {
		return ""
	}
	return c.Key + "." + c.ID
}

// ParseRecencyCursor reads a wire cursor, refusing anything this code would
// not have written. Empty is the first page.
func ParseRecencyCursor(s string) (RecencyCursor, error) {
	if s == "" {
		return RecencyCursor{}, nil
	}
	if len(s) < recencyKeyDigits+2 || s[recencyKeyDigits] != '.' || !validRecencyKey(s[:recencyKeyDigits]) {
		return RecencyCursor{}, fmt.Errorf("%w: %q is not a recency cursor", ErrObservationCursor, preview(s))
	}
	id := s[recencyKeyDigits+1:]
	if err := validateID("recency cursor id", id); err != nil {
		return RecencyCursor{}, fmt.Errorf("%w: %v", ErrObservationCursor, err)
	}
	return RecencyCursor{Key: s[:recencyKeyDigits], ID: id}, nil
}

// ---------------------------------------------------------------------
// Schema v10
// ---------------------------------------------------------------------

// SchemaVersion v10's index names.
const (
	indexRunsRecentByCandidate = "platform_runs_recent_by_candidate"
	indexRunsRecent            = "platform_runs_recent"
	indexScenarioRecent        = "platform_scenario_executions_recent"
)

// schemaVersionV9 is task 078's schema, the last version without recency keys.
// v10 adds columns and indexes only, so v9 and v10 hold the same tables and are
// told apart by the stamp.
const schemaVersionV9 = 9

// schemaVersionV10 is task 101's schema, the last version without task 087's
// per-behavior operational columns. v11 adds columns only, so v10 and v11 hold
// the same tables and are told apart by the stamp.
const schemaVersionV10 = 10

// recencySchemaStatements are v10's whole schema change, in either dialect.
//
// A column with an empty default, filled by recencyBackfill in the same
// transaction: SQLite cannot add a NOT NULL column without a default, and the
// default is never left in place — the read path refuses an empty key as
// corruption rather than sorting it last.
//
// Three indexes, one per question: a candidate's runs newest first; any set of
// runs newest first, which the project and agent scopes walk through their
// join; and a project's executions newest first.
func recencySchemaStatements(textType string) []string {
	return []string{
		`ALTER TABLE ` + tableRuns + ` ADD COLUMN created_order ` + textType + ` NOT NULL DEFAULT ''`,
		`ALTER TABLE ` + tableScenarioExecutions + ` ADD COLUMN started_order ` + textType + ` NOT NULL DEFAULT ''`,
		`CREATE INDEX ` + indexRunsRecentByCandidate + ` ON ` + tableRuns + ` (candidate_id, created_order, id)`,
		`CREATE INDEX ` + indexRunsRecent + ` ON ` + tableRuns + ` (created_order, id)`,
		`CREATE INDEX ` + indexScenarioRecent + ` ON ` + tableScenarioExecutions + ` (project_id, started_order, id)`,
	}
}

// recencyBackfill derives every existing row's key from the time it already
// stores. It reads the identifiers and times first and writes afterwards, so
// no backend has to update a table it is still iterating.
func recencyBackfill(
	ctx context.Context, q evidenceQuerier, exec func(ctx context.Context, query string, args ...any) error,
) error {
	for _, target := range []struct {
		table, timeColumn, keyColumn, field string
	}{
		{tableRuns, "created_at", "created_order", "evaluation run created_at"},
		{tableScenarioExecutions, "started_at", "started_order", "scenario execution started_at"},
	} {
		rows, err := q.query(ctx, `SELECT id, `+target.timeColumn+` FROM `+target.table)
		if err != nil {
			return fmt.Errorf("platform: backfill recency keys: %w", err)
		}
		type pending struct{ id, key string }
		var updates []pending
		for rows.Next() {
			var id, at string
			if err := rows.Scan(&id, &at); err != nil {
				rows.Close()
				return fmt.Errorf("platform: backfill recency keys: %w", err)
			}
			parsed, err := parseTimeText(target.field, at)
			if err != nil {
				rows.Close()
				return err
			}
			key, err := recencyKey(parsed)
			if err != nil {
				rows.Close()
				return fmt.Errorf("%w: %s %s: %v", ErrStoreCorrupt, target.field, preview(id), err)
			}
			updates = append(updates, pending{id: id, key: key})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("platform: backfill recency keys: %w", err)
		}
		rows.Close()
		for _, u := range updates {
			if err := exec(ctx, q.rebind(
				`UPDATE `+target.table+` SET `+target.keyColumn+` = ? WHERE id = ?`), u.key, u.id); err != nil {
				return fmt.Errorf("platform: backfill recency keys: %w", err)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------
// Recent runs
// ---------------------------------------------------------------------

// RunScope is the narrowest level a recency read is asked about. Exactly one
// of the three identifiers selects the predicate; the control plane resolves
// the reader's project/agent/candidate into it after checking they agree.
type RunScope struct {
	ProjectID   ProjectID
	AgentID     AgentID
	CandidateID CandidateID
}

// RecentRunStore reads runs newest first (task 101). A capability of its own,
// like TraceSummaryStore: it joins control-plane rows to evaluation rows, which
// a backend implementing only one of the two capabilities could not do.
type RecentRunStore interface {
	// RecentEvaluationRuns returns up to limit runs in the scope, ordered by
	// (creation key DESC, id DESC), strictly after the cursor. The narrowest
	// non-empty identifier in scope decides the predicate.
	RecentEvaluationRuns(
		ctx context.Context, scope RunScope, after RecencyCursor, limit int,
	) ([]RecentRun, error)
}

// RecentRun is one listed run and the key it is ordered by, which the caller
// needs to form the next cursor.
type RecentRun struct {
	Run EvaluationRun
	Key string
}

// queryRecentRuns is the one statement both backends run.
//
// Built from constant fragments; every value is a bound parameter. The keyset
// predicate is the row comparison spelled out, `key < k OR (key = k AND id <
// i)`, because SQLite and PostgreSQL agree on that spelling and the tuple
// form's support differs.
func queryRecentRuns(
	ctx context.Context, q evidenceQuerier, scope RunScope, after RecencyCursor, limit int,
) ([]RecentRun, error) {
	var query strings.Builder
	args := make([]any, 0, 5)
	query.WriteString(`SELECT r.id, r.candidate_id, r.environment, r.behavioral_profile, r.status,
	        r.created_at, r.started_at, r.finished_at, r.failure_reason, r.created_order
	   FROM ` + tableRuns + ` r`)
	switch {
	case scope.CandidateID != "":
		query.WriteString(` WHERE r.candidate_id = ?`)
		args = append(args, string(scope.CandidateID))
	case scope.AgentID != "":
		query.WriteString(` JOIN ` + tableCandidates + ` c ON c.id = r.candidate_id
		  WHERE c.agent_id = ?`)
		args = append(args, string(scope.AgentID))
	case scope.ProjectID != "":
		query.WriteString(` JOIN ` + tableCandidates + ` c ON c.id = r.candidate_id
		  JOIN ` + tableAgents + ` a ON a.id = c.agent_id
		  WHERE a.project_id = ?`)
		args = append(args, string(scope.ProjectID))
	default:
		return nil, fmt.Errorf("%w: a recency read names no scope", ErrRunScope)
	}
	if !after.IsZero() {
		query.WriteString(` AND (r.created_order < ? OR (r.created_order = ? AND r.id < ?))`)
		args = append(args, after.Key, after.Key, after.ID)
	}
	query.WriteString(` ORDER BY r.created_order DESC, r.id DESC LIMIT ?`)
	args = append(args, limit)

	rows, err := q.query(ctx, q.rebind(query.String()), args...)
	if err != nil {
		return nil, fmt.Errorf("platform: list recent runs: %w", err)
	}
	defer rows.Close()

	out := make([]RecentRun, 0, limit)
	for rows.Next() {
		var id, candidateID, environment, profile, status, createdAt, failureReason, key string
		var startedAt, finishedAt sql.NullString
		if err := rows.Scan(&id, &candidateID, &environment, &profile, &status,
			&createdAt, &startedAt, &finishedAt, &failureReason, &key); err != nil {
			return nil, fmt.Errorf("platform: list recent runs: %w", err)
		}
		if !validRecencyKey(key) {
			return nil, fmt.Errorf("%w: evaluation run %s has recency key %q",
				ErrStoreCorrupt, preview(id), preview(key))
		}
		run, err := rehydrateRun(EvaluationRunID(id), storedRun{
			candidateID:   CandidateID(candidateID),
			environment:   EnvironmentRef(environment),
			profile:       BehavioralProfileRef(profile),
			status:        RunStatus(status),
			createdAt:     createdAt,
			startedAt:     startedAt,
			finishedAt:    finishedAt,
			failureReason: failureReason,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, RecentRun{Run: run, Key: key})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform: list recent runs: %w", err)
	}
	return out, nil
}

// RecentEvaluationRuns lists runs newest first within an explicit scope.
//
// The project is required and must exist. An agent, when named, must belong to
// the project; a candidate, when named, must belong to the named agent — or,
// with no agent named, to some agent of the project. A narrowing that does not
// fit is ErrRunScope; a narrowing that does not exist is ErrStoreNotFound.
func (c *ControlPlane) RecentEvaluationRuns(
	ctx context.Context, scope RunScope, after string, limit int,
) ([]RecentRun, error) {
	if err := validateID("recent runs project id", string(scope.ProjectID)); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxListPage {
		return nil, fmt.Errorf("%w: recent runs limit %d is outside 1..%d", ErrInvalidID, limit, MaxListPage)
	}
	cursor, err := ParseRecencyCursor(after)
	if err != nil {
		return nil, err
	}
	if _, err := c.control.Project(ctx, scope.ProjectID); err != nil {
		return nil, err
	}
	if scope.AgentID != "" {
		agent, err := c.control.Agent(ctx, scope.AgentID)
		if err != nil {
			return nil, err
		}
		if agent.ProjectID() != scope.ProjectID {
			return nil, fmt.Errorf("%w: agent %s belongs to project %s, not %s", ErrRunScope,
				preview(string(scope.AgentID)), preview(string(agent.ProjectID())), preview(string(scope.ProjectID)))
		}
	}
	if scope.CandidateID != "" {
		candidate, err := c.control.Candidate(ctx, scope.CandidateID)
		if err != nil {
			return nil, err
		}
		if scope.AgentID != "" && candidate.AgentID() != scope.AgentID {
			return nil, fmt.Errorf("%w: candidate %s belongs to agent %s, not %s", ErrRunScope,
				preview(string(scope.CandidateID)), preview(string(candidate.AgentID())), preview(string(scope.AgentID)))
		}
		if scope.AgentID == "" {
			agent, err := c.control.Agent(ctx, candidate.AgentID())
			if err != nil {
				return nil, err
			}
			if agent.ProjectID() != scope.ProjectID {
				return nil, fmt.Errorf("%w: candidate %s belongs to project %s, not %s", ErrRunScope,
					preview(string(scope.CandidateID)), preview(string(agent.ProjectID())), preview(string(scope.ProjectID)))
			}
		}
	}
	store, ok := c.evaluations.(RecentRunStore)
	if !ok {
		return nil, fmt.Errorf("%w: this store cannot order runs by recency", ErrStoreSchemaVersion)
	}
	return store.RecentEvaluationRuns(ctx, scope, cursor, limit)
}
