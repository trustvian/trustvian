package platform

// Task 102: listing recorded scenario executions, on every backend.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func conformScenarioExecutionList(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	ctx := t.Context()
	// Completion serializes on the project row, so the project must exist.
	seedParents(t, store)
	t0 := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	staging := ScenarioScope{ProjectID: "proj-1", AgentID: "agent-1", Environment: "staging"}
	local := ScenarioScope{ProjectID: "proj-1", AgentID: "agent-2", Environment: "local"}
	other := ScenarioScope{ProjectID: "proj-2", AgentID: "agent-x", Environment: "staging"}
	for _, e := range []struct {
		id, name string
		scope    ScenarioScope
		offset   time.Duration
	}{
		{"e-1", "support", staging, 0},
		{"e-2", "billing", staging, time.Second},
		{"e-3", "support", local, 2 * time.Second},
		{"e-tie-a", "support", staging, 3 * time.Second},
		{"e-tie-b", "support", staging, 3 * time.Second},
		{"e-x", "support", other, 4 * time.Second},
	} {
		if err := store.CreateScenarioExecution(ctx,
			newRunningExecution(t, e.id, e.name, e.scope, 2, "", t0.Add(e.offset))); err != nil {
			t.Fatalf("create %s: %v", e.id, err)
		}
	}
	if _, err := store.CompleteScenarioExecution(ctx, "e-1", associations("e1", 2), GateVerdictPass,
		t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailScenarioExecution(ctx, "e-2", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	lister := store.(ScenarioExecutionListStore)
	read := func(filter ScenarioExecutionFilter, limit int) []string {
		t.Helper()
		var ids []string
		after := RecencyCursor{}
		for range 20 {
			page, err := lister.RecentScenarioExecutions(ctx, filter, after, limit)
			if err != nil {
				t.Fatalf("RecentScenarioExecutions(%+v) error = %v", filter, err)
			}
			if len(page) == 0 {
				return ids
			}
			for _, e := range page {
				ids = append(ids, string(e.Execution.ID()))
			}
			last := page[len(page)-1]
			after = RecencyCursor{Key: last.Key, ID: string(last.Execution.ID())}
		}
		t.Fatal("never reached an empty page")
		return nil
	}

	for _, tc := range []struct {
		name   string
		filter ScenarioExecutionFilter
		want   string
	}{
		{"project", ScenarioExecutionFilter{ProjectID: "proj-1"}, "e-tie-b,e-tie-a,e-3,e-2,e-1"},
		{"agent", ScenarioExecutionFilter{ProjectID: "proj-1", AgentID: "agent-1"}, "e-tie-b,e-tie-a,e-2,e-1"},
		{"environment", ScenarioExecutionFilter{ProjectID: "proj-1", Environment: "local"}, "e-3"},
		{"scenario", ScenarioExecutionFilter{ProjectID: "proj-1", ScenarioName: "billing"}, "e-2"},
		{"all three", ScenarioExecutionFilter{ProjectID: "proj-1", AgentID: "agent-1", Environment: "staging", ScenarioName: "support"}, "e-tie-b,e-tie-a,e-1"},
		{"unknown project", ScenarioExecutionFilter{ProjectID: "proj-none"}, ""},
	} {
		for _, limit := range []int{1, 2, MaxListPage} {
			if got := strings.Join(read(tc.filter, limit), ","); got != tc.want {
				t.Errorf("%s at limit %d = %s, want %s", tc.name, limit, got, tc.want)
			}
		}
	}

	page, err := lister.RecentScenarioExecutions(ctx, ScenarioExecutionFilter{ProjectID: "proj-1", ScenarioName: "support",
		AgentID: "agent-1", Environment: "staging"}, RecencyCursor{}, MaxListPage)
	if err != nil {
		t.Fatal(err)
	}
	completed := page[len(page)-1].Execution
	if completed.ID() != "e-1" || completed.Status() != ScenarioExecutionCompleted ||
		completed.Verdict() != GateVerdictPass || completed.Runs() != 2 || completed.FinishedAt().IsZero() {
		t.Errorf("listed completed execution lost fields: %+v", completed)
	}
}

func TestRecentScenarioExecutionsValidatesItsFilter(t *testing.T) {
	store, _ := testStore(t)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		filter ScenarioExecutionFilter
		after  string
		limit  int
		want   error
	}{
		{"no project", ScenarioExecutionFilter{}, "", 1, ErrInvalidID},
		{"agent with whitespace", ScenarioExecutionFilter{ProjectID: "p", AgentID: " a"}, "", 1, ErrInvalidID},
		{"zero limit", ScenarioExecutionFilter{ProjectID: "p"}, "", 0, ErrInvalidID},
		{"bad cursor", ScenarioExecutionFilter{ProjectID: "p"}, "e-1", 1, ErrObservationCursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := plane.RecentScenarioExecutions(t.Context(), tc.filter, tc.after, tc.limit); !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
}
