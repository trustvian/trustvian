package platform

// PostgreSQL concurrency correctness.
//
// This is the part a port gets wrong. SQLite's store holds one connection, so
// every read-then-write in it is atomic by construction; a pool has no such
// property, and the same code against PostgreSQL races. These tests drive the
// races that matter and assert the invariants survive them.
//
// Correctness only. Throughput, contention behaviour and multi-node operation
// are task 069's subject, and nothing here is a benchmark.
//
// Every test below uses several real connections. A test that accidentally
// funnels its goroutines through one connection proves nothing about a pooled
// backend, so newConcurrentPostgresStore sets an explicit floor and
// TestPostgresConcurrencyUsesSeveralConnections checks the pool actually opens
// them.

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// concurrentWorkers is how many goroutines race each invariant.
//
// Small enough to stay fast, large enough that a lost update shows up rather
// than being a coin flip: with eight racers an unprotected read-then-write
// fails essentially every run.
const concurrentWorkers = 8

// newConcurrentPostgresStore opens a store whose pool can actually run the
// racers in parallel.
func newConcurrentPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	isolated := isolatedSchemaDSN(t, postgresDSN(t))
	store, err := OpenPostgresStore(context.Background(), PostgresConfig{
		DSN: isolated,
		// An explicit floor: pgx's default depends on the machine, and a
		// four-connection default on a small CI runner would quietly serialize
		// eight racers and make these tests pass for the wrong reason.
		MaxConnections: concurrentWorkers + 2,
	})
	if err != nil {
		t.Fatalf("OpenPostgresStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// raceStart releases every worker at once.
//
// Goroutines started in a loop otherwise run staggered, and a stagger is how a
// race test quietly stops racing.
func raceStart(workers int, work func(worker int) error) []error {
	var ready, done sync.WaitGroup
	release := make(chan struct{})
	results := make([]error, workers)

	ready.Add(workers)
	done.Add(workers)
	for i := range workers {
		go func(worker int) {
			defer done.Done()
			ready.Done()
			<-release
			results[worker] = work(worker)
		}(i)
	}
	ready.Wait()
	close(release)
	done.Wait()
	return results
}

// TestPostgresConcurrencyUsesSeveralConnections keeps the other tests honest.
//
// If the pool served every goroutine from one connection, PostgreSQL would
// serialize them for us and every invariant below would hold for a reason that
// has nothing to do with the code under test.
func TestPostgresConcurrencyUsesSeveralConnections(t *testing.T) {
	store := newConcurrentPostgresStore(t)

	// Hold several connections busy at once and observe the pool's own count.
	var hold sync.WaitGroup
	release := make(chan struct{})
	hold.Add(concurrentWorkers)
	for range concurrentWorkers {
		go func() {
			defer hold.Done()
			conn, err := store.pool.Acquire(context.Background())
			if err != nil {
				return
			}
			defer conn.Release()
			<-release
		}()
	}
	// Let them all acquire before measuring.
	deadline := time.After(5 * time.Second)
	for {
		if store.pool.Stat().AcquiredConns() >= int32(concurrentWorkers) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d connections were acquired; the pool cannot run %d "+
				"racers in parallel, so the concurrency tests would not race",
				store.pool.Stat().AcquiredConns(), concurrentWorkers)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(release)
	hold.Wait()
}

// TestPostgresConcurrentEntityCreationYieldsOneWinner covers the create path.
//
// The primary key is the protection here, not a lock: the database refuses the
// duplicate and mapPostgresError turns SQLSTATE 23505 into the documented
// sentinel. What this proves is that the refusal is classified correctly under
// contention, and that exactly one row is durable.
func TestPostgresConcurrentEntityCreationYieldsOneWinner(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	ctx := t.Context()

	results := raceStart(concurrentWorkers, func(worker int) error {
		// Same identity, different name per worker, so a lost update would be
		// visible in the stored value rather than merely in the count.
		project, err := NewProject("proj-race", fmt.Sprintf("Name-%d", worker))
		if err != nil {
			return err
		}
		return store.CreateProject(ctx, project)
	})

	var winners, conflicts int
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrStoreAlreadyExists):
			conflicts++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if winners != 1 {
		t.Errorf("%d creates succeeded, want exactly 1", winners)
	}
	if conflicts != concurrentWorkers-1 {
		t.Errorf("%d creates reported ErrStoreAlreadyExists, want %d",
			conflicts, concurrentWorkers-1)
	}

	// And the durable value is one of the racers', untouched by the losers.
	stored, err := store.Project(ctx, "proj-race")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if !strings.HasPrefix(stored.Name(), "Name-") {
		t.Errorf("stored name = %q, want one of the racers' values", stored.Name())
	}
}

// occupancyTracker measures how many workers are inside the read-to-write
// window of a run-scoped mutation at the same time.
//
// One is the whole point: the window must be mutually exclusive per run, which
// is what SELECT ... FOR UPDATE buys. Anything above one is a pair of
// transactions that both read the old state and are both about to write.
type occupancyTracker struct {
	mu      sync.Mutex
	current int
	max     int
}

func (o *occupancyTracker) enter() {
	o.mu.Lock()
	o.current++
	o.max = max(o.max, o.current)
	o.mu.Unlock()
	// Long enough that a genuinely concurrent worker overlaps this one, short
	// enough to keep the test fast. Only reached inside the critical section.
	time.Sleep(25 * time.Millisecond)
	o.mu.Lock()
	o.current--
	o.mu.Unlock()
}

func (o *occupancyTracker) peak() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.max
}

// installMutationHook sets the interleaving hook for one test and clears it
// afterwards, so no test inherits another's hook.
func installMutationHook(t *testing.T, hook func()) {
	t.Helper()
	testHookInRunMutation.Store(&hook)
	t.Cleanup(func() { testHookInRunMutation.Store(nil) })
}

