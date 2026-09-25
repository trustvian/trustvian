package platform

// The backend-shared reads behind task 074's four hierarchy collections.
//
// One definition per collection, used by both backends through the querier
// seam, for the reason querier.go already gives: a second copy of a column
// list and its scan targets is where SQLite and PostgreSQL silently drift
// apart. A differential test finds drift after it happens; sharing the
// definition means there is nothing to drift.
//
// Every query here is an indexed bounded range scan with the limit in the SQL.
// Nothing loads a table and slices it in Go — that is the shape that works on
// a developer's laptop and falls over on the first real database.

import (
	"context"
	"database/sql"
	"fmt"
)

// queryProjectPage reads one bounded page of projects in id byte order.
//
// The one unscoped collection, and the root of the hierarchy: without it there
// is no entry point that does not require prior knowledge of an identifier.
// It needs no index — `WHERE id > ? ORDER BY id LIMIT ?` is a range scan along
// the primary key on both backends.
func queryProjectPage(
	ctx context.Context, q evidenceQuerier, after ProjectID, limit int,
) ([]Project, error) {
	rows, err := q.query(ctx, q.rebind(
		`SELECT id, name FROM `+tableProjects+`
		 WHERE id > ?
		 ORDER BY id
		 LIMIT ?`),
		string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("platform: list projects: %w", err)
	}
	defer rows.Close()

	projects := make([]Project, 0, limit)
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("platform: list projects: %w", err)
		}
		// Through the domain constructor, like every other restore path: a
		// stored row faces the validation a live value did rather than being
		// trusted because it is stored.
		project, err := NewProject(ProjectID(id), name)
		if err != nil {
			return nil, fmt.Errorf("%w: project %s: %w", ErrStoreCorrupt, preview(id), err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform: list projects: %w", err)
	}
	return projects, nil
}

// queryAgentPage reads one bounded page of a project's agents.
func queryAgentPage(
	ctx context.Context, q evidenceQuerier, projectID ProjectID, after AgentID, limit int,
) ([]Agent, error) {
	rows, err := q.query(ctx, q.rebind(
		`SELECT id, name FROM `+tableAgents+`
		 WHERE project_id = ? AND id > ?
		 ORDER BY id
		 LIMIT ?`),
		string(projectID), string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("platform: list agents: %w", err)
	}
	defer rows.Close()

	agents := make([]Agent, 0, limit)
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("platform: list agents: %w", err)
		}
		agent, err := NewAgent(AgentID(id), projectID, name)
		if err != nil {
			return nil, fmt.Errorf("%w: agent %s: %w", ErrStoreCorrupt, preview(id), err)
		}
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform: list agents: %w", err)
	}
	return agents, nil
}

// queryCandidatePage reads one bounded page of an agent's candidates.
func queryCandidatePage(
	ctx context.Context, q evidenceQuerier, agentID AgentID, after CandidateID, limit int,
) ([]Candidate, error) {
	rows, err := q.query(ctx, q.rebind(
		`SELECT id, label, source_ref, artifact_digest, model, toolset_digest, config_digest
		 FROM `+tableCandidates+`
		 WHERE agent_id = ? AND id > ?
		 ORDER BY id
		 LIMIT ?`),
		string(agentID), string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("platform: list candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]Candidate, 0, limit)
	for rows.Next() {
		var id string
		var m CandidateMetadata
		if err := rows.Scan(&id, &m.Label, &m.SourceRef, &m.ArtifactDigest,
			&m.Model, &m.ToolsetDigest, &m.ConfigDigest); err != nil {
			return nil, fmt.Errorf("platform: list candidates: %w", err)
		}
		candidate, err := NewCandidate(CandidateID(id), agentID, m)
		if err != nil {
			return nil, fmt.Errorf("%w: candidate %s: %w", ErrStoreCorrupt, preview(id), err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform: list candidates: %w", err)
	}
	return candidates, nil
}

// queryRunPage reads one bounded page of a candidate's evaluation runs.
//
// Restored through rehydrateRun, the same replay loadRun uses, so a listed run
// faces every lifecycle invariant a singly-read one does. A corrupt row in a
// page is ErrStoreCorrupt rather than a silently skipped entry: a listing that
// quietly omitted a damaged run would be the worst possible way to learn about
// it.
func queryRunPage(
	ctx context.Context, q evidenceQuerier, candidateID CandidateID, after EvaluationRunID, limit int,
) ([]EvaluationRun, error) {
	rows, err := q.query(ctx, q.rebind(
		`SELECT id, environment, behavioral_profile, status,
		        created_at, started_at, finished_at, failure_reason
		 FROM `+tableRuns+`
		 WHERE candidate_id = ? AND id > ?
		 ORDER BY id
		 LIMIT ?`),
		string(candidateID), string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("platform: list evaluation runs: %w", err)
	}
	defer rows.Close()

	runs := make([]EvaluationRun, 0, limit)
	for rows.Next() {
		var id, environment, profile, status, createdAt, failureReason string
		var startedAt, finishedAt sql.NullString
		if err := rows.Scan(&id, &environment, &profile, &status,
			&createdAt, &startedAt, &finishedAt, &failureReason); err != nil {
			return nil, fmt.Errorf("platform: list evaluation runs: %w", err)
		}
		run, err := rehydrateRun(EvaluationRunID(id), storedRun{
			candidateID:   candidateID,
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
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform: list evaluation runs: %w", err)
	}
	return runs, nil
}
