package platform

// Shared SQLite/PostgreSQL conformance for task 074's four hierarchy
// collections.
//
// One suite, run against both backends, because the whole point of the
// collection contract is that a caller cannot tell which database answered.
// Ordering, cursor exclusivity, the limit range, the parent-missing-versus-
// empty distinction and continuation behaviour are asserted once here rather
// than twice in two backend files, which is where they would drift.
//
// Byte order is proved with a fixture whose identifiers sort differently under
// a locale-aware collation. On PostgreSQL that is what COLLATE "C" buys; on
// SQLite it is the default. A test that used only lowercase ASCII would pass
// under either and prove neither.

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// byteOrderIDs sort one way by byte value and another in most locales.
//
// "Zulu" < "_under" < "alpha" < "beta" by byte ("Z"=0x5A, "_"=0x5F, "a"=0x61),
// while a locale-aware collation typically ignores case and punctuation and
// produces alpha, beta, _under, Zulu.
var byteOrderIDs = []string{"Zulu", "alpha", "_under", "beta"}

var byteOrderSorted = []string{"Zulu", "_under", "alpha", "beta"}

func idsOf[T any](values []T, id func(T) string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, id(value))
	}
	return out
}

// conformProjectCollection covers the one unscoped collection.
func conformProjectCollection(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)

	// An empty platform is an empty page, not an error: there is no parent
	// that could be missing.
	empty, err := store.Projects(ctx, "", MaxListPage)
	if err != nil {
		t.Fatalf("Projects() on an empty platform error = %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty platform returned %d projects", len(empty))
	}

	for _, id := range byteOrderIDs {
		seedProject(t, store, id)
	}

	page, err := store.Projects(ctx, "", MaxListPage)
	if err != nil {
		t.Fatalf("Projects() error = %v", err)
	}
	got := idsOf(page, func(p Project) string { return string(p.ID()) })
	if fmt.Sprint(got) != fmt.Sprint(byteOrderSorted) {
		t.Errorf("order = %v, want %v (byte order, not locale order)", got, byteOrderSorted)
	}

	// The cursor is exclusive, and need not name a row that exists.
	after, err := store.Projects(ctx, "_under", MaxListPage)
	if err != nil {
		t.Fatalf("Projects(after) error = %v", err)
	}
	if fmt.Sprint(idsOf(after, func(p Project) string { return string(p.ID()) })) !=
		fmt.Sprint([]string{"alpha", "beta"}) {
		t.Errorf("after _under = %v, want [alpha beta]", after)
	}

	conformPageLimits(t, "Projects", func(limit int) error {
		_, err := store.Projects(ctx, "", limit)
		return err
	})
}

// conformAgentCollection covers the project-scoped agent collection.
func conformAgentCollection(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")
	seedProject(t, store, "proj-empty")

	// A project that does not exist and a project with nothing in it are
	// different answers, and a caller browsing a hierarchy acts differently on
	// each. An empty array cannot say which happened.
	if _, err := store.ProjectAgents(ctx, "proj-absent", "", 10); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("list for a missing project error = %v, want ErrStoreNotFound", err)
	}
	empty, err := store.ProjectAgents(ctx, "proj-empty", "", 10)
	if err != nil {
		t.Fatalf("list for an empty project error = %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty project returned %d agents", len(empty))
	}

	for _, id := range byteOrderIDs {
		if err := store.CreateAgent(ctx, mustAgent(t, AgentID(id), "proj-1", "A")); err != nil {
			t.Fatalf("create agent %s error = %v", id, err)
		}
	}
	// One agent in the other project, to prove the scope is real.
	if err := store.CreateAgent(ctx, mustAgent(t, "other", "proj-empty", "A")); err != nil {
		t.Fatalf("create agent in the second project error = %v", err)
	}

	page, err := store.ProjectAgents(ctx, "proj-1", "", MaxListPage)
	if err != nil {
		t.Fatalf("ProjectAgents() error = %v", err)
	}
	got := idsOf(page, func(a Agent) string { return string(a.ID()) })
	if fmt.Sprint(got) != fmt.Sprint(byteOrderSorted) {
		t.Errorf("order = %v, want %v (byte order)", got, byteOrderSorted)
	}
	for _, agent := range page {
		if agent.ProjectID() != "proj-1" {
			t.Errorf("agent %s belongs to %s; the scope leaked", agent.ID(), agent.ProjectID())
		}
	}

	after, err := store.ProjectAgents(ctx, "proj-1", "_under", MaxListPage)
	if err != nil {
		t.Fatalf("ProjectAgents(after) error = %v", err)
	}
	if fmt.Sprint(idsOf(after, func(a Agent) string { return string(a.ID()) })) !=
		fmt.Sprint([]string{"alpha", "beta"}) {
		t.Errorf("after _under = %v, want [alpha beta]", after)
	}

	conformPageLimits(t, "ProjectAgents", func(limit int) error {
		_, err := store.ProjectAgents(ctx, "proj-1", "", limit)
		return err
	})
}