// TestPostgresRunMutationWindowIsMutuallyExclusive proves the row lock does its
// job, directly.
//
// This measures the property rather than a consequence of it. A test that only
// counted winners would pass with no lock at all, because the status predicate
// alone also produces one winner — and would pass with *neither*, because a
// local transaction finishes before the next one starts. Occupancy is the thing
// FOR UPDATE actually changes.
func TestPostgresRunMutationWindowIsMutuallyExclusive(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	ctx := t.Context()

	created := seedPostgresRun(t, store, "run-excl", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	var occupancy occupancyTracker
	installMutationHook(t, occupancy.enter)

	raceStart(concurrentWorkers, func(worker int) error {
		started, err := created.Start(created.CreatedAt().Add(time.Duration(worker+1) * time.Second))
		if err != nil {
			return err
		}
		// Errors are expected here — only one transition can win. What is under
		// test is how many workers were inside the window at once.
		_ = store.UpdateEvaluationRun(ctx, created, started)
		return nil
	})

	if peak := occupancy.peak(); peak != 1 {
		t.Errorf("%d workers were inside one run's read-to-write window at once, want 1. "+
			"Without SELECT ... FOR UPDATE they all read the same state and all "+
			"proceed to write, which is the lost update this lock prevents", peak)
	}
}

// TestPostgresConcurrentLifecycleTransitionYieldsOneWinner is the lost-update
// case, with the interleaving forced.
//
// Without the hook this test is theatre: the first transaction commits before
// the second reads, so every loser sees the new state and reports a conflict no
// matter what the implementation does — it passed with both the lock and the
// predicate removed. The hook holds each worker inside the window long enough
// for the others to arrive, which is the state a connection pool makes possible.
func TestPostgresConcurrentLifecycleTransitionYieldsOneWinner(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	ctx := t.Context()

	created := seedPostgresRun(t, store, "run-race", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Hold each worker briefly between its read and its write. With the lock in
	// place only one is ever here, so this costs one sleep; without it, every
	// worker waits here holding a stale read.
	installMutationHook(t, func() { time.Sleep(25 * time.Millisecond) })

	results := raceStart(concurrentWorkers, func(worker int) error {
		started, err := created.Start(created.CreatedAt().Add(time.Duration(worker+1) * time.Second))
		if err != nil {
			return err
		}
		return store.UpdateEvaluationRun(ctx, created, started)
	})

	var winners, conflicts int
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrStoreConflict):
			conflicts++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if winners != 1 {
		t.Errorf("%d transitions succeeded, want exactly 1. More than one means a "+
			"caller was told its transition applied while another overwrote it", winners)
	}
	if conflicts != concurrentWorkers-1 {
		t.Errorf("%d transitions reported ErrStoreConflict, want %d",
			conflicts, concurrentWorkers-1)
	}

	final, err := store.EvaluationRun(ctx, "run-race")
	if err != nil {
		t.Fatalf("EvaluationRun: %v", err)
	}
	if final.Status() != RunRunning {
		t.Errorf("status = %s, want %s", final.Status(), RunRunning)
	}
	// The durable start instant is the winner's, not a later overwrite.
	if final.StartedAt().IsZero() {
		t.Error("the winning transition did not record a start instant")
	}
}

// TestPostgresConcurrentIngestOfTheSameSequenceCommitsOnce is the invariant the
// run row lock exists for.
//
// Every racer submits the same record at sequence 1 against a run with no cursor
// row yet. SELECT ... FOR UPDATE cannot lock a row that does not exist, so the
// cursor cannot be the lock — the run row is, and it always exists.
//
// Unprotected, every racer reads "no cursor, next is 1", every one passes, every
// one writes evidence and upserts the cursor, and every one returns Committed
// for the same logical record.
func TestPostgresConcurrentIngestOfTheSameSequenceCommitsOnce(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	ctx := t.Context()

	commit := runningRunWithCommit(t, store, "run-ing-race", strings.Repeat("a", 64))

	// Same reason as the lifecycle race: without this the first commit lands
	// before the second reads the cursor, and the test proves nothing.
	installMutationHook(t, func() { time.Sleep(25 * time.Millisecond) })

	results := make([]EvaluationIngestCommitResult, concurrentWorkers)
	errs := raceStart(concurrentWorkers, func(worker int) error {
		result, err := store.CommitEvaluationIngest(ctx, commit)
		results[worker] = result
		return err
	})

	var committed, already int
	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d returned %v; an identical retry is not a failure", i, err)
			continue
		}
		switch results[i].Disposition {
		case EvaluationIngestCommitted:
			committed++
		case EvaluationIngestAlreadyCommitted:
			already++
		default:
			t.Errorf("worker %d reported disposition %v", i, results[i].Disposition)
		}
	}
	if committed != 1 {
		t.Errorf("%d workers reported Committed, want exactly 1. More than one means "+
			"the same record was counted more than once", committed)
	}
	if already != concurrentWorkers-1 {
		t.Errorf("%d workers reported AlreadyCommitted, want %d", already, concurrentWorkers-1)
	}

	// The cursor advanced exactly once, and the evidence counted one record.
	state, err := store.EvaluationIngestState(ctx, "run-ing-race")
	if err != nil {
		t.Fatalf("EvaluationIngestState: %v", err)
	}
	if state.NextSequence() != 2 {
		t.Errorf("next sequence = %d, want 2", state.NextSequence())
	}
	aggregate, _, err := store.EvaluationEvidence(ctx, "run-ing-race")
	if err != nil {
		t.Fatalf("EvaluationEvidence: %v", err)
	}
	if aggregate.RecordCount() != 1 {
		t.Errorf("record count = %d, want 1", aggregate.RecordCount())
	}
}

