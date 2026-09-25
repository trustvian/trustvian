package platform

// Index usage for the four collections.
//
// The whole reason v5 exists is three indexes, so "the query returns the right
// rows" is not the interesting assertion — a sequential scan returns the right
// rows too, and would keep doing so right up until a database had enough rows
// to matter. This asks the planner instead.
//
// PostgreSQL only: EXPLAIN is dialect-specific, and SQLite's planner output is
// a different grammar. SQLite gets the same indexes from the same shared
// statements, and its own collection conformance covers correctness.

import (
	"context"
	"strings"
	"testing"
)

// TestPostgresCollectionsUseTheirIndexes proves the v5 indexes are actually
// chosen, not merely present.
//
// enable_seqscan is switched off for the check rather than relying on row
// counts: a test table is small enough that a sequential scan is genuinely
// cheaper, so the planner would pick one however good the index is. Disabling
// it asks the question this test means to ask — *can* this query be answered
// from an index — and a plan that still scans sequentially means no usable
// index exists.
func TestPostgresCollectionsUseTheirIndexes(t *testing.T) {
	store := newPostgresStore(t)
	ctx := context.Background()

	seedProject(t, store, "proj-1")
	if err := store.CreateAgent(ctx, mustAgent(t, "agent-1", "proj-1", "A")); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := store.CreateCandidate(ctx,
		mustCandidate(t, "cand-1", "agent-1", CandidateMetadata{Label: "v1"})); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	if err := store.CreateEnvironment(ctx,
		mustEnvironmentValue(t, "staging", "proj-1", "Staging")); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	if err := store.CreateEvaluationRun(ctx,
		mustRun(t, "run-1", "cand-1", conformanceEpoch())); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	if _, err := store.pool.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	tests := []struct {
		name  string
		query string
		args  []any
		index string
	}{
		{
			"projects range along the primary key",
			`SELECT id, name FROM ` + tableProjects + `
			 WHERE id > $1 ORDER BY id LIMIT $2`,
			[]any{"", 64},
			// No index of its own: the primary key is the range.
			tableProjects + "_pkey",
		},
		{
			"agents by project",
			`SELECT id, name FROM ` + tableAgents + `
			 WHERE project_id = $1 AND id > $2 ORDER BY id LIMIT $3`,
			[]any{"proj-1", "", 64},
			indexAgentsByProject,
		},
		{
			"candidates by agent",
			`SELECT id FROM ` + tableCandidates + `
			 WHERE agent_id = $1 AND id > $2 ORDER BY id LIMIT $3`,
			[]any{"agent-1", "", 64},
			indexCandidatesByAgent,
		},
		{
			"runs by candidate",
			`SELECT id FROM ` + tableRuns + `
			 WHERE candidate_id = $1 AND id > $2 ORDER BY id LIMIT $3`,
			[]any{"cand-1", "", 64},
			indexRunsByCandidate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := explain(t, store, tt.query, tt.args...)
			if !strings.Contains(plan, tt.index) {
				t.Errorf("the plan does not use %s:\n%s", tt.index, plan)
			}
			if strings.Contains(plan, "Seq Scan") {
				t.Errorf("the plan falls back to a sequential scan:\n%s", plan)
			}
		})
	}
}

// explain returns the query plan as text.
func explain(t *testing.T, store *PostgresStore, query string, args ...any) string {
	t.Helper()
	// query is a compile-time constant assembled from table-name constants in
	// this file; args are bound parameters.
	rows, err := store.pool.Query(context.Background(), `EXPLAIN `+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	return strings.Join(lines, "\n")
}