// conformCandidateCollection covers the agent-scoped candidate collection.
func conformCandidateCollection(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")
	for _, agent := range []string{"agent-1", "agent-empty"} {
		if err := store.CreateAgent(ctx, mustAgent(t, AgentID(agent), "proj-1", "A")); err != nil {
			t.Fatalf("seed agent %s: %v", agent, err)
		}
	}

	if _, err := store.AgentCandidates(ctx, "agent-absent", "", 10); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("list for a missing agent error = %v, want ErrStoreNotFound", err)
	}
	empty, err := store.AgentCandidates(ctx, "agent-empty", "", 10)
	if err != nil {
		t.Fatalf("list for an empty agent error = %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty agent returned %d candidates", len(empty))
	}

	for _, id := range byteOrderIDs {
		if err := store.CreateCandidate(ctx,
			mustCandidate(t, CandidateID(id), "agent-1", CandidateMetadata{Label: "v" + id})); err != nil {
			t.Fatalf("create candidate %s error = %v", id, err)
		}
	}

	page, err := store.AgentCandidates(ctx, "agent-1", "", MaxListPage)
	if err != nil {
		t.Fatalf("AgentCandidates() error = %v", err)
	}
	got := idsOf(page, func(c Candidate) string { return string(c.ID()) })
	if fmt.Sprint(got) != fmt.Sprint(byteOrderSorted) {
		t.Errorf("order = %v, want %v (byte order)", got, byteOrderSorted)
	}

	// Metadata survives the listing intact — a collection element is the same
	// value a by-id read returns, not a reduced summary of one.
	for _, candidate := range page {
		if candidate.AgentID() != "agent-1" {
			t.Errorf("candidate %s belongs to %s", candidate.ID(), candidate.AgentID())
		}
		if want := "v" + string(candidate.ID()); candidate.Metadata().Label != want {
			t.Errorf("candidate %s label = %q, want %q",
				candidate.ID(), candidate.Metadata().Label, want)
		}
	}

	conformPageLimits(t, "AgentCandidates", func(limit int) error {
		_, err := store.AgentCandidates(ctx, "agent-1", "", limit)
		return err
	})
}

// conformRunCollection covers the candidate-scoped run collection.
func conformRunCollection(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedParents(t, store)
	seedCandidate(t, store)
	if err := store.CreateCandidate(ctx,
		mustCandidate(t, "cand-empty", "agent-1", CandidateMetadata{Label: "v0"})); err != nil {
		t.Fatalf("seed empty candidate: %v", err)
	}

	if _, err := store.CandidateEvaluationRuns(ctx, "cand-absent", "", 10); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("list for a missing candidate error = %v, want ErrStoreNotFound", err)
	}
	empty, err := store.CandidateEvaluationRuns(ctx, "cand-empty", "", 10)
	if err != nil {
		t.Fatalf("list for an empty candidate error = %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty candidate returned %d runs", len(empty))
	}

	for _, id := range byteOrderIDs {
		if err := store.CreateEvaluationRun(ctx,
			mustRun(t, EvaluationRunID(id), "cand-1", conformanceEpoch())); err != nil {
			t.Fatalf("create run %s error = %v", id, err)
		}
	}

	page, err := store.CandidateEvaluationRuns(ctx, "cand-1", "", MaxListPage)
	if err != nil {
		t.Fatalf("CandidateEvaluationRuns() error = %v", err)
	}
	got := idsOf(page, func(r EvaluationRun) string { return string(r.ID()) })
	if fmt.Sprint(got) != fmt.Sprint(byteOrderSorted) {
		t.Errorf("order = %v, want %v (byte order)", got, byteOrderSorted)
	}

	// A listed run is restored by replaying its lifecycle, exactly as a by-id
	// read is: its status and timestamps are the run's own, not defaults.
	for _, run := range page {
		if run.CandidateID() != "cand-1" {
			t.Errorf("run %s belongs to %s", run.ID(), run.CandidateID())
		}
		if run.Status() != RunPending {
			t.Errorf("run %s status = %s, want %s", run.ID(), run.Status(), RunPending)
		}
		if !run.CreatedAt().Equal(conformanceEpoch()) {
			t.Errorf("run %s created at %s, want %s", run.ID(), run.CreatedAt(), conformanceEpoch())
		}
	}

	// A lifecycle change is visible through the collection, because the
	// collection reads the same rows the by-id path does.
	started, err := page[0].Start(conformanceEpoch().Add(1))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, page[0], started); err != nil {
		t.Fatalf("UpdateEvaluationRun() error = %v", err)
	}
	reread, err := store.CandidateEvaluationRuns(ctx, "cand-1", "", MaxListPage)
	if err != nil {
		t.Fatalf("CandidateEvaluationRuns() re-read error = %v", err)
	}
	if reread[0].Status() != RunRunning {
		t.Errorf("after Start the listed status = %s, want %s", reread[0].Status(), RunRunning)
	}

	conformPageLimits(t, "CandidateEvaluationRuns", func(limit int) error {
		_, err := store.CandidateEvaluationRuns(ctx, "cand-1", "", limit)
		return err
	})
}

