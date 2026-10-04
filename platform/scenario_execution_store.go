package platform

// Scenario execution storage, written once for both backends (schema v9).
//
// What differs between SQLite and PostgreSQL is how a transaction serializes
// completions within a project: PostgreSQL takes FOR UPDATE on the project
// row, SQLite takes its database-wide write intent by writing that row. Both
// are the existing lockProject seam. Everything after that — the row shape,
// the sequence, the compare-and-swap, the restore — is this file.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ScenarioExecutionStore persists scenario executions.
//
// A capability of its own rather than methods on ControlStore or
// EvaluationStore: an execution is control-plane metadata that names
// evaluation runs, and neither existing capability owns that. Both shipped
// backends implement it, and Store requires it.
type ScenarioExecutionStore interface {
	// CreateScenarioExecution stores a running execution with no
	// repetitions. An existing identifier is ErrStoreAlreadyExists, whatever
	// the rest of the value says.
	CreateScenarioExecution(ctx context.Context, execution ScenarioExecution) error

	// ScenarioExecution loads one execution with its associations.
	ScenarioExecution(ctx context.Context, id ScenarioExecutionID) (ScenarioExecution, error)

	// CompleteScenarioExecution moves a running execution to completed with
	// its ordered associations and verdict, in one transaction, and returns
	// the stored value.
	//
	// The transaction serializes on the execution's project row, assigns the
	// next completion sequence within the project, inserts the associations
	// and compare-and-swaps the status from running. An execution that is no
	// longer running is ErrScenarioExecutionState and nothing is written: a
	// completion is not retried into a second one.
	CompleteScenarioExecution(
		ctx context.Context, id ScenarioExecutionID,
		repetitions []ScenarioRepetition, verdict GateVerdict, at time.Time,
	) (ScenarioExecution, error)

	// FailScenarioExecution moves a running execution to failed and returns
	// the stored value. A failed one is returned unchanged; a completed one is
	// ErrScenarioExecutionState.
	FailScenarioExecution(ctx context.Context, id ScenarioExecutionID, at time.Time) (ScenarioExecution, error)

	// LatestCompletedScenarioExecution returns the completed execution of this
	// scenario in this scope with the highest completion sequence, or
	// ErrStoreNotFound. Only completed executions are candidates; whether the
	// one found is usable is the caller's question, and a caller that finds it
	// unusable must not ask for the next one.
	LatestCompletedScenarioExecution(
		ctx context.Context, scenarioName string, scope ScenarioScope,
	) (ScenarioExecution, error)
}

// Schema v9's tables and index.
const (
	tableScenarioExecutions  = "platform_scenario_executions"
	tableScenarioRepetitions = "platform_scenario_repetitions"

	// indexScenarioExecutionsLatest backs `last`: equality on the scope and
	// the scenario name, then the sequence descending.
	indexScenarioExecutionsLatest = "platform_scenario_executions_latest"

	// constraintScenarioSequence names the per-project uniqueness of the
	// completion sequence, so no backend generates a name for it.
	constraintScenarioSequence = "platform_scenario_executions_sequence"
)

