package platform

// The environment registry's storage logic, written once for both backends.
//
// What differs between SQLite and PostgreSQL here is exactly one thing: how a
// transaction takes the write intent on the owning project row. Everything
// after that — the order of the checks, the cap, the row shape, the page
// query, the compare-and-swap — is the same text, because two copies of "is
// this project full" is precisely where the two backends would drift apart
// while both passing their own tests.
//
// See docs/adr/0037-postgresql-is-the-shared-platform-persistence-backend.md
// for why the shared surface is this narrow, and
// docs/adr/0039-environments-are-project-owned-ranked-references.md for the
// invariant the lock exists to hold.

import (
	"context"
	"database/sql"
	"fmt"
)

// environmentWriter is one backend's transaction, seen through the three
// operations a create needs.
//
// Deliberately not a general transaction abstraction: no Begin, no Commit, no
// query building. Each backend owns its own transaction lifecycle in its own
// file, where the dialect is visible.
type environmentWriter interface {
	rowQuerier

	// exec runs a statement and reports rows affected.
	exec(ctx context.Context, query string, args ...any) (int64, error)

	// lockProject takes the write intent on the owning project row for the
	// rest of the transaction, and reports ErrStoreNotFound when the project
	// does not exist.
	//
	// This is the serialization point for the per-project environment cap.
	// The cap is a cross-row invariant, so no predicate on the row being
	// inserted can express it: two transactions can each count 63
	// environments, each insert a different ref, and each commit against an
	// intact primary key. Locking the project both bounds the contention to
	// the project the invariant is about and makes the count a decision no
	// concurrent writer can invalidate before the insert lands.
	lockProject(ctx context.Context, projectID string) error
}

// insertEnvironmentLocked performs the checks and the insert, with the
// project row already locked by the caller.
//
// The order is the contract:
//
//	missing project       → ErrStoreNotFound   (established by the lock)
//	(project, ref) exists → ErrStoreAlreadyExists, at any count
//	count >= cap          → ErrEnvironmentLimit
//	otherwise             → insert
//
// Identity before cap, because a caller re-sending a ref that already exists
// is adding nothing: the cap is irrelevant to that request, and answering
// with a limit would be both misleading and dependent on which of two racing
// callers arrived first. It does *not* mean a ref that does not exist yet can
// be created past the cap — a create that would add a row is subject to the
// cap whoever else is asking for it.
func insertEnvironmentLocked(
	ctx context.Context, w environmentWriter, env Environment, rank uint16, ranked bool,
) error {
	exists, err := environmentExists(ctx, w, env.ProjectID(), env.Ref())
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: environment %s in project %s",
			ErrStoreAlreadyExists, preview(string(env.Ref())), preview(string(env.ProjectID())))
	}

	count, err := environmentCount(ctx, w, env.ProjectID())
	if err != nil {
		return err
	}
	if count >= maxProjectEnvironments {
		return fmt.Errorf(
			"%w: project %s holds %d environments and may create no more than %d",
			ErrEnvironmentLimit, preview(string(env.ProjectID())), count, maxProjectEnvironments)
	}

	if _, err := w.exec(ctx, w.rebind(
		`INSERT INTO `+tableEnvironments+`
		 (project_id, ref, name, rank, status, revision)
		 VALUES (?, ?, ?, ?, ?, ?)`),
		string(env.ProjectID()), string(env.Ref()), env.Name(),
		nullRank(rank, ranked), string(env.Status()), int64(env.Revision()),
	); err != nil {
		return fmt.Errorf("platform: store environment: %w", err)
	}
	return nil
}

