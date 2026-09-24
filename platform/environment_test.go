package platform_test

// The environment domain: construction, validation, transitions, and the one
// ordering primitive.
//
// Task 065's acceptance criteria in test form. What these prove beyond the
// obvious: a transition never mutates its receiver, every successful one
// advances the revision by exactly one, and rank 0 is a rank rather than a
// synonym for unranked.

import (
	"strings"
	"testing"

	platform "trustvian-platform"
)

func mustEnvironment(t *testing.T, ref, project, name string) platform.Environment {
	t.Helper()
	env, err := platform.NewEnvironment(
		platform.EnvironmentRef(ref), platform.ProjectID(project), name)
	if err != nil {
		t.Fatalf("NewEnvironment() error = %v", err)
	}
	return env
}

func rankedEnvironment(t *testing.T, ref, project string, rank uint16) platform.Environment {
	t.Helper()
	env, err := platform.NewRankedEnvironment(
		platform.EnvironmentRef(ref), platform.ProjectID(project), "Name", rank)
	if err != nil {
		t.Fatalf("NewRankedEnvironment() error = %v", err)
	}
	return env
}

func TestNewEnvironmentDefaults(t *testing.T) {
	env := mustEnvironment(t, "staging", "proj-1", "Staging")

	if env.Ref() != "staging" || env.ProjectID() != "proj-1" || env.Name() != "Staging" {
		t.Errorf("environment = %+v, want the values it was constructed with", env)
	}
	if env.Status() != platform.EnvironmentActive {
		t.Errorf("status = %q, want active", env.Status())
	}
	if _, ranked := env.Rank(); ranked {
		t.Error("a new environment is ranked; an ordering nobody chose is worse than none")
	}
	if env.Revision() != 1 {
		t.Errorf("revision = %d, want 1", env.Revision())
	}
}

func TestNewEnvironmentValidation(t *testing.T) {
	long := strings.Repeat("e", 257)
	tests := []struct {
		name               string
		ref, project, envn string
	}{
		{"empty ref", "", "proj-1", "Staging"},
		{"empty project", "staging", "", "Staging"},
		{"empty name", "staging", "proj-1", ""},
		{"whitespace-only name", "staging", "proj-1", "   "},
		{"ref with leading space", " staging", "proj-1", "Staging"},
		{"ref with trailing space", "staging ", "proj-1", "Staging"},
		{"ref with a control character", "stag\ning", "proj-1", "Staging"},
		{"name with a control character", "staging", "proj-1", "Stag\x00ing"},
		{"ref over the length bound", long, "proj-1", "Staging"},
		{"name over the length bound", "staging", "proj-1", long},
		{"invalid UTF-8 in ref", "stag\xffing", "proj-1", "Staging"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := platform.NewEnvironment(
				platform.EnvironmentRef(tt.ref), platform.ProjectID(tt.project), tt.envn); err == nil {
				t.Fatal("NewEnvironment() error = nil, want a rejection")
			}
		})
	}
}

func TestEnvironmentRankBounds(t *testing.T) {
	env := mustEnvironment(t, "staging", "proj-1", "Staging")

	for _, rank := range []uint16{0, 1, 9999} {
		ranked, err := env.WithRank(rank)
		if err != nil {
			t.Fatalf("WithRank(%d) error = %v, want it accepted", rank, err)
		}
		got, isRanked := ranked.Rank()
		if !isRanked || got != rank {
			t.Errorf("Rank() = (%d, %t), want (%d, true)", got, isRanked, rank)
		}
	}

	// Rank 0 is a rank, not a synonym for unranked — the reason the domain
	// carries a separate flag instead of a sentinel.
	zero, _ := env.WithRank(0)
	if _, ranked := zero.Rank(); !ranked {
		t.Error("rank 0 reported as unranked; zero is a position, absence is not")
	}

	if _, err := env.WithRank(10000); err == nil {
		t.Error("WithRank(10000) error = nil, want the bound enforced")
	}
}

