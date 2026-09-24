package platform_test

// The control plane's half of task 065: environment operations, and the two
// cross-entity rules a registry makes possible.
//
// The rules are the interesting part. A run may only name an environment its
// own project owns and has not archived, and a comparison may only span one
// project — both checked where the entity graph is visible, and both before
// anything is written or read that they would otherwise waste.

import (
	"context"
	"errors"
	"testing"
	"time"

	platform "trustvian-platform"
)

// seedProjectOnly creates a project with no agents, for environment tests that
// need nothing else.
func seedProjectOnly(t *testing.T, f *controlPlaneFixture, id platform.ProjectID) {
	t.Helper()
	project, err := platform.NewProject(id, "P")
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	if err := f.plane.CreateProject(t.Context(), project); err != nil &&
		!errors.Is(err, platform.ErrStoreAlreadyExists) {
		t.Fatalf("CreateProject() error = %v", err)
	}
}

func createEnvironment(t *testing.T, f *controlPlaneFixture, project, ref, name string) platform.Environment {
	t.Helper()
	env, err := platform.NewEnvironment(
		platform.EnvironmentRef(ref), platform.ProjectID(project), name)
	if err != nil {
		t.Fatalf("NewEnvironment() error = %v", err)
	}
	if err := f.plane.CreateEnvironment(t.Context(), env); err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	return env
}

func TestEnvironmentServiceRoundTrip(t *testing.T) {
	f := newFixture(t)
	seedProjectOnly(t, f, "proj-1")
	createEnvironment(t, f, "proj-1", "staging", "Staging")

	loaded, err := f.plane.Environment(t.Context(), "proj-1", "staging")
	if err != nil {
		t.Fatalf("Environment() error = %v", err)
	}
	if loaded.Name() != "Staging" || loaded.Revision() != 1 {
		t.Errorf("loaded = %+v, want Staging at revision 1", loaded)
	}
}

func TestConfigureEnvironment(t *testing.T) {
	f := newFixture(t)
	seedProjectOnly(t, f, "proj-1")
	createEnvironment(t, f, "proj-1", "staging", "Staging")

	name := "Staging EU"
	rank := uint16(20)
	updated, err := f.plane.ConfigureEnvironment(t.Context(), "proj-1", "staging",
		platform.ConfigureEnvironmentRequest{Revision: 1, Name: &name, Rank: &rank})
	if err != nil {
		t.Fatalf("ConfigureEnvironment() error = %v", err)
	}
	if updated.Name() != name {
		t.Errorf("name = %q, want %q", updated.Name(), name)
	}
	if got, ranked := updated.Rank(); !ranked || got != rank {
		t.Errorf("rank = (%d, %t), want (%d, true)", got, ranked, rank)
	}

	// A stale revision is refused and changes nothing.
	if _, err := f.plane.ConfigureEnvironment(t.Context(), "proj-1", "staging",
		platform.ConfigureEnvironmentRequest{Revision: 1, Name: &name}); !errors.Is(
		err, platform.ErrStoreConflict) {
		t.Errorf("stale configure error = %v, want ErrStoreConflict", err)
	}

	// Clearing the rank is its own intention, and cannot be combined with
	// setting one.
	current, _ := f.plane.Environment(t.Context(), "proj-1", "staging")
	cleared, err := f.plane.ConfigureEnvironment(t.Context(), "proj-1", "staging",
		platform.ConfigureEnvironmentRequest{Revision: current.Revision(), ClearRank: true})
	if err != nil {
		t.Fatalf("clear rank error = %v", err)
	}
	if _, ranked := cleared.Rank(); ranked {
		t.Error("the rank survived a clear")
	}
	if _, err := f.plane.ConfigureEnvironment(t.Context(), "proj-1", "staging",
		platform.ConfigureEnvironmentRequest{
			Revision: cleared.Revision(), Rank: &rank, ClearRank: true,
		}); err == nil {
		t.Error("rank and clear_rank together were accepted; they are opposite intentions")
	}

	// A configure that changes nothing is a caller mistake rather than a
	// no-op write that burns a revision.
	if _, err := f.plane.ConfigureEnvironment(t.Context(), "proj-1", "staging",
		platform.ConfigureEnvironmentRequest{Revision: cleared.Revision()}); err == nil {
		t.Error("an empty configure was accepted")
	}
}

