package platform

// Discovering recorded scenario executions (task 102).
//
// `trustvian eval run --reference <execution>` reuses a recorded reference
// side, and until now the only way to learn an execution's identifier was a CI
// log. This lists a project's executions newest first — by schema v10's start
// key (ADR 0063) — and answers, for one execution, whether the control plane
// would accept it as a reference. That answer is the existing validation,
// called unchanged: nothing here restates a rule `usableReference` owns.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ScenarioExecutionFilter narrows a project's executions by equality. Empty
// fields constrain nothing; the project is required.
type ScenarioExecutionFilter struct {
	ProjectID    ProjectID
	AgentID      AgentID
	Environment  EnvironmentRef
	ScenarioName string
}

func (f ScenarioExecutionFilter) validate() error {
	if err := validateID("scenario execution project id", string(f.ProjectID)); err != nil {
		return err
	}
	if f.AgentID != "" {
		if err := validateID("scenario execution agent id", string(f.AgentID)); err != nil {
			return err
		}
	}
	if f.Environment != "" {
		if err := validateID("scenario execution environment", string(f.Environment)); err != nil {
			return err
		}
	}
	if f.ScenarioName != "" {
		if err := validateID("scenario name", f.ScenarioName); err != nil {
			return err
		}
	}
	return nil
}

// RecentScenarioExecution is one listed execution — without its run
// associations, which the by-id read returns — and its ordering key.
type RecentScenarioExecution struct {
	Execution ScenarioExecution
	Key       string
}

// ScenarioExecutionListStore lists executions newest first. A capability of
// its own beside ScenarioExecutionStore, which writes them.
type ScenarioExecutionListStore interface {
	RecentScenarioExecutions(
		ctx context.Context, filter ScenarioExecutionFilter, after RecencyCursor, limit int,
	) ([]RecentScenarioExecution, error)
}