// Transitions return a new value and leave the receiver untouched — the
// immutability proof .claude/rules/testing.md requires for a value type.
func TestEnvironmentTransitionsDoNotMutateTheReceiver(t *testing.T) {
	original := mustEnvironment(t, "staging", "proj-1", "Staging")

	renamed, err := original.Rename("Staging EU")
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	ranked, err := original.WithRank(20)
	if err != nil {
		t.Fatalf("WithRank() error = %v", err)
	}
	archived, err := original.Archive()
	if err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	cleared := original.WithoutRank()

	if original.Name() != "Staging" {
		t.Errorf("Rename mutated the receiver: name = %q", original.Name())
	}
	if _, isRanked := original.Rank(); isRanked {
		t.Error("WithRank mutated the receiver")
	}
	if original.Status() != platform.EnvironmentActive {
		t.Errorf("Archive mutated the receiver: status = %q", original.Status())
	}
	if original.Revision() != 1 {
		t.Errorf("a transition mutated the receiver's revision: %d", original.Revision())
	}

	for name, derived := range map[string]platform.Environment{
		"rename": renamed, "rank": ranked, "archive": archived, "clear rank": cleared,
	} {
		if derived.Revision() != 2 {
			t.Errorf("%s produced revision %d, want exactly one more than 1",
				name, derived.Revision())
		}
	}
}

func TestEnvironmentTransitionsAdvanceTheRevisionOnce(t *testing.T) {
	env := mustEnvironment(t, "staging", "proj-1", "Staging")
	steps := []struct {
		name  string
		apply func(platform.Environment) (platform.Environment, error)
	}{
		{"rename", func(e platform.Environment) (platform.Environment, error) { return e.Rename("A") }},
		{"rank", func(e platform.Environment) (platform.Environment, error) { return e.WithRank(10) }},
		{"clear rank", func(e platform.Environment) (platform.Environment, error) { return e.WithoutRank(), nil }},
		{"archive", func(e platform.Environment) (platform.Environment, error) { return e.Archive() }},
		{"activate", func(e platform.Environment) (platform.Environment, error) { return e.Activate() }},
	}
	for i, step := range steps {
		next, err := step.apply(env)
		if err != nil {
			t.Fatalf("%s error = %v", step.name, err)
		}
		if want := uint64(i + 2); next.Revision() != want {
			t.Fatalf("after %s revision = %d, want %d", step.name, next.Revision(), want)
		}
		env = next
	}

	// Clearing an already-unranked environment still produces a new
	// revision: a no-op that kept the number would let a compare-and-swap
	// succeed without anything having changed.
	unranked := mustEnvironment(t, "dev", "proj-1", "Dev")
	if cleared := unranked.WithoutRank(); cleared.Revision() != unranked.Revision()+1 {
		t.Errorf("WithoutRank on an unranked environment left revision %d, want %d",
			cleared.Revision(), unranked.Revision()+1)
	}
}

func TestEnvironmentArchiveAndActivateAreInverse(t *testing.T) {
	env := mustEnvironment(t, "staging", "proj-1", "Staging")

	archived, err := env.Archive()
	if err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	if archived.Status() != platform.EnvironmentArchived {
		t.Errorf("status = %q, want archived", archived.Status())
	}
	reactivated, err := archived.Activate()
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if reactivated.Status() != platform.EnvironmentActive {
		t.Errorf("status = %q, want active", reactivated.Status())
	}
	if reactivated.Revision() != 3 {
		t.Errorf("revision = %d, want 3 after two transitions", reactivated.Revision())
	}
}

func TestNewRankedEnvironmentIsStillRevisionOne(t *testing.T) {
	env := rankedEnvironment(t, "staging", "proj-1", 20)
	if env.Revision() != 1 {
		t.Errorf("revision = %d, want 1: a create is one act", env.Revision())
	}
	rank, ranked := env.Rank()
	if !ranked || rank != 20 {
		t.Errorf("Rank() = (%d, %t), want (20, true)", rank, ranked)
	}
	if _, err := platform.NewRankedEnvironment("staging", "proj-1", "Staging", 10000); err == nil {
		t.Error("NewRankedEnvironment(10000) error = nil, want the bound enforced")
	}
}

