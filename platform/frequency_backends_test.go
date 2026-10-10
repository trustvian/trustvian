package platform

// Task 106's golden repeated comparison over a fixed record set, through real
// ingest and CompareRepeatedEvaluations, on SQLite and — when a DSN is set —
// PostgreSQL. Both must return the same figures.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
)

// frequencyBackendFixture is two repetitions a side, as observation counts:
//
//	        ref-1 ref-2 | can-1 can-2
//	crm        3     3  |    7     8
//	send       1     1  |    1     —
func driveFrequencyFixture(t *testing.T, store Store) RepeatedEvaluationComparison {
	t.Helper()
	ctx := context.Background()
	seedParents(t, store)
	seedCandidate(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	epoch := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	runs := []struct {
		id    EvaluationRunID
		calls map[string]int
	}{
		{"ref-1", map[string]int{"crm": 3, "send": 1}},
		{"ref-2", map[string]int{"crm": 3, "send": 1}},
		{"can-1", map[string]int{"crm": 7, "send": 1}},
		{"can-2", map[string]int{"crm": 8}},
	}
	for _, r := range runs {
		run, err := NewEvaluationRun(r.id, "cand-1", "staging", BehavioralProfileRef(string(r.id)+"-profile"), epoch)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CreateEvaluationRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		started, err := run.Start(epoch.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateEvaluationRun(ctx, run, started); err != nil {
			t.Fatal(err)
		}
		sequence := uint64(0)
		for _, op := range []string{"crm", "send"} {
			for range r.calls[op] {
				sequence++
				record := operationalTestRecord(started, fmt.Sprintf("%s-%d", r.id, sequence), "", event.StatusOK)
				record.FingerprintID = "fp-" + op
				record.Behavior.OperationName = op
				record.Behavior.TargetName = op + ".internal"
				// Task 081: crm is a named tool, send a transport call, and
				// can-1's first crm call arrived without its convention.
				fidelity, layer := event.FidelitySemantic, event.LayerTool
				if op == "send" || (r.id == "can-1" && sequence == 1) {
					fidelity, layer = event.FidelityTransport, event.LayerTransport
				}
				if _, err := plane.IngestDecisionRecord(ctx, IngestRequest{
					RunID: r.id, Sequence: sequence, BehavioralProfile: started.BehavioralProfile(), Record: record,
					Fidelity: fidelity, BehaviorLayer: layer,
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err := plane.CompleteEvaluationRun(ctx, r.id, epoch.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := plane.CompareRepeatedEvaluations(ctx, RepeatedEvaluationRequest{
		ReferenceRunIDs: []EvaluationRunID{"ref-1", "ref-2"},
		CandidateRunIDs: []EvaluationRunID{"can-1", "can-2"},
		Limits:          repeatedTestLimits(2, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertFrequencyGolden(t *testing.T, c RepeatedEvaluationComparison) {
	t.Helper()
	crm, send := behaviorOf(t, c, "fp-crm"), behaviorOf(t, c, "fp-send")
	if crm.Reference != stats(2, 6, 3, 3, 3000) || crm.Candidate != stats(2, 15, 7, 8, 7500) || crm.Lost {
		t.Errorf("crm = %+v / %+v lost %v", crm.Reference, crm.Candidate, crm.Lost)
	}
	if send.Reference != stats(2, 2, 1, 1, 1000) || send.Candidate != stats(2, 1, 0, 1, 500) || !send.Lost {
		t.Errorf("send = %+v / %+v lost %v", send.Reference, send.Candidate, send.Lost)
	}
	// 15 × 1000 / 6 = 2500 permille; 1 × 1000 / 2 = 500.
	if r := targetOf(t, c, "crm.internal"); !r.RatioAvailable || r.CallRatioPermille != 2500 {
		t.Errorf("crm.internal = %+v", r)
	}
	if r := targetOf(t, c, "send.internal"); !r.RatioAvailable || r.CallRatioPermille != 500 {
		t.Errorf("send.internal = %+v", r)
	}
	// Task 081: summed per side, then classified.
	for _, tt := range []struct {
		name      string
		got, want BehaviorFidelity
		level     FidelityLevel
		mixed     bool
	}{
		{"crm reference", crm.ReferenceFidelity, BehaviorFidelity{Semantic: 6, LayerTool: 6}, FidelityLevelSemantic, false},
		{"crm candidate", crm.CandidateFidelity, BehaviorFidelity{Semantic: 14, Transport: 1, LayerTool: 14, LayerTransport: 1},
			FidelityLevelTransport, true},
		{"send candidate", send.CandidateFidelity, BehaviorFidelity{Transport: 1, LayerTransport: 1},
			FidelityLevelTransport, false},
	} {
		level, mixed := tt.got.Reported()
		if tt.got != tt.want || level != tt.level || mixed != tt.mixed {
			t.Errorf("%s = %+v (%s, mixed %v); want %+v (%s, mixed %v)",
				tt.name, tt.got, level, mixed, tt.want, tt.level, tt.mixed)
		}
	}
}

func TestFrequencyGoldenOnBothBackends(t *testing.T) {
	sqliteStore, err := OpenSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	fromSQLite := driveFrequencyFixture(t, sqliteStore)
	assertFrequencyGolden(t, fromSQLite)

	dsn := strings.TrimSpace(conformancePostgresDSN())
	if dsn == "" {
		t.Skipf("%s is not set; the PostgreSQL half needs a database", postgresDSNEnv)
	}
	postgresStore, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: isolatedSchemaDSN(t, dsn)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = postgresStore.Close() })
	fromPostgres := driveFrequencyFixture(t, postgresStore)
	assertFrequencyGolden(t, fromPostgres)
	if a, b := fmt.Sprintf("%+v %+v", fromSQLite.Behaviors, fromSQLite.Targets),
		fmt.Sprintf("%+v %+v", fromPostgres.Behaviors, fromPostgres.Targets); a != b {
		t.Fatalf("the backends disagree:\n sqlite   %s\n postgres %s", a, b)
	}
}
