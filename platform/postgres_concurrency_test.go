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