// TestPostgresConcurrentIngestOfDifferentRecordsAtOneSequence is the dangerous
// half of the same race.
//
// Identical retries can be reconciled — same digest, same sequence, so the
// second is a retry of the first. Two *different* records at the same sequence
// cannot: one of them must be refused. Unprotected, both commit and the loser's
// evidence is overwritten while its caller is told the record landed.
func TestPostgresConcurrentIngestOfDifferentRecordsAtOneSequence(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	ctx := t.Context()

	base := runningRunWithCommit(t, store, "run-ing-diff", strings.Repeat("a", 64))

	// Each worker submits the same sequence with its own digest.
	commits := make([]EvaluationIngestCommit, concurrentWorkers)
	for i := range commits {
		commits[i] = base
		commits[i].RecordDigest = strings.Repeat(fmt.Sprintf("%x", i%16), 64)
	}

	installMutationHook(t, func() { time.Sleep(25 * time.Millisecond) })

	results := make([]EvaluationIngestCommitResult, concurrentWorkers)
	errs := raceStart(concurrentWorkers, func(worker int) error {
		result, err := store.CommitEvaluationIngest(ctx, commits[worker])
		results[worker] = result
		return err
	})

	var committed, refused int
	for i, err := range errs {
		switch {
		case err == nil && results[i].Disposition == EvaluationIngestCommitted:
			committed++
		case err == nil && results[i].Disposition == EvaluationIngestAlreadyCommitted:
			// Only legitimate for a worker whose digest matched the winner's.
			committed += 0
			refused++
		case errors.Is(err, ErrIngestSequence):
			refused++
		default:
			t.Errorf("worker %d returned %v", i, err)
		}
	}
	if committed != 1 {
		t.Errorf("%d workers committed, want exactly 1. More than one means one "+
			"record's evidence replaced another's while its caller was told it "+
			"had landed", committed)
	}
	if committed+refused != concurrentWorkers {
		t.Errorf("accounted for %d of %d workers", committed+refused, concurrentWorkers)
	}

	state, err := store.EvaluationIngestState(ctx, "run-ing-diff")
	if err != nil {
		t.Fatalf("EvaluationIngestState: %v", err)
	}
	if state.NextSequence() != 2 {
		t.Errorf("next sequence = %d, want 2; the cursor must advance once", state.NextSequence())
	}
}

// runningRunWithCommit seeds a started run and returns a commit for sequence 1.
func runningRunWithCommit(
	t *testing.T, store *PostgresStore, runID EvaluationRunID, digest string,
) EvaluationIngestCommit {
	t.Helper()
	ctx := t.Context()

	run := seedPostgresRun(t, store, runID, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	started, err := run.Start(run.CreatedAt().Add(time.Second))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, run, started); err != nil {
		t.Fatalf("start: %v", err)
	}

	aggregate, err := NewEvaluationAggregate(started)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate: %v", err)
	}
	collector, err := NewBehaviorCollector(started)
	if err != nil {
		t.Fatalf("NewBehaviorCollector: %v", err)
	}
	rec := internalRecord("evt-1", "fp-1", "op-1")
	if aggregate, err = aggregate.AddRecord(rec); err != nil {
		t.Fatalf("AddRecord: %v", err)
	}
	if err := collector.Observe(rec); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	return EvaluationIngestCommit{
		Aggregate:            aggregate,
		Snapshot:             collector.Snapshot(),
		Sequence:             1,
		PreviousNextSequence: 1,
		RecordDigest:         digest,
	}
}

// TestPostgresEvidenceSaveWindowIsMutuallyExclusive covers the third
// run-scoped mutation.
//
// SaveEvaluationEvidence reads the run, checks the stored evidence is not newer,
// then replaces aggregate, snapshot and entries. Every step depends on the one
// before, so two savers interleaving can both decide their evidence is current
// and both write — leaving a stored aggregate and snapshot that describe
// different moments, which is precisely what SaveEvaluationEvidence's contract
// says must never happen.
//
// Added because removing this lock did not fail any other test: the exclusivity
// test drives the lifecycle path, and a mutation proved it blind to this one.
func TestPostgresEvidenceSaveWindowIsMutuallyExclusive(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	ctx := t.Context()

	run := seedPostgresRun(t, store, "run-ev-excl", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	started, err := run.Start(run.CreatedAt().Add(time.Second))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, run, started); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Identical evidence from every worker, so a save that lands is idempotent
	// and the only thing under test is how many are inside the window at once.
	aggregate, snapshot := evidenceForRun(t, started, 3)

	var occupancy occupancyTracker
	installMutationHook(t, occupancy.enter)

	raceStart(concurrentWorkers, func(int) error {
		// An identical rewrite is legal, so errors here are not the subject.
		_ = store.SaveEvaluationEvidence(ctx, aggregate, snapshot)
		return nil
	})

	if peak := occupancy.peak(); peak != 1 {
		t.Errorf("%d workers were inside one run's evidence read-to-write window at "+
			"once, want 1. Two savers that both read the stored evidence can both "+
			"conclude theirs is current and both write", peak)
	}
}

// evidenceForRun builds an aggregate and snapshot carrying records observations.
func evidenceForRun(t *testing.T, run EvaluationRun, records int) (EvaluationAggregate, BehaviorSnapshot) {
	t.Helper()
	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate: %v", err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector: %v", err)
	}
	for i := range records {
		rec := internalRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i),
			fmt.Sprintf("op-%d", i))
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord: %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	return aggregate, collector.Snapshot()
}