func TestArchiveAndActivateEnvironment(t *testing.T) {
	f := newFixture(t)
	seedProjectOnly(t, f, "proj-1")
	createEnvironment(t, f, "proj-1", "staging", "Staging")

	archived, err := f.plane.ArchiveEnvironment(t.Context(), "proj-1", "staging", 1)
	if err != nil {
		t.Fatalf("ArchiveEnvironment() error = %v", err)
	}
	if archived.Status() != platform.EnvironmentArchived {
		t.Errorf("status = %q, want archived", archived.Status())
	}
	if _, err := f.plane.ArchiveEnvironment(t.Context(), "proj-1", "staging", 1); !errors.Is(
		err, platform.ErrStoreConflict) {
		t.Errorf("stale archive error = %v, want ErrStoreConflict", err)
	}

	reactivated, err := f.plane.ActivateEnvironment(
		t.Context(), "proj-1", "staging", archived.Revision())
	if err != nil {
		t.Fatalf("ActivateEnvironment() error = %v", err)
	}
	if reactivated.Status() != platform.EnvironmentActive {
		t.Errorf("status = %q, want active", reactivated.Status())
	}
}

func TestPromotionOrderThroughTheService(t *testing.T) {
	f := newFixture(t)
	seedProjectOnly(t, f, "proj-1")
	createEnvironment(t, f, "proj-1", "staging", "Staging")
	createEnvironment(t, f, "proj-1", "production", "Production")

	rank := func(ref string, value uint16) {
		t.Helper()
		current, err := f.plane.Environment(t.Context(), "proj-1", platform.EnvironmentRef(ref))
		if err != nil {
			t.Fatalf("Environment(%s) error = %v", ref, err)
		}
		if _, err := f.plane.ConfigureEnvironment(t.Context(), "proj-1",
			platform.EnvironmentRef(ref),
			platform.ConfigureEnvironmentRequest{Revision: current.Revision(), Rank: &value}); err != nil {
			t.Fatalf("ranking %s error = %v", ref, err)
		}
	}
	rank("staging", 10)
	rank("production", 20)

	forward, err := f.plane.PromotionOrder(t.Context(), "proj-1", "staging", "production")
	if err != nil {
		t.Fatalf("PromotionOrder() error = %v", err)
	}
	if !forward {
		t.Error("staging → production reported as not ordered")
	}
	backward, err := f.plane.PromotionOrder(t.Context(), "proj-1", "production", "staging")
	if err != nil {
		t.Fatalf("PromotionOrder() error = %v", err)
	}
	if backward {
		t.Error("production → staging reported as ordered")
	}
	if _, err := f.plane.PromotionOrder(t.Context(), "proj-1", "staging", "absent"); !errors.Is(
		err, platform.ErrStoreNotFound) {
		t.Errorf("PromotionOrder with a missing environment error = %v, want ErrStoreNotFound", err)
	}
}

// ---------------------------------------------------------------------
// Rule 1: a run names an environment its project owns
// ---------------------------------------------------------------------

// seedHierarchyOnly creates project, agent and candidate — everything a run
// needs except the environment, which each test supplies or withholds.
func seedHierarchyOnly(t *testing.T, f *controlPlaneFixture, project, agent, candidate string) {
	t.Helper()
	ctx := t.Context()
	p, _ := platform.NewProject(platform.ProjectID(project), "P")
	a, _ := platform.NewAgent(platform.AgentID(agent), platform.ProjectID(project), "A")
	c, _ := platform.NewCandidate(
		platform.CandidateID(candidate), platform.AgentID(agent), platform.CandidateMetadata{})
	for _, err := range []error{
		f.plane.CreateProject(ctx, p),
		f.plane.CreateAgent(ctx, a),
		f.plane.CreateCandidate(ctx, c),
	} {
		if err != nil && !errors.Is(err, platform.ErrStoreAlreadyExists) {
			t.Fatalf("seed error = %v", err)
		}
	}
}

func newEnvRun(t *testing.T, id, candidate, environment string) platform.EvaluationRun {
	t.Helper()
	run, err := platform.NewEvaluationRun(
		platform.EvaluationRunID(id), platform.CandidateID(candidate),
		platform.EnvironmentRef(environment), "profile-1",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	return run
}

func TestCreateEvaluationRunRequiresARegisteredEnvironment(t *testing.T) {
	f := newFixture(t)
	seedHierarchyOnly(t, f, "proj-1", "agent-1", "cand-1")

	// Missing: the typo case the registry exists to catch.
	err := f.plane.CreateEvaluationRun(t.Context(), newEnvRun(t, "run-1", "cand-1", "stagin"))
	if !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("run against an unregistered environment error = %v, want ErrStoreNotFound", err)
	}
	if _, err := f.plane.EvaluationRun(t.Context(), "run-1"); !errors.Is(
		err, platform.ErrStoreNotFound) {
		t.Error("the refused run was written anyway")
	}

	// Registered and active: accepted.
	createEnvironment(t, f, "proj-1", "staging", "Staging")
	if err := f.plane.CreateEvaluationRun(
		t.Context(), newEnvRun(t, "run-1", "cand-1", "staging")); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
}