// ---------------------------------------------------------------------
// Ordering
// ---------------------------------------------------------------------

func TestCanPromoteTruthTable(t *testing.T) {
	ten := rankedEnvironment(t, "staging", "proj-1", 10)
	twenty := rankedEnvironment(t, "production", "proj-1", 20)
	otherTen := rankedEnvironment(t, "staging-eu", "proj-1", 10)
	unranked := mustEnvironment(t, "scratch", "proj-1", "Scratch")
	otherProject := rankedEnvironment(t, "production", "proj-2", 20)

	archivedTarget, _ := twenty.Archive()
	archivedSource, _ := ten.Archive()

	tests := []struct {
		name     string
		from, to platform.Environment
		want     bool
		why      string
	}{
		{"forward", ten, twenty, true, "a higher rank is the next stage"},
		{"same environment", ten, ten, false, "equal ranks are not strictly greater"},
		{"equal ranks", ten, otherTen, false, "same rank means peers, and a peer is not a next stage"},
		{"backward", twenty, ten, false, "rollback is not promotion run in reverse"},
		{"skipping a stage", ten, rankedEnvironment(t, "prod", "proj-1", 30), true,
			"forward is forward; whether a jump is permitted is authorization"},
		{"unranked source", unranked, twenty, false, "no position means no direction"},
		{"unranked target", ten, unranked, false, "no position means no direction"},
		{"archived target", ten, archivedTarget, false, "archived is closed to new work"},
		{"archived source", archivedSource, twenty, false, "archived is closed to new work"},
		{"different projects", ten, otherProject, false, "ordering is per project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := platform.CanPromote(tt.from, tt.to); got != tt.want {
				t.Errorf("CanPromote() = %t, want %t — %s", got, tt.want, tt.why)
			}
		})
	}
}

func TestCanPromoteIsAStrictOrder(t *testing.T) {
	environments := []platform.Environment{
		rankedEnvironment(t, "dev", "proj-1", 10),
		rankedEnvironment(t, "staging", "proj-1", 20),
		rankedEnvironment(t, "production", "proj-1", 30),
	}

	for _, env := range environments {
		if platform.CanPromote(env, env) {
			t.Errorf("CanPromote(%s, %s) = true; the relation must be irreflexive", env.Ref(), env.Ref())
		}
	}
	for _, a := range environments {
		for _, b := range environments {
			if platform.CanPromote(a, b) && platform.CanPromote(b, a) {
				t.Errorf("CanPromote is not antisymmetric for %s and %s", a.Ref(), b.Ref())
			}
		}
	}
	for _, a := range environments {
		for _, b := range environments {
			for _, c := range environments {
				if platform.CanPromote(a, b) && platform.CanPromote(b, c) && !platform.CanPromote(a, c) {
					t.Errorf("CanPromote is not transitive: %s → %s → %s",
						a.Ref(), b.Ref(), c.Ref())
				}
			}
		}
	}
}

// Archiving either side removes an ordering that existed, and activating
// restores it: availability is part of the relation, not a separate check a
// caller has to remember.
func TestCanPromoteFollowsAvailability(t *testing.T) {
	from := rankedEnvironment(t, "staging", "proj-1", 10)
	to := rankedEnvironment(t, "production", "proj-1", 20)
	if !platform.CanPromote(from, to) {
		t.Fatal("precondition: the pair should be ordered")
	}

	archived, _ := to.Archive()
	if platform.CanPromote(from, archived) {
		t.Error("an archived target is still promotable")
	}
	reactivated, _ := archived.Activate()
	if !platform.CanPromote(from, reactivated) {
		t.Error("reactivating did not restore the ordering")
	}
}