// TestPostgresRunScopedWritesTakeTheRowLock is a structural guard over the
// three mutations, and it exists because behaviour alone cannot pin all of them.
//
// The lock and the status predicate are independently sufficient for the
// lifecycle case: removing either one still yields exactly one winner, because
// the other catches it. That is defence in depth working, not a weak test — but
// it means the one-winner test only fails when *both* are gone, and a silent
// removal of one would pass unnoticed.
//
// Each protection is therefore pinned twice: behaviourally by an exclusivity
// test that measures what the lock changes, and structurally here.
func TestPostgresRunScopedWritesTakeTheRowLock(t *testing.T) {
	source := goSourceWithoutCommentsForTest(t, "postgres.go")

	// One lock helper, and the FOR UPDATE that makes it a lock.
	if !strings.Contains(source, "FOR UPDATE") {
		t.Error("postgres.go contains no FOR UPDATE; run-scoped writes would have " +
			"no mutual exclusion under a connection pool")
	}
	// Every run-scoped mutation must take it. Three call sites:
	// UpdateEvaluationRun, SaveEvaluationEvidence, CommitEvaluationIngest.
	if got := strings.Count(source, "lockRun(ctx, tx,"); got != 3 {
		t.Errorf("lockRun is called %d times, want 3 (UpdateEvaluationRun, "+
			"SaveEvaluationEvidence, CommitEvaluationIngest). A run-scoped write "+
			"without it can interleave with another.", got)
	}

	// And the lifecycle update keeps its predicate, which is what protects the
	// invariant if the lock is ever removed.
	if !strings.Contains(source, "WHERE id = $5 AND status = $6") {
		t.Error("the lifecycle UPDATE does not carry its status predicate; with the " +
			"lock gone, a lost update would report success")
	}
	if !strings.Contains(source, "tag.RowsAffected() != 1") {
		t.Error("the lifecycle UPDATE does not check RowsAffected; a predicate that " +
			"matches nothing would report success")
	}
}

// TestPostgresUsesReadCommitted pins the isolation decision.
//
// Every invariant here is held by a row lock, a predicate or a constraint.
// Raising isolation globally to SERIALIZABLE would make 40001 a routine outcome
// and push retry handling onto every caller for a property two row locks already
// provide — so the empty TxOptions is deliberate, not an oversight.
func TestPostgresUsesReadCommitted(t *testing.T) {
	source := goSourceWithoutCommentsForTest(t, "postgres.go")

	if !strings.Contains(source, "BeginTx(ctx, pgx.TxOptions{})") {
		t.Error("transactions no longer begin with default isolation")
	}
	for _, stronger := range []string{"Serializable", "RepeatableRead"} {
		if strings.Contains(source, stronger) {
			t.Errorf("postgres.go requests %s isolation; correctness here comes from "+
				"row locks and predicates, and a global bump would make 40001 "+
				"routine for every caller", stronger)
		}
	}
}

// TestPostgresBuildsNoSQLFromNonConstants is the injection guard.
//
// Every value is a bound parameter. The only SQL assembled at runtime is a
// placeholder list and an upsert assignment list, both derived from
// aggregateInsertColumns — a package-level list of constants — so no identifier
// can originate from HTTP, the CLI, the TUI, the WebUI or configuration.
func TestPostgresBuildsNoSQLFromNonConstants(t *testing.T) {
	source := goSourceWithoutCommentsForTest(t, "postgres.go")

	// Sprintf into SQL is the shape this forbids. The one Sprintf in the file
	// builds a "$n" placeholder from an int index, which is why the check is for
	// Sprintf adjacent to SQL keywords rather than for Sprintf at all.
	for _, bad := range []string{
		`fmt.Sprintf("SELECT`, `fmt.Sprintf("INSERT`, `fmt.Sprintf("UPDATE`,
		`fmt.Sprintf("DELETE`, `fmt.Sprintf(\"WHERE`, "+ id +", "+ runID +",
	} {
		if strings.Contains(source, bad) {
			t.Errorf("postgres.go appears to build SQL from a value (%q). Every "+
				"value must be a bound parameter.", bad)
		}
	}
}

// goSourceWithoutCommentsForTest returns a file's source with comments blanked.
//
// The guards above match on code, and postgres.go deliberately names the things
// they forbid — in prose explaining why it does not use them. Stripping comments
// is the fix; loosening a pattern so prose does not trip it is how a guard stops
// catching the real thing.
//
// Byte ranges are blanked rather than removed so positions still line up.
func goSourceWithoutCommentsForTest(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", name, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, name, body, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}

	stripped := []byte(string(body))
	base := fset.File(parsed.Pos()).Base()
	for _, group := range parsed.Comments {
		for i := int(group.Pos()) - base; i < int(group.End())-base && i < len(stripped); i++ {
			if stripped[i] != '\n' {
				stripped[i] = ' '
			}
		}
	}
	return string(stripped)
}

// ---------------------------------------------------------------------
// Environment creation cap (task 065)
// ---------------------------------------------------------------------

// installEnvironmentCreateHook sets the create-window hook for one test.
func installEnvironmentCreateHook(t *testing.T, hook func()) {
	t.Helper()
	testHookInEnvironmentCreate.Store(&hook)
	t.Cleanup(func() { testHookInEnvironmentCreate.Store(nil) })
}

// seedPostgresEnvironments fills one project to count environments.
func seedPostgresEnvironments(t *testing.T, store *PostgresStore, projectID string, count int) {
	t.Helper()
	ctx := context.Background()
	project, err := NewProject(ProjectID(projectID), "P")
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	if err := store.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	for i := range count {
		env, err := NewEnvironment(
			EnvironmentRef(fmt.Sprintf("seed-%03d", i)), ProjectID(projectID), "E")
		if err != nil {
			t.Fatalf("NewEnvironment() error = %v", err)
		}
		if err := store.CreateEnvironment(ctx, env); err != nil {
			t.Fatalf("seeding environment %d error = %v", i, err)
		}
	}
}

// TestPostgresEnvironmentCreateWindowIsMutuallyExclusive proves the project
// row lock, directly.
//
// The distinction this measures is the one a result-only test cannot make: a
// completely unlocked implementation also produces "one winner" most of the
// time against a fast local database, because each transaction commits before
// the next one reads. Occupancy inside the count-then-insert window is what
// SELECT … FOR UPDATE changes, and it is what fails if the lock is dropped.
func TestPostgresEnvironmentCreateWindowIsMutuallyExclusive(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	seedPostgresEnvironments(t, store, "proj-lock", 0)

	var occupancy occupancyTracker
	installEnvironmentCreateHook(t, occupancy.enter)

	raceStart(concurrentWorkers, func(worker int) error {
		env, err := NewEnvironment(
			EnvironmentRef(fmt.Sprintf("racer-%02d", worker)), "proj-lock", "E")
		if err != nil {
			return err
		}
		// Every one of these should succeed — the project is empty. What is
		// under test is how many were inside the window at once.
		return store.CreateEnvironment(context.Background(), env)
	})

	if peak := occupancy.peak(); peak != 1 {
		t.Errorf("%d writers were inside one project's count-to-insert window at once, want 1. "+
			"Without SELECT ... FOR UPDATE on the project row they all count the same "+
			"total and all insert, which is how a project reaches 65", peak)
	}
}