func TestCreateEvaluationRunRefusesAnArchivedEnvironment(t *testing.T) {
	f := newFixture(t)
	seedHierarchyOnly(t, f, "proj-1", "agent-1", "cand-1")
	createEnvironment(t, f, "proj-1", "staging", "Staging")
	if _, err := f.plane.ArchiveEnvironment(t.Context(), "proj-1", "staging", 1); err != nil {
		t.Fatalf("ArchiveEnvironment() error = %v", err)
	}

	err := f.plane.CreateEvaluationRun(t.Context(), newEnvRun(t, "run-1", "cand-1", "staging"))
	if !errors.Is(err, platform.ErrEnvironmentUnavailable) {
		t.Errorf("run against an archived environment error = %v, want ErrEnvironmentUnavailable", err)
	}
	if _, err := f.plane.EvaluationRun(t.Context(), "run-1"); !errors.Is(
		err, platform.ErrStoreNotFound) {
		t.Error("the refused run was written anyway")
	}
}

// The ref exists — in another project. Identity is the pair, so from this
// run's side it does not exist at all.
func TestCreateEvaluationRunRefusesAnotherProjectsEnvironment(t *testing.T) {
	f := newFixture(t)
	seedHierarchyOnly(t, f, "proj-1", "agent-1", "cand-1")
	seedProjectOnly(t, f, "proj-2")
	createEnvironment(t, f, "proj-2", "staging", "Staging")

	err := f.plane.CreateEvaluationRun(t.Context(), newEnvRun(t, "run-1", "cand-1", "staging"))
	if !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("run against another project's environment error = %v, want ErrStoreNotFound", err)
	}
}

// Archiving is about what may start. A run already underway keeps running,
// ingesting and completing.
func TestArchivingDoesNotDisturbARunningRun(t *testing.T) {
	f := newFixture(t)
	seedHierarchyOnly(t, f, "proj-1", "agent-1", "cand-1")
	createEnvironment(t, f, "proj-1", "staging", "Staging")

	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := f.plane.CreateEvaluationRun(
		t.Context(), newEnvRun(t, "run-1", "cand-1", "staging")); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	if _, err := f.plane.StartEvaluationRun(t.Context(), "run-1", epoch.Add(time.Minute)); err != nil {
		t.Fatalf("StartEvaluationRun() error = %v", err)
	}
	if _, err := f.plane.ArchiveEnvironment(t.Context(), "proj-1", "staging", 1); err != nil {
		t.Fatalf("ArchiveEnvironment() error = %v", err)
	}

	// Ingest resolves no environment, so it is unaffected.
	if _, err := f.plane.IngestDecisionRecord(t.Context(), platform.IngestRequest{
		RunID: "run-1", Sequence: 1, BehavioralProfile: "profile-1",
		Record: ingestRecord("evt-1", "fp-1", "read"),
	}); err != nil {
		t.Errorf("ingest into a run whose environment was archived error = %v", err)
	}
	if _, err := f.plane.CompleteEvaluationRun(t.Context(), "run-1", epoch.Add(time.Hour)); err != nil {
		t.Errorf("completing a run whose environment was archived error = %v", err)
	}

	// And a *new* run against it is refused, which is the whole point.
	seedHierarchyOnly(t, f, "proj-1", "agent-1", "cand-2")
	if err := f.plane.CreateEvaluationRun(
		t.Context(), newEnvRun(t, "run-2", "cand-2", "staging")); !errors.Is(
		err, platform.ErrEnvironmentUnavailable) {
		t.Errorf("new run after archiving error = %v, want ErrEnvironmentUnavailable", err)
	}
}

// ---------------------------------------------------------------------
// Rule 2: a comparison stays inside one project
// ---------------------------------------------------------------------

// evidenceTripwire fails the test if evidence is read.
//
// The refusal must happen before any evidence load: four reads saved on a
// request that can never succeed, and no partial work on a pairing that is
// refused.
type evidenceTripwire struct {
	platform.EvaluationStore
	t *testing.T
}

func (e evidenceTripwire) EvaluationEvidence(
	ctx context.Context, id platform.EvaluationRunID,
) (platform.EvaluationAggregate, platform.BehaviorSnapshot, error) {
	e.t.Errorf("evidence for %s was loaded before the comparison's scope was checked", id)
	return e.EvaluationStore.EvaluationEvidence(ctx, id)
}