// queryRecentScenarioExecutions is the one statement both backends run.
// Constant fragments and bound values only, ordered (key DESC, id DESC).
func queryRecentScenarioExecutions(
	ctx context.Context, q evidenceQuerier, filter ScenarioExecutionFilter, after RecencyCursor, limit int,
) ([]RecentScenarioExecution, error) {
	var query strings.Builder
	args := make([]any, 0, 8)
	query.WriteString(`SELECT id, scenario_name, project_id, agent_id, environment, runs,
	        reference_execution_id, status, started_at, finished_at,
	        completion_sequence, verdict, started_order
	   FROM ` + tableScenarioExecutions + `
	  WHERE project_id = ?`)
	args = append(args, string(filter.ProjectID))
	if filter.AgentID != "" {
		query.WriteString(` AND agent_id = ?`)
		args = append(args, string(filter.AgentID))
	}
	if filter.Environment != "" {
		query.WriteString(` AND environment = ?`)
		args = append(args, string(filter.Environment))
	}
	if filter.ScenarioName != "" {
		query.WriteString(` AND scenario_name = ?`)
		args = append(args, filter.ScenarioName)
	}
	if !after.IsZero() {
		query.WriteString(` AND (started_order < ? OR (started_order = ? AND id < ?))`)
		args = append(args, after.Key, after.Key, after.ID)
	}
	query.WriteString(` ORDER BY started_order DESC, id DESC LIMIT ?`)
	args = append(args, limit)

	rows, err := q.query(ctx, q.rebind(query.String()), args...)
	if err != nil {
		return nil, fmt.Errorf("platform: list scenario executions: %w", err)
	}
	defer rows.Close()

	out := make([]RecentScenarioExecution, 0, limit)
	for rows.Next() {
		var (
			id, name, project, agent, environment, status, startedAt, key string
			runs                                                          int64
			reference, finishedAt, verdict                                sql.NullString
			sequence                                                      sql.NullInt64
		)
		if err := rows.Scan(&id, &name, &project, &agent, &environment, &runs, &reference,
			&status, &startedAt, &finishedAt, &sequence, &verdict, &key); err != nil {
			return nil, fmt.Errorf("platform: list scenario executions: %w", err)
		}
		if !validRecencyKey(key) {
			return nil, fmt.Errorf("%w: scenario execution %s has recency key %q",
				ErrStoreCorrupt, preview(id), preview(key))
		}
		started, err := parseTimeText("scenario execution started_at", startedAt)
		if err != nil {
			return nil, err
		}
		finished, err := parseNullTimeText("scenario execution finished_at", finishedAt)
		if err != nil {
			return nil, err
		}
		if runs < 1 || runs > MaxRepetitions || !ScenarioExecutionStatus(status).valid() {
			return nil, fmt.Errorf("%w: scenario execution %s has runs %d, status %q",
				ErrStoreCorrupt, preview(id), runs, preview(status))
		}
		if sequence.Valid && sequence.Int64 <= 0 {
			return nil, fmt.Errorf("%w: scenario execution %s has completion sequence %d",
				ErrStoreCorrupt, preview(id), sequence.Int64)
		}
		out = append(out, RecentScenarioExecution{
			Execution: ScenarioExecution{
				id: ScenarioExecutionID(id), scenarioName: name,
				scope: ScenarioScope{ProjectID: ProjectID(project), AgentID: AgentID(agent),
					Environment: EnvironmentRef(environment)},
				runs: int(runs), reference: ScenarioExecutionID(reference.String),
				status: ScenarioExecutionStatus(status), startedAt: started, finishedAt: finished,
				completionSequence: uint64(sequence.Int64), verdict: GateVerdict(verdict.String),
			},
			Key: key,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform: list scenario executions: %w", err)
	}
	return out, nil
}

// RecentScenarioExecutions lists a project's executions newest first by start
// time, optionally narrowed by agent, environment and scenario name.
//
// The project is not required to exist as a control-plane row: an execution
// records its scope before its first repetition provisions anything, so an
// unknown project is an empty page rather than a 404 — the same reading
// LatestCompletedScenarioExecution gives it.
func (c *ControlPlane) RecentScenarioExecutions(
	ctx context.Context, filter ScenarioExecutionFilter, after string, limit int,
) ([]RecentScenarioExecution, error) {
	if err := filter.validate(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxListPage {
		return nil, fmt.Errorf("%w: scenario execution limit %d is outside 1..%d", ErrInvalidID, limit, MaxListPage)
	}
	cursor, err := ParseRecencyCursor(after)
	if err != nil {
		return nil, err
	}
	store, err := c.scenarioExecutions()
	if err != nil {
		return nil, err
	}
	lister, ok := store.(ScenarioExecutionListStore)
	if !ok {
		return nil, ErrScenarioExecutionsUnsupported
	}
	return lister.RecentScenarioExecutions(ctx, filter, cursor, limit)
}

// ScenarioReferenceCheck is the control plane's answer to "may this execution
// be reused as a reference?", for a scenario with the execution's own
// repetition count in the execution's own project and environment — the only
// scenario a browser can name without the scenario file.
type ScenarioReferenceCheck struct {
	Execution ScenarioExecution
	Usable    bool
	// Reason is the validation's own message when not usable.
	Reason string
}

// CheckScenarioReference runs usableReference, unchanged, against the
// execution's own repetition count and scope.
//
// What it cannot check is stated by its shape: a scenario with a different
// `runs`, or run in another project or environment, would be refused by the
// same validation on a rule this call holds fixed. The caller says so.
func (c *ControlPlane) CheckScenarioReference(
	ctx context.Context, id ScenarioExecutionID,
) (ScenarioReferenceCheck, error) {
	execution, err := c.ScenarioExecution(ctx, id)
	if err != nil {
		return ScenarioReferenceCheck{}, err
	}
	_, err = c.usableReference(ctx, execution, execution.Runs(), execution.Scope())
	switch {
	case err == nil:
		return ScenarioReferenceCheck{Execution: execution, Usable: true}, nil
	case errors.Is(err, ErrScenarioReference), errors.Is(err, ErrIncompleteSnapshot), errors.Is(err, ErrScenarioScope):
		return ScenarioReferenceCheck{Execution: execution, Usable: false, Reason: err.Error()}, nil
	default:
		return ScenarioReferenceCheck{}, err
	}
}