// TestPostgresEnvironmentCapHoldsUnderContention is the invariant itself: a
// project at the cap minus one cannot be pushed past it by concurrent
// writers, whatever refs they use.
func TestPostgresEnvironmentCapHoldsUnderContention(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	seedPostgresEnvironments(t, store, "proj-cap", maxProjectEnvironments-1)

	var successes, duplicates, limits atomic.Int64
	raceStart(concurrentWorkers, func(worker int) error {
		env, err := NewEnvironment(
			EnvironmentRef(fmt.Sprintf("racer-%02d", worker)), "proj-cap", "E")
		if err != nil {
			return err
		}
		switch err := store.CreateEnvironment(context.Background(), env); {
		case err == nil:
			successes.Add(1)
		case errors.Is(err, ErrStoreAlreadyExists):
			duplicates.Add(1)
		case errors.Is(err, ErrEnvironmentLimit):
			limits.Add(1)
		default:
			return err
		}
		return nil
	})

	if successes.Load() != 1 {
		t.Errorf("%d creates succeeded into a project with one slot left, want 1", successes.Load())
	}
	if limits.Load() != int64(concurrentWorkers-1) {
		t.Errorf("%d creates reported the limit, want %d", limits.Load(), concurrentWorkers-1)
	}
	if duplicates.Load() != 0 {
		t.Errorf("%d creates reported a duplicate; every racer used its own ref", duplicates.Load())
	}
	if total := countPostgresEnvironments(t, store, "proj-cap"); total != maxProjectEnvironments {
		t.Errorf("project holds %d environments, want %d", total, maxProjectEnvironments)
	}
}

// TestPostgresEnvironmentSameRefRaceReportsDuplicates: below the cap, racing
// on one previously-absent ref produces one success and duplicates for the
// rest — never a limit, because the loser found the row rather than the cap.
func TestPostgresEnvironmentSameRefRaceReportsDuplicates(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	seedPostgresEnvironments(t, store, "proj-same", 0)

	var successes, duplicates, limits atomic.Int64
	raceStart(concurrentWorkers, func(int) error {
		env, err := NewEnvironment("contested", "proj-same", "E")
		if err != nil {
			return err
		}
		switch err := store.CreateEnvironment(context.Background(), env); {
		case err == nil:
			successes.Add(1)
		case errors.Is(err, ErrStoreAlreadyExists):
			duplicates.Add(1)
		case errors.Is(err, ErrEnvironmentLimit):
			limits.Add(1)
		default:
			return err
		}
		return nil
	})

	if successes.Load() != 1 || duplicates.Load() != int64(concurrentWorkers-1) || limits.Load() != 0 {
		t.Errorf("successes=%d duplicates=%d limits=%d, want 1/%d/0",
			successes.Load(), duplicates.Load(), limits.Load(), concurrentWorkers-1)
	}
}

// TestPostgresEnvironmentCreatesInDifferentProjectsDoNotBlock: the lock is per
// project, so unrelated projects proceed together. Measured by occupancy
// again — with a table lock, or a lock on anything shared, the peak would be
// one.
func TestPostgresEnvironmentCreatesInDifferentProjectsDoNotBlock(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	for worker := range concurrentWorkers {
		seedPostgresEnvironments(t, store, fmt.Sprintf("proj-%02d", worker), 0)
	}

	// A barrier inside the window: every worker waits for all of them to
	// arrive. If the writers serialized, this would deadlock rather than
	// report a wrong number, so the test bounds its own wait.
	var arrived sync.WaitGroup
	arrived.Add(concurrentWorkers)
	released := make(chan struct{})
	var timedOut atomic.Bool
	installEnvironmentCreateHook(t, func() {
		arrived.Done()
		select {
		case <-released:
		case <-time.After(5 * time.Second):
			timedOut.Store(true)
		}
	})

	go func() {
		arrived.Wait()
		close(released)
	}()

	raceStart(concurrentWorkers, func(worker int) error {
		env, err := NewEnvironment("only", ProjectID(fmt.Sprintf("proj-%02d", worker)), "E")
		if err != nil {
			return err
		}
		return store.CreateEnvironment(context.Background(), env)
	})

	if timedOut.Load() {
		t.Error("creates into different projects blocked on each other; " +
			"the lock must be the owning project row, not something shared")
	}
}

// countPostgresEnvironments pages the collection, because a project at the cap
// does not fit one page.
func countPostgresEnvironments(t *testing.T, store *PostgresStore, projectID ProjectID) int {
	t.Helper()
	total := 0
	after := EnvironmentRef("")
	for {
		page, err := store.ProjectEnvironments(context.Background(), projectID, after, MaxEnvironmentPage)
		if err != nil {
			t.Fatalf("ProjectEnvironments() error = %v", err)
		}
		total += len(page)
		if len(page) < MaxEnvironmentPage {
			return total
		}
		after = page[len(page)-1].Ref()
	}
}

// ---------------------------------------------------------------------
// Promotion commit (task 066)
// ---------------------------------------------------------------------

// A promotion's invariant spans three rows: the two environment rows it was
// decided against, and the promotion row it writes. Holding it means no other
// promotion over an overlapping pair may sit between the revalidation and the
// insert, and that both environments are re-read inside the writing
// transaction rather than trusted from the caller's earlier read.
//
// These tests drive that directly. They are PostgreSQL-only on purpose: the
// shared conformance suite already proves both backends refuse a stale
// decision, and what cannot be shared is *how* — row locks under a connection
// pool here, one writer at a time there. SQLite is not claimed to run two
// promotion writers concurrently and is not tested as if it did.