// conformPageLimits asserts the shared 1..MaxListPage range at the store edge.
//
// The upper bound matters as much as the lower. Task 066 found a transport
// asking the store for limit+1 to detect continuation, which had widened the
// store's public contract to 65 so one caller could look ahead. The store
// accepts the range it documents and nothing more; a transport that needs to
// know whether another page follows asks a second bounded question.
func conformPageLimits(t *testing.T, name string, list func(limit int) error) {
	t.Helper()
	for _, limit := range []int{0, -1, MaxListPage + 1, 1000} {
		if err := list(limit); !errors.Is(err, ErrInvalidID) {
			t.Errorf("%s(limit=%d) error = %v, want ErrInvalidID", name, limit, err)
		}
	}
	for _, limit := range []int{1, MaxListPage} {
		if err := list(limit); err != nil {
			t.Errorf("%s(limit=%d) error = %v, want success", name, limit, err)
		}
	}
}

// conformCollectionPagingEnumeratesEverything walks a collection larger than
// one page, the way the HTTP route does.
//
// 130 agents: more than two full pages, so the traversal cannot accidentally
// pass by getting the boundary right once. The continuation rule under test is
// the route's — a short page ends, a full page is resolved by a second bounded
// probe — and it must enumerate every row exactly once.
func conformCollectionPagingEnumeratesEverything(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")

	const total = 130
	want := make(map[string]bool, total)
	for i := range total {
		id := fmt.Sprintf("agent-%03d", i)
		want[id] = true
		if err := store.CreateAgent(ctx, mustAgent(t, AgentID(id), "proj-1", "A")); err != nil {
			t.Fatalf("seed agent %s: %v", id, err)
		}
	}

	seen := make(map[string]bool, total)
	after := AgentID("")
	pages := 0
	for {
		pages++
		if pages > total {
			t.Fatal("traversal did not terminate")
		}
		page, err := store.ProjectAgents(ctx, "proj-1", after, MaxListPage)
		if err != nil {
			t.Fatalf("ProjectAgents(after=%q) error = %v", after, err)
		}
		if len(page) > MaxListPage {
			t.Fatalf("page returned %d rows, over the %d bound", len(page), MaxListPage)
		}
		for _, agent := range page {
			id := string(agent.ID())
			if seen[id] {
				t.Errorf("agent %s appeared on two pages", id)
			}
			seen[id] = true
		}
		if len(page) < MaxListPage {
			break
		}
		last := page[len(page)-1].ID()
		probe, err := store.ProjectAgents(ctx, "proj-1", last, 1)
		if err != nil {
			t.Fatalf("continuation probe error = %v", err)
		}
		if len(probe) == 0 {
			break
		}
		after = last
	}

	if len(seen) != total {
		t.Errorf("enumerated %d agents, want %d", len(seen), total)
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("agent %s was never returned", id)
		}
	}
	if pages != 3 {
		t.Errorf("traversal took %d pages, want 3 for %d rows at %d per page",
			pages, total, MaxListPage)
	}
}

// conformCollectionConcurrentInsert covers a row created mid-traversal.
//
// The rule is positional, not temporal: a row appears if and only if its id
// sorts after the caller's cursor. Nothing already returned moves, because id
// is immutable — which is the property that makes an id cursor safe and a
// timestamp cursor not.
func conformCollectionConcurrentInsert(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")

	for _, id := range []string{"agent-1", "agent-3", "agent-5"} {
		if err := store.CreateAgent(ctx, mustAgent(t, AgentID(id), "proj-1", "A")); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	first, err := store.ProjectAgents(ctx, "proj-1", "", 2)
	if err != nil {
		t.Fatalf("first page error = %v", err)
	}
	if fmt.Sprint(idsOf(first, func(a Agent) string { return string(a.ID()) })) !=
		fmt.Sprint([]string{"agent-1", "agent-3"}) {
		t.Fatalf("first page = %v, want [agent-1 agent-3]", first)
	}

	// One row behind the cursor and one ahead of it, both created after the
	// first page was read.
	for _, id := range []string{"agent-2", "agent-4"} {
		if err := store.CreateAgent(ctx, mustAgent(t, AgentID(id), "proj-1", "A")); err != nil {
			t.Fatalf("concurrent create %s: %v", id, err)
		}
	}

	second, err := store.ProjectAgents(ctx, "proj-1", "agent-3", MaxListPage)
	if err != nil {
		t.Fatalf("second page error = %v", err)
	}
	got := idsOf(second, func(a Agent) string { return string(a.ID()) })
	// agent-2 sorts before the cursor and is correctly missed; agent-4 sorts
	// after it and is correctly seen. Neither is a bug: a keyset traversal
	// reports a consistent position, not a consistent snapshot.
	if fmt.Sprint(got) != fmt.Sprint([]string{"agent-4", "agent-5"}) {
		t.Errorf("second page = %v, want [agent-4 agent-5]", got)
	}
}