// scenarioExecutionSchemaStatements creates v9's tables in either dialect.
//
// textType is the identifier column type — TEXT, or TEXT COLLATE "C" on
// PostgreSQL for the byte ordering every other key column has. intType is the
// signed 64-bit integer: INTEGER on SQLite, BIGINT on PostgreSQL. Two
// integers are integers rather than text, unlike the counters: runs is bounded
// by MaxRepetitions and the completion sequence by the number of executions,
// and the sequence has to sort numerically in SQL.
//
// No foreign key from a repetition to its run: runs are EvaluationStore
// entities, and the promotion history refused the same constraint for the
// same reason. A reference whose run has gone is reported by name when it is
// resolved, rather than making the execution unloadable. No foreign key from
// an execution to its project either: a self-contained execution begins
// before its first repetition has provisioned the project.
//
// UNIQUE (project_id, completion_sequence) is the backstop for the sequence:
// the project lock already serializes completions, and a second writer that
// somehow got past it fails rather than sharing a position.
func scenarioExecutionSchemaStatements(textType, intType string) []string {
	return []string{
		`CREATE TABLE ` + tableScenarioExecutions + ` (
			id                     ` + textType + ` PRIMARY KEY,
			scenario_name          ` + textType + ` NOT NULL,
			project_id             ` + textType + ` NOT NULL,
			agent_id               ` + textType + ` NOT NULL,
			environment            ` + textType + ` NOT NULL,
			runs                   ` + intType + ` NOT NULL,
			reference_execution_id ` + textType + `,
			status                 TEXT NOT NULL,
			started_at             TEXT NOT NULL,
			finished_at            TEXT,
			completion_sequence    ` + intType + `,
			verdict                TEXT,
			CONSTRAINT ` + constraintScenarioSequence + ` UNIQUE (project_id, completion_sequence)
		)`,
		`CREATE INDEX ` + indexScenarioExecutionsLatest + ` ON ` + tableScenarioExecutions +
			` (project_id, scenario_name, agent_id, environment, completion_sequence)`,
		`CREATE TABLE ` + tableScenarioRepetitions + ` (
			execution_id       ` + textType + ` NOT NULL REFERENCES ` + tableScenarioExecutions + `(id),
			side               TEXT NOT NULL,
			repetition_index   ` + intType + ` NOT NULL,
			run_id             ` + textType + ` NOT NULL,
			behavioral_profile ` + textType + ` NOT NULL,
			PRIMARY KEY (execution_id, side, repetition_index)
		)`,
	}
}

// scenarioExecutionWriter is one backend's transaction, seen through what a
// completion needs.
type scenarioExecutionWriter interface {
	rowQuerier
	exec(ctx context.Context, query string, args ...any) (int64, error)
	writeError(kind, id string, err error) error
	lockProject(ctx context.Context, projectID string) error
}