// installPromotionLockHook sets the per-lock hook for one test.
func installPromotionLockHook(t *testing.T, hook func(int)) {
	t.Helper()
	testHookAfterPromotionLock.Store(&hook)
	t.Cleanup(func() { testHookAfterPromotionLock.Store(nil) })
}

// rankedEnv names one environment in a promotion world.
type rankedEnv struct {
	ref  string
	rank uint16
}

// seedPostgresPromotionWorld creates everything a promotion needs: the project,
// an agent, the reference and candidate candidates, and the ranked
// environments.
func seedPostgresPromotionWorld(
	t *testing.T, store *PostgresStore, projectID string, envs []rankedEnv,
) {
	t.Helper()
	ctx := context.Background()

	project, err := NewProject(ProjectID(projectID), "P")
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	if err := store.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	agent, err := NewAgent(AgentID("agent-"+projectID), ProjectID(projectID), "Agent")
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	if err := store.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	for _, id := range []string{"cand-ref-" + projectID, "cand-can-" + projectID} {
		candidate, err := NewCandidate(CandidateID(id), agent.ID(), CandidateMetadata{Label: "v1"})
		if err != nil {
			t.Fatalf("NewCandidate() error = %v", err)
		}
		if err := store.CreateCandidate(ctx, candidate); err != nil {
			t.Fatalf("CreateCandidate(%s) error = %v", id, err)
		}
	}
	for _, env := range envs {
		value, err := NewRankedEnvironment(
			EnvironmentRef(env.ref), ProjectID(projectID), env.ref, env.rank)
		if err != nil {
			t.Fatalf("NewRankedEnvironment(%s) error = %v", env.ref, err)
		}
		if err := store.CreateEnvironment(ctx, value); err != nil {
			t.Fatalf("CreateEnvironment(%s) error = %v", env.ref, err)
		}
	}
}