func environmentExists(
	ctx context.Context, q rowQuerier, projectID ProjectID, ref EnvironmentRef,
) (bool, error) {
	var one int
	err := q.queryRow(ctx, q.rebind(
		`SELECT 1 FROM `+tableEnvironments+` WHERE project_id = ? AND ref = ?`),
		string(projectID), string(ref)).Scan(&one)
	switch {
	case q.noRows(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("platform: verify environment: %w", err)
	}
	return true, nil
}

func environmentCount(ctx context.Context, q rowQuerier, projectID ProjectID) (int, error) {
	var count int
	err := q.queryRow(ctx, q.rebind(
		`SELECT COUNT(*) FROM `+tableEnvironments+` WHERE project_id = ?`),
		string(projectID)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("platform: count environments: %w", err)
	}
	return count, nil
}

// loadEnvironment reads one environment and rebuilds it through the domain.
//
// A stored row that cannot make a valid Environment is ErrStoreCorrupt, never
// a partially trusted value: an unrecognized status in particular is refused
// rather than coerced to active, because guessing would silently reopen an
// archived environment.
func loadEnvironment(
	ctx context.Context, q rowQuerier, projectID ProjectID, ref EnvironmentRef,
) (Environment, error) {
	var name, status string
	var rank sql.NullInt64
	var revision int64

	err := q.queryRow(ctx, q.rebind(
		`SELECT name, rank, status, revision FROM `+tableEnvironments+`
		 WHERE project_id = ? AND ref = ?`),
		string(projectID), string(ref)).Scan(&name, &rank, &status, &revision)
	switch {
	case q.noRows(err):
		return Environment{}, fmt.Errorf("%w: environment %s in project %s",
			ErrStoreNotFound, preview(string(ref)), preview(string(projectID)))
	case err != nil:
		return Environment{}, fmt.Errorf("platform: load environment: %w", err)
	}
	return restoreEnvironmentRow(projectID, ref, name, rank, status, revision)
}

// restoreEnvironmentRow turns one scanned row into a domain value.
func restoreEnvironmentRow(
	projectID ProjectID, ref EnvironmentRef,
	name string, rank sql.NullInt64, status string, revision int64,
) (Environment, error) {
	corrupt := func(err error) (Environment, error) {
		return Environment{}, fmt.Errorf("%w: environment %s in project %s: %w",
			ErrStoreCorrupt, preview(string(ref)), preview(string(projectID)), err)
	}
	if revision < 0 {
		return corrupt(fmt.Errorf("revision %d is negative", revision))
	}
	var value uint16
	if rank.Valid {
		if rank.Int64 < 0 || rank.Int64 > maxEnvironmentRank {
			return corrupt(fmt.Errorf("rank %d is outside 0..%d", rank.Int64, maxEnvironmentRank))
		}
		value = uint16(rank.Int64)
	}
	env, err := restoreEnvironment(
		ref, projectID, name, value, rank.Valid, EnvironmentStatus(status), uint64(revision))
	if err != nil {
		return corrupt(err)
	}
	return env, nil
}

// queryEnvironmentPage reads at most limit rows in ref byte order.
//
// The bound is applied in SQL, so a project holding hundreds of migrated
// environments costs one page of memory rather than all of them. Traversal is
// by ref because ref is immutable — see ControlStore.ProjectEnvironments.
func queryEnvironmentPage(
	ctx context.Context, q evidenceQuerier, projectID ProjectID, after EnvironmentRef, limit int,
) ([]Environment, error) {
	rows, err := q.query(ctx, q.rebind(
		`SELECT ref, name, rank, status, revision FROM `+tableEnvironments+`
		 WHERE project_id = ? AND ref > ?
		 ORDER BY ref
		 LIMIT ?`),
		string(projectID), string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("platform: list environments: %w", err)
	}
	defer rows.Close()

	environments := make([]Environment, 0, limit)
	for rows.Next() {
		var ref, name, status string
		var rank sql.NullInt64
		var revision int64
		if err := rows.Scan(&ref, &name, &rank, &status, &revision); err != nil {
			return nil, fmt.Errorf("platform: list environments: %w", err)
		}
		env, err := restoreEnvironmentRow(
			projectID, EnvironmentRef(ref), name, rank, status, revision)
		if err != nil {
			return nil, err
		}
		environments = append(environments, env)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform: list environments: %w", err)
	}
	return environments, nil
}

// validateEnvironmentUpdate refuses a compare-and-swap that would move an
// identity or skip a revision.
//
// The store will not let next carry a different ref or project than previous:
// identity is (ProjectID, EnvironmentRef), a run's historical reference
// resolves through it, and an update that could rewrite either would make a
// completed run point at something else. Changing an environment's identity
// means creating another one.
func validateEnvironmentUpdate(previous, next Environment) error {
	if previous.Ref() != next.Ref() {
		return fmt.Errorf("%w: environment ref %s cannot become %s",
			ErrInvalidID, preview(string(previous.Ref())), preview(string(next.Ref())))
	}
	if previous.ProjectID() != next.ProjectID() {
		return fmt.Errorf("%w: environment %s cannot move from project %s to %s",
			ErrInvalidID, preview(string(previous.Ref())),
			preview(string(previous.ProjectID())), preview(string(next.ProjectID())))
	}
	if next.Revision() != previous.Revision()+1 {
		return fmt.Errorf(
			"%w: environment %s update advances revision %d to %d, expected %d",
			ErrInvalidID, preview(string(previous.Ref())),
			previous.Revision(), next.Revision(), previous.Revision()+1)
	}
	return nil
}

// environmentUpdateMiss explains an update that matched no row.
//
// Two different conditions reach the same empty result, and a caller acts
// differently on each: the environment is gone, or somebody else changed it
// while this caller held a stale copy. One follow-up read distinguishes them
// rather than reporting whichever is more convenient.
func environmentUpdateMiss(ctx context.Context, q rowQuerier, previous Environment) error {
	current, err := loadEnvironment(ctx, q, previous.ProjectID(), previous.Ref())
	if err != nil {
		return err
	}
	return fmt.Errorf(
		"%w: environment %s is at revision %d, not %d",
		ErrStoreConflict, preview(string(previous.Ref())), current.Revision(), previous.Revision())
}

// nullRank renders an optional rank for storage. NULL means unranked, which
// is a different statement from rank 0.
func nullRank(rank uint16, ranked bool) any {
	if !ranked {
		return nil
	}
	return int64(rank)
}