func TestCompareEvaluationsRefusesTwoProjects(t *testing.T) {
	store, err := platform.OpenSQLiteStore(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	plane, err := platform.NewControlPlane(store, evidenceTripwire{store, t}, store)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}

	// Two projects, each owning a "staging", each with a completed run there.
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, h := range []struct{ project, agent, candidate, run string }{
		{"proj-a", "agent-a", "cand-a", "run-a"},
		{"proj-b", "agent-b", "cand-b", "run-b"},
	} {
		p, _ := platform.NewProject(platform.ProjectID(h.project), "P")
		a, _ := platform.NewAgent(platform.AgentID(h.agent), platform.ProjectID(h.project), "A")
		c, _ := platform.NewCandidate(
			platform.CandidateID(h.candidate), platform.AgentID(h.agent), platform.CandidateMetadata{})
		env, _ := platform.NewEnvironment("staging", platform.ProjectID(h.project), "Staging")
		run, _ := platform.NewEvaluationRun(
			platform.EvaluationRunID(h.run), platform.CandidateID(h.candidate),
			"staging", "profile-1", epoch)
		for _, err := range []error{
			plane.CreateProject(t.Context(), p),
			plane.CreateAgent(t.Context(), a),
			plane.CreateCandidate(t.Context(), c),
			plane.CreateEnvironment(t.Context(), env),
			plane.CreateEvaluationRun(t.Context(), run),
		} {
			if err != nil {
				t.Fatalf("seed %s error = %v", h.run, err)
			}
		}
		if _, err := plane.StartEvaluationRun(t.Context(),
			platform.EvaluationRunID(h.run), epoch.Add(time.Minute)); err != nil {
			t.Fatalf("start %s error = %v", h.run, err)
		}
		if _, err := plane.CompleteEvaluationRun(t.Context(),
			platform.EvaluationRunID(h.run), epoch.Add(time.Hour)); err != nil {
			t.Fatalf("complete %s error = %v", h.run, err)
		}
	}

	_, err = plane.CompareEvaluations(t.Context(), "run-a", "run-b", platform.EvaluationGateLimits{})
	if !errors.Is(err, platform.ErrComparisonScope) {
		t.Fatalf("cross-project comparison error = %v, want ErrComparisonScope", err)
	}
}

// Same project, different agents, stays allowed: the ambiguity project-scoped
// refs create sits at the project boundary, and nothing narrower was closed.
func TestCompareEvaluationsAllowsTwoAgentsInOneProject(t *testing.T) {
	f := newFixture(t)
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedProjectOnly(t, f, "proj-1")
	createEnvironment(t, f, "proj-1", "staging", "Staging")

	for _, h := range []struct{ agent, candidate, run string }{
		{"agent-1", "cand-1", "run-1"},
		{"agent-2", "cand-2", "run-2"},
	} {
		a, _ := platform.NewAgent(platform.AgentID(h.agent), "proj-1", "A")
		c, _ := platform.NewCandidate(
			platform.CandidateID(h.candidate), platform.AgentID(h.agent), platform.CandidateMetadata{})
		for _, err := range []error{
			f.plane.CreateAgent(t.Context(), a),
			f.plane.CreateCandidate(t.Context(), c),
			f.plane.CreateEvaluationRun(t.Context(), newEnvRun(t, h.run, h.candidate, "staging")),
		} {
			if err != nil {
				t.Fatalf("seed %s error = %v", h.run, err)
			}
		}
		if _, err := f.plane.StartEvaluationRun(t.Context(),
			platform.EvaluationRunID(h.run), epoch.Add(time.Minute)); err != nil {
			t.Fatalf("start %s error = %v", h.run, err)
		}
		if _, err := f.ingest(t, platform.EvaluationRunID(h.run), 1,
			ingestRecord(h.run+"-evt", "fp-read", "read")); err != nil {
			t.Fatalf("ingest %s error = %v", h.run, err)
		}
		if _, err := f.plane.CompleteEvaluationRun(t.Context(),
			platform.EvaluationRunID(h.run), epoch.Add(time.Hour)); err != nil {
			t.Fatalf("complete %s error = %v", h.run, err)
		}
	}

	if _, err := f.plane.CompareEvaluations(
		t.Context(), "run-1", "run-2", platform.EvaluationGateLimits{}); err != nil {
		t.Errorf("same-project comparison across two agents error = %v, want it allowed", err)
	}
}