// insertScenarioExecution stores a new running execution.
func insertScenarioExecution(ctx context.Context, w scenarioExecutionWriter, e ScenarioExecution) error {
	if e.status != ScenarioExecutionRunning {
		return fmt.Errorf("%w: a new scenario execution must be running, not %s",
			ErrScenarioExecutionState, e.status)
	}
	if err := e.validate(); err != nil {
		return err
	}
	var reference any
	if e.reference != "" {
		reference = string(e.reference)
	}
	key, err := recencyKey(e.startedAt)
	if err != nil {
		return err
	}
	_, err = w.exec(ctx, w.rebind(
		`INSERT INTO `+tableScenarioExecutions+` (
		   id, scenario_name, project_id, agent_id, environment, runs,
		   reference_execution_id, status, started_at, started_order
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		string(e.id), e.scenarioName, string(e.scope.ProjectID), string(e.scope.AgentID),
		string(e.scope.Environment), int64(e.runs), reference, string(e.status), timeText(e.startedAt), key)
	if err != nil {
		return w.writeError("scenario execution", string(e.id), err)
	}
	return nil
}

// completeScenarioExecutionTx is the whole completion, inside the caller's
// transaction.
func completeScenarioExecutionTx(
	ctx context.Context, w scenarioExecutionWriter, id ScenarioExecutionID,
	repetitions []ScenarioRepetition, verdict GateVerdict, at time.Time,
) (ScenarioExecution, error) {
	current, err := loadScenarioExecutionRow(ctx, w, id)
	if err != nil {
		return ScenarioExecution{}, err
	}
	// Serialize every completion in the project before the sequence is read,
	// so two completions cannot both read the same maximum.
	if err := w.lockProject(ctx, string(current.scope.ProjectID)); err != nil {
		return ScenarioExecution{}, err
	}

	var last sql.NullInt64
	if err := w.queryRow(ctx, w.rebind(
		`SELECT MAX(completion_sequence) FROM `+tableScenarioExecutions+` WHERE project_id = ?`),
		string(current.scope.ProjectID)).Scan(&last); err != nil {
		return ScenarioExecution{}, fmt.Errorf("platform: read completion sequence: %w", err)
	}
	if last.Valid && last.Int64 < 0 {
		return ScenarioExecution{}, fmt.Errorf("%w: project %s has a negative completion sequence",
			ErrStoreCorrupt, preview(string(current.scope.ProjectID)))
	}
	sequence := uint64(last.Int64) + 1

	next, err := current.complete(repetitions, verdict, sequence, at)
	if err != nil {
		return ScenarioExecution{}, err
	}

	affected, err := w.exec(ctx, w.rebind(
		`UPDATE `+tableScenarioExecutions+`
		 SET status = ?, finished_at = ?, completion_sequence = ?, verdict = ?
		 WHERE id = ? AND status = ?`),
		string(ScenarioExecutionCompleted), timeText(at), int64(sequence), string(verdict),
		string(id), string(ScenarioExecutionRunning))
	if err != nil {
		return ScenarioExecution{}, w.writeError("scenario execution", string(id), err)
	}
	if affected == 0 {
		return ScenarioExecution{}, fmt.Errorf("%w: execution %s stopped running before it could complete",
			ErrScenarioExecutionState, preview(string(id)))
	}

	for _, r := range next.repetitions {
		if _, err := w.exec(ctx, w.rebind(
			`INSERT INTO `+tableScenarioRepetitions+`
			 (execution_id, side, repetition_index, run_id, behavioral_profile)
			 VALUES (?, ?, ?, ?, ?)`),
			string(id), string(r.Side), int64(r.Index), string(r.RunID), string(r.BehavioralProfile),
		); err != nil {
			return ScenarioExecution{}, w.writeError("scenario repetition", string(id), err)
		}
	}
	return next, nil
}

// failScenarioExecutionTx is the whole failure, inside the caller's
// transaction.
func failScenarioExecutionTx(
	ctx context.Context, w scenarioExecutionWriter, id ScenarioExecutionID, at time.Time,
) (ScenarioExecution, error) {
	current, err := loadScenarioExecutionRow(ctx, w, id)
	if err != nil {
		return ScenarioExecution{}, err
	}
	next, err := current.fail(at)
	if err != nil {
		return ScenarioExecution{}, err
	}
	if current.status == ScenarioExecutionFailed {
		return current, nil
	}
	affected, err := w.exec(ctx, w.rebind(
		`UPDATE `+tableScenarioExecutions+` SET status = ?, finished_at = ?
		 WHERE id = ? AND status = ?`),
		string(ScenarioExecutionFailed), timeText(at), string(id), string(ScenarioExecutionRunning))
	if err != nil {
		return ScenarioExecution{}, w.writeError("scenario execution", string(id), err)
	}
	if affected == 0 {
		return ScenarioExecution{}, fmt.Errorf("%w: execution %s stopped running before it could fail",
			ErrScenarioExecutionState, preview(string(id)))
	}
	return next, nil
}

// loadScenarioExecutionRow reads the execution row alone, without its
// associations — what a lifecycle transition needs, since only a running
// execution transitions and a running one has none.
func loadScenarioExecutionRow(
	ctx context.Context, q rowQuerier, id ScenarioExecutionID,
) (ScenarioExecution, error) {
	var (
		name, project, agent, environment, status, startedAt string
		runs                                                 int64
		reference, finishedAt, verdict                       sql.NullString
		sequence                                             sql.NullInt64
	)
	err := q.queryRow(ctx, q.rebind(
		`SELECT scenario_name, project_id, agent_id, environment, runs,
		        reference_execution_id, status, started_at, finished_at,
		        completion_sequence, verdict
		 FROM `+tableScenarioExecutions+` WHERE id = ?`), string(id)).
		Scan(&name, &project, &agent, &environment, &runs, &reference, &status,
			&startedAt, &finishedAt, &sequence, &verdict)
	switch {
	case q.noRows(err):
		return ScenarioExecution{}, fmt.Errorf("%w: scenario execution %s",
			ErrStoreNotFound, preview(string(id)))
	case err != nil:
		return ScenarioExecution{}, fmt.Errorf("platform: load scenario execution: %w", err)
	}

	started, err := parseTimeText("scenario execution started_at", startedAt)
	if err != nil {
		return ScenarioExecution{}, err
	}
	finished, err := parseNullTimeText("scenario execution finished_at", finishedAt)
	if err != nil {
		return ScenarioExecution{}, err
	}
	if sequence.Valid && sequence.Int64 <= 0 {
		return ScenarioExecution{}, fmt.Errorf("%w: scenario execution %s has completion sequence %d",
			ErrStoreCorrupt, preview(string(id)), sequence.Int64)
	}
	if runs < 1 || runs > MaxRepetitions {
		return ScenarioExecution{}, fmt.Errorf("%w: scenario execution %s has runs %d",
			ErrStoreCorrupt, preview(string(id)), runs)
	}
	return ScenarioExecution{
		id: id, scenarioName: name,
		scope: ScenarioScope{ProjectID: ProjectID(project), AgentID: AgentID(agent),
			Environment: EnvironmentRef(environment)},
		runs: int(runs), reference: ScenarioExecutionID(reference.String),
		status: ScenarioExecutionStatus(status), startedAt: started, finishedAt: finished,
		completionSequence: uint64(sequence.Int64), verdict: GateVerdict(verdict.String),
	}, nil
}

// loadScenarioExecution reads one execution and its associations, and
// refuses anything a live value could not be.
func loadScenarioExecution(
	ctx context.Context, q evidenceQuerier, id ScenarioExecutionID,
) (ScenarioExecution, error) {
	e, err := loadScenarioExecutionRow(ctx, q, id)
	if err != nil {
		return ScenarioExecution{}, err
	}
	// Bounded: one more than a valid execution can hold, so an oversized
	// association set is seen as corruption rather than read whole.
	rows, err := q.query(ctx, q.rebind(
		`SELECT side, repetition_index, run_id, behavioral_profile
		 FROM `+tableScenarioRepetitions+` WHERE execution_id = ?
		 ORDER BY CASE side WHEN 'reference' THEN 0 ELSE 1 END, repetition_index
		 LIMIT ?`), string(id), 2*MaxRepetitions+1)
	if err != nil {
		return ScenarioExecution{}, fmt.Errorf("platform: load scenario repetitions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var side, runID, profile string
		var index int64
		if err := rows.Scan(&side, &index, &runID, &profile); err != nil {
			return ScenarioExecution{}, fmt.Errorf("platform: load scenario repetitions: %w", err)
		}
		e.repetitions = append(e.repetitions, ScenarioRepetition{
			Side: ComparisonSide(side), Index: int(index),
			RunID: EvaluationRunID(runID), BehavioralProfile: BehavioralProfileRef(profile),
		})
	}
	if err := rows.Err(); err != nil {
		return ScenarioExecution{}, fmt.Errorf("platform: load scenario repetitions: %w", err)
	}
	return restoreScenarioExecution(e)
}

// latestCompletedScenarioExecutionID finds the identifier `last` names.
//
// Ordered by the completion sequence, then by identifier: the sequence is
// unique within a project, so the identifier never decides in a valid
// database, and is there so a damaged one still answers deterministically.
func latestCompletedScenarioExecutionID(
	ctx context.Context, q rowQuerier, scenarioName string, scope ScenarioScope,
) (ScenarioExecutionID, error) {
	var id string
	err := q.queryRow(ctx, q.rebind(
		`SELECT id FROM `+tableScenarioExecutions+`
		 WHERE project_id = ? AND scenario_name = ? AND agent_id = ? AND environment = ?
		   AND status = ?
		 ORDER BY completion_sequence DESC, id DESC
		 LIMIT 1`),
		string(scope.ProjectID), scenarioName, string(scope.AgentID), string(scope.Environment),
		string(ScenarioExecutionCompleted)).Scan(&id)
	switch {
	case q.noRows(err):
		return "", fmt.Errorf(
			"%w: no completed execution of scenario %s for agent %s in project %s, environment %s",
			ErrStoreNotFound, preview(scenarioName), preview(string(scope.AgentID)),
			preview(string(scope.ProjectID)), preview(string(scope.Environment)))
	case err != nil:
		return "", fmt.Errorf("platform: find latest scenario execution: %w", err)
	}
	return ScenarioExecutionID(id), nil
}