// postgresPromotionBetween builds one storable decision over a named pair.
func postgresPromotionBetween(
	t *testing.T, store *PostgresStore, projectID, id, sourceRef, targetRef string,
) Promotion {
	t.Helper()
	ctx := context.Background()

	source, err := store.Environment(ctx, ProjectID(projectID), EnvironmentRef(sourceRef))
	if err != nil {
		t.Fatalf("Environment(%s) error = %v", sourceRef, err)
	}
	target, err := store.Environment(ctx, ProjectID(projectID), EnvironmentRef(targetRef))
	if err != nil {
		t.Fatalf("Environment(%s) error = %v", targetRef, err)
	}

	gate, err := restoreEvaluationGateResult(
		EvaluationRunID("run-ref-"+projectID), CandidateID("cand-ref-"+projectID),
		EvaluationRunID("run-can-"+projectID), CandidateID("cand-can-"+projectID),
		EnvironmentRef(sourceRef),
		MinimumCountGate{Actual: 412, Minimum: 1, Passed: true},
		MinimumCountGate{Actual: 388, Minimum: 1, Passed: true},
		MaximumCountGate{Actual: 0, Maximum: 0, Passed: true},
		MaximumCountGate{Actual: 0, Maximum: 0, Passed: true},
		MaximumCountGate{Actual: 0, Maximum: 0, Passed: true},
		GateVerdictPass,
	)
	if err != nil {
		t.Fatalf("restoreEvaluationGateResult() error = %v", err)
	}

	promotion, err := NewPromotion(PromotionDecision{
		ID: PromotionID(id), Source: source, Target: target,
		GateResult: gate,
		DecidedAt:  time.Date(2026, 3, 1, 9, 14, 22, 481_000_321, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewPromotion(%s) error = %v", id, err)
	}
	return promotion
}

// TestPostgresPromotionCommitWindowIsMutuallyExclusive proves the two row locks
// do their job, directly.
//
// Every racer here promotes over the same pair with a distinct identifier, so a
// completely unlocked implementation also writes every row successfully — the
// unique index is on the promotion id and none of them collide. What it would
// not do is keep two transactions out of the revalidate-then-insert window at
// once, and that is the only thing this measures.
func TestPostgresPromotionCommitWindowIsMutuallyExclusive(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	seedPostgresPromotionWorld(t, store, "proj-lock",
		[]rankedEnv{{"staging", 30}, {"production", 40}})

	var occupancy occupancyTracker
	installPromotionLockHook(t, func(index int) {
		// Both locks held: the whole window the insert depends on.
		if index == 1 {
			occupancy.enter()
		}
	})

	promotions := make([]Promotion, concurrentWorkers)
	for i := range promotions {
		promotions[i] = postgresPromotionBetween(t, store, "proj-lock",
			fmt.Sprintf("promo-%02d", i), "staging", "production")
	}

	for worker, err := range raceStart(concurrentWorkers, func(worker int) error {
		return store.CreatePromotion(context.Background(), promotions[worker])
	}) {
		if err != nil {
			t.Errorf("worker %d: CreatePromotion() error = %v", worker, err)
		}
	}

	if got := occupancy.peak(); got != 1 {
		t.Errorf("peak occupancy inside the commit window = %d, want 1; two "+
			"promotions decided against the same environment state at once", got)
	}
}

// TestPostgresPromotionsOverDisjointPairsDoNotBlock keeps the lock scoped.
//
// The environment row is the unit, not the project and not the table. Two
// promotions that share no environment have no invariant in common, and a
// project-wide lock — the tempting shortcut, and the one that would make the
// test above pass just as well — would serialize them for nothing.
func TestPostgresPromotionsOverDisjointPairsDoNotBlock(t *testing.T) {
	const pairs = 4
	store := newConcurrentPostgresStore(t)

	envs := make([]rankedEnv, 0, pairs*2)
	for pair := range pairs {
		envs = append(envs,
			rankedEnv{fmt.Sprintf("pair%d-from", pair), uint16(10 + pair*2)},
			rankedEnv{fmt.Sprintf("pair%d-to", pair), uint16(11 + pair*2)})
	}
	seedPostgresPromotionWorld(t, store, "proj-disjoint", envs)

	promotions := make([]Promotion, pairs)
	for pair := range pairs {
		promotions[pair] = postgresPromotionBetween(t, store, "proj-disjoint",
			fmt.Sprintf("promo-%02d", pair),
			fmt.Sprintf("pair%d-from", pair), fmt.Sprintf("pair%d-to", pair))
	}

	// A barrier inside the window: every worker waits for all of them to
	// arrive. If the writers serialized, this would deadlock rather than
	// report a wrong number, so the test bounds its own wait.
	var arrived sync.WaitGroup
	arrived.Add(pairs)
	released := make(chan struct{})
	var timedOut atomic.Bool
	installPromotionLockHook(t, func(index int) {
		if index != 1 {
			return
		}
		arrived.Done()
		select {
		case <-released:
		case <-time.After(5 * time.Second):
			timedOut.Store(true)
		}
	})
	go func() {
		arrived.Wait()
		close(released)
	}()

	for worker, err := range raceStart(pairs, func(worker int) error {
		return store.CreatePromotion(context.Background(), promotions[worker])
	}) {
		if err != nil {
			t.Errorf("worker %d: CreatePromotion() error = %v", worker, err)
		}
	}
	if timedOut.Load() {
		t.Error("promotions over disjoint environment pairs blocked on each " +
			"other; the lock must be the two environment rows, not the project " +
			"or the table")
	}
}

// TestPostgresPromotionsInDifferentProjectsDoNotBlock is the same property
// across the identity boundary.
//
// An environment's identity is (project, ref), so the same ref in two projects
// is two rows and two independent decisions. A lock keyed on the ref alone
// would pass every other test here and still serialize unrelated tenants.
func TestPostgresPromotionsInDifferentProjectsDoNotBlock(t *testing.T) {
	const projects = 4
	store := newConcurrentPostgresStore(t)

	promotions := make([]Promotion, projects)
	for i := range projects {
		project := fmt.Sprintf("proj-%02d", i)
		seedPostgresPromotionWorld(t, store, project,
			[]rankedEnv{{"staging", 30}, {"production", 40}})
		promotions[i] = postgresPromotionBetween(t, store, project,
			fmt.Sprintf("promo-%02d", i), "staging", "production")
	}

	var arrived sync.WaitGroup
	arrived.Add(projects)
	released := make(chan struct{})
	var timedOut atomic.Bool
	installPromotionLockHook(t, func(index int) {
		if index != 1 {
			return
		}
		arrived.Done()
		select {
		case <-released:
		case <-time.After(5 * time.Second):
			timedOut.Store(true)
		}
	})
	go func() {
		arrived.Wait()
		close(released)
	}()

	for worker, err := range raceStart(projects, func(worker int) error {
		return store.CreatePromotion(context.Background(), promotions[worker])
	}) {
		if err != nil {
			t.Errorf("worker %d: CreatePromotion() error = %v", worker, err)
		}
	}
	if timedOut.Load() {
		t.Error("promotions in different projects blocked on each other; the " +
			"lock must be keyed on (project, ref), not the ref alone")
	}
}

// TestPostgresPromotionLocksEnvironmentsInByteOrder pins the acquisition order
// to the rows rather than to the roles.
//
// The honest framing matters here, because the tempting one is wrong. Locking
// source then target would also be deadlock-free today, and provably so rather
// than by luck: CanPromote requires the target to rank strictly above the
// source, so every transaction takes its two rows in increasing rank order and
// two of them can never take the same pair in opposite orders. No cycle is
// reachable, and a test claiming to construct one would be constructing
// nothing.
//
// What byte order buys is that the proof no longer depends on CanPromote. The
// three environments below are named so byte order and rank order disagree —
// refs sort a, b, c against ranks c(10) < a(20) < b(30) — and the assertion is
// that the acquisition order follows the refs in every case, including the one
// where the source sorts last. That is the property that would survive
// relaxing the promotion rule, and it is the one that fails if the ordering is
// ever dropped in favour of the roles.
//
// The concurrent half is a live check on the same three overlapping pairs: the
// hook widens the window while exactly one lock is held, and every writer must
// still commit rather than abort with 40P01.
func TestPostgresPromotionLocksEnvironmentsInByteOrder(t *testing.T) {
	const rounds = 4
	store := newConcurrentPostgresStore(t)
	seedPostgresPromotionWorld(t, store, "proj-order",
		[]rankedEnv{{"c", 10}, {"a", 20}, {"b", 30}})

	pairs := []struct{ source, target, first, second string }{
		{"c", "a", "a", "c"}, // ranks 10 → 20; the source sorts last
		{"a", "b", "a", "b"}, // ranks 20 → 30; roles and bytes agree
		{"c", "b", "b", "c"}, // ranks 10 → 30; the source sorts last
	}
	for _, pair := range pairs {
		promotion := postgresPromotionBetween(
			t, store, "proj-order", "probe-"+pair.source+pair.target,
			pair.source, pair.target)
		want := [2]EnvironmentRef{EnvironmentRef(pair.first), EnvironmentRef(pair.second)}
		if got := promotionEnvironmentOrder(promotion); got != want {
			t.Errorf("promotionEnvironmentOrder(%s→%s) = %v, want %v",
				pair.source, pair.target, got, want)
		}
	}

	// Hold every writer while exactly one of its two rows is locked, which is
	// the widest the acquisition window gets.
	installPromotionLockHook(t, func(index int) {
		if index == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	})

	for round := range rounds {
		for worker, err := range raceStart(len(pairs), func(worker int) error {
			pair := pairs[worker]
			return store.CreatePromotion(context.Background(),
				postgresPromotionBetween(t, store, "proj-order",
					fmt.Sprintf("promo-%d-%d", round, worker), pair.source, pair.target))
		}) {
			if err == nil {
				continue
			}
			t.Errorf("round %d worker %d (%s→%s): CreatePromotion() error = %v",
				round, worker, pairs[worker].source, pairs[worker].target, err)
		}
	}
}

// TestPostgresPromotionWaitsForTheLockAndSeesTheNewState is the stale case
// under real contention.
//
// The conformance suite proves a promotion built against an old revision is
// refused. This proves the mechanism that makes that true when the revision
// changes *while* the promotion is committing: the writer must block on the
// environment row rather than read around it, and when the holder commits, the
// writer must see what was committed rather than the snapshot it started with.
// A CreatePromotion that took no lock — or took one and then trusted the
// caller's copy — would succeed here and write a decision made against an
// environment configuration that no longer exists.
func TestPostgresPromotionWaitsForTheLockAndSeesTheNewState(t *testing.T) {
	store := newConcurrentPostgresStore(t)
	ctx := context.Background()
	seedPostgresPromotionWorld(t, store, "proj-stale",
		[]rankedEnv{{"staging", 30}, {"production", 40}})

	// Built now, against revision 1 of both environments.
	promotion := postgresPromotionBetween(t, store, "proj-stale", "promo-1",
		"staging", "production")

	// Hold the source row inside a transaction the store cannot see past.
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var held string
	if err := tx.QueryRow(ctx,
		`SELECT ref FROM `+tableEnvironments+`
		 WHERE project_id = $1 AND ref = $2 FOR UPDATE`,
		"proj-stale", "staging").Scan(&held); err != nil {
		t.Fatalf("lock staging: %v", err)
	}

	committed := make(chan error, 1)
	go func() { committed <- store.CreatePromotion(ctx, promotion) }()

	// The writer must still be waiting: nothing may commit against an
	// environment whose row another transaction holds.
	select {
	case err := <-committed:
		t.Fatalf("CreatePromotion() returned %v while staging was locked; it "+
			"read around the lock", err)
	case <-time.After(250 * time.Millisecond):
	}

	// Change the environment under it and release.
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableEnvironments+`
		 SET rank = 35, revision = revision + 1
		 WHERE project_id = $1 AND ref = $2`, "proj-stale", "staging"); err != nil {
		t.Fatalf("bump staging: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	select {
	case err := <-committed:
		if !errors.Is(err, ErrStoreConflict) {
			t.Fatalf("CreatePromotion() error = %v, want ErrStoreConflict", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CreatePromotion() never returned after the lock was released")
	}

	// And it wrote nothing: a refused decision leaves no row behind.
	history, err := store.ProjectPromotions(ctx, "proj-stale", "", MaxPromotionPage)
	if err != nil {
		t.Fatalf("ProjectPromotions() error = %v", err)
	}
	if len(history) != 0 {
		t.Errorf("a refused promotion left %d row(s) behind", len(history))
	}
}

// TestPostgresPromotionLocksTwoEnvironmentRowsAndNothingElse pins the scope of
// the lock structurally.
//
// The behavioural tests above show that disjoint pairs and different projects
// make progress, but they show it for today's implementation. A later change
// to a project-wide lock — the shortcut task 065's creation cap legitimately
// takes, and the one that would be easy to copy here — would keep every
// promotion correct while quietly serializing unrelated tenants, and only the
// timing-sensitive halves would notice. This reads the statements instead.
func TestPostgresPromotionLocksTwoEnvironmentRowsAndNothingElse(t *testing.T) {
	source := goSourceWithoutCommentsForTest(t, "postgres.go")

	// The lock is one row of platform_environments, identified by the pair.
	// Read from lockEnvironment's own body with whitespace collapsed, so
	// reindenting the SQL is not a failure while changing what it locks is.
	lock := strings.Join(strings.Fields(
		postgresFuncBodyForTest(t, source, "lockEnvironment")), " ")
	// The table is named by its constant in the source, so the assertion is on
	// the identifier rather than on the value it expands to.
	for _, want := range []string{
		`SELECT ref FROM `,
		`tableEnvironments`,
		`WHERE project_id = $1 AND ref = $2 FOR UPDATE`,
	} {
		if !strings.Contains(lock, want) {
			t.Errorf("lockEnvironment no longer contains %q; the lock must be the "+
				"one environment row identified by (project_id, ref)", want)
		}
	}

	// Issued once per environment, so the acquisition order is the code's
	// rather than the query planner's.
	for _, planner := range []string{"ref = ANY(", "ORDER BY ref FOR UPDATE"} {
		if strings.Contains(source, planner) {
			t.Errorf("postgres.go contains %q; a multi-row FOR UPDATE leaves the "+
				"lock order to the planner", planner)
		}
	}

	// And CreatePromotion takes exactly those, in the shared byte order, with
	// no project lock and no table lock alongside them.
	body := postgresFuncBodyForTest(t, source, "CreatePromotion")
	if !strings.Contains(body, "promotionEnvironmentOrder(promotion)") {
		t.Error("CreatePromotion no longer derives its lock order from " +
			"promotionEnvironmentOrder; the order would follow source and target")
	}
	if got := strings.Count(body, "lockEnvironment("); got != 1 {
		t.Errorf("CreatePromotion calls lockEnvironment %d times, want 1 (inside "+
			"the loop over both refs)", got)
	}
	for _, wider := range []string{"lockProjectForWrite", tableProjects, "LOCK TABLE"} {
		if strings.Contains(body, wider) {
			t.Errorf("CreatePromotion takes %q; the invariant names exactly two "+
				"environment rows, and a wider lock would serialize unrelated "+
				"promotions", wider)
		}
	}
}

// postgresFuncBodyForTest returns one top-level function's source text.
func postgresFuncBodyForTest(t *testing.T, source, name string) string {
	t.Helper()
	marker := ") " + name + "("
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("no method named %s in postgres.go", name)
	}
	// Top-level declarations close on a column-zero brace.
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of %s", name)
	}
	return source[start : start+end]
}
