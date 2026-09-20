package platform_test

// Task 052: the evaluation domain.
//
// Two things these tests are built to catch, beyond ordinary validation:
//
//  1. Identity collapse. The whole model rests on Agent, Candidate, profile
//     and run being four different questions. A change that quietly made any
//     two of them the same thing would still pass a naive construction test,
//     so the distinctions are asserted directly.
//
//  2. A run's status becoming a verdict. "Completed" means the execution
//     finished. The moment it starts meaning "passed", the gate task has been
//     pre-empted by a field that holds no evidence.
//
// Nothing here imports the core. The domain does not depend on it, and a test
// that reached for it would be the first step in making that untrue.

import (
	"errors"
	"strings"
	"testing"
	"time"

	platform "trustvian-platform"
)

var (
	// A fixed instant: no assertion in this file depends on wall-clock time,
	// because no transition in the package reads a clock.
	epoch = time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)

	maxLenID   = strings.Repeat("a", 256)
	overLenID  = strings.Repeat("a", 257)
	multiByte  = "проект-развёртывания"            // valid, and >1 byte per rune
	fourByteID = "deploy-" + string(rune(0x1F600)) // an emoji is valid UTF-8
)

// ---------------------------------------------------------------------
// Shared identifier policy
// ---------------------------------------------------------------------

// TestIdentifierPolicy drives the one validation policy through every
// constructor that takes an identifier, rather than restating the same table
// five times. A constructor that skipped a check would show up here as a
// single failing row instead of being invisible.
func TestIdentifierPolicy(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"simple", "proj-1", false},
		{"path-like", "acme/checkout-agent", false},
		{"uuid-shaped", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", false},
		{"multi-byte", multiByte, false},
		{"emoji", fourByteID, false},
		{"internal space", "my agent", false},
		{"exactly the limit", maxLenID, false},

		{"empty", "", true},
		{"one byte over the limit", overLenID, true},
		{"leading whitespace", " proj-1", true},
		{"trailing whitespace", "proj-1 ", true},
		{"trailing newline", "proj-1\n", true},
		{"only whitespace", "   ", true},
		{"embedded newline", "proj\n1", true},
		{"embedded tab", "proj\t1", true},
		{"NUL", "proj\x001", true},
		{"terminal escape", "proj\x1b[31m", true},
		{"C1 control", "proj\u0085x", true},
		{"invalid UTF-8", "proj-\xff", true},
	}

	// Every constructor position that takes an identifier or reference.
	positions := map[string]func(string) error{
		"Project.ID": func(v string) error {
			_, err := platform.NewProject(platform.ProjectID(v), "name")
			return err
		},
		"Agent.ID": func(v string) error {
			_, err := platform.NewAgent(platform.AgentID(v), "proj-1", "name")
			return err
		},
		"Agent.ProjectID": func(v string) error {
			_, err := platform.NewAgent("agent-1", platform.ProjectID(v), "name")
			return err
		},
		"Candidate.ID": func(v string) error {
			_, err := platform.NewCandidate(platform.CandidateID(v), "agent-1", platform.CandidateMetadata{})
			return err
		},
		"Candidate.AgentID": func(v string) error {
			_, err := platform.NewCandidate("cand-1", platform.AgentID(v), platform.CandidateMetadata{})
			return err
		},
		"EvaluationRun.ID": func(v string) error {
			_, err := platform.NewEvaluationRun(platform.EvaluationRunID(v), "cand-1", "local", "profile-1", epoch)
			return err
		},
		"EvaluationRun.CandidateID": func(v string) error {
			_, err := platform.NewEvaluationRun("run-1", platform.CandidateID(v), "local", "profile-1", epoch)
			return err
		},
		"EvaluationRun.Environment": func(v string) error {
			_, err := platform.NewEvaluationRun("run-1", "cand-1", platform.EnvironmentRef(v), "profile-1", epoch)
			return err
		},
		"EvaluationRun.Profile": func(v string) error {
			_, err := platform.NewEvaluationRun("run-1", "cand-1", "local", platform.BehavioralProfileRef(v), epoch)
			return err
		},
	}

	for position, construct := range positions {
		t.Run(position, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					err := construct(tt.id)
					if tt.wantErr {
						if !errors.Is(err, platform.ErrInvalidID) {
							t.Fatalf("got %v, want an error wrapping ErrInvalidID", err)
						}
						return
					}
					if err != nil {
						t.Fatalf("got %v, want nil", err)
					}
				})
			}
		})
	}
}

// TestNamePolicy: names are for people. Spaces and Unicode are fine; nothing
// else about the policy differs.
func TestNamePolicy(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"simple", "Checkout Agent", false},
		{"leading space is tolerated", " Checkout Agent", false},
		{"unicode", "Проект Развёртывания", false},
		{"exactly the limit", maxLenID, false},

		{"empty", "", true},
		{"only whitespace", "   ", true},
		{"one byte over the limit", overLenID, true},
		{"embedded newline", "Checkout\nAgent", true},
		{"invalid UTF-8", "Checkout-\xff", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := platform.NewProject("proj-1", tt.value)
			if tt.wantErr && !errors.Is(err, platform.ErrInvalidName) {
				t.Fatalf("NewProject() = %v, want an error wrapping ErrInvalidName", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("NewProject() = %v, want nil", err)
			}

			// The same policy applies to an Agent's name.
			_, err = platform.NewAgent("agent-1", "proj-1", tt.value)
			if got := errors.Is(err, platform.ErrInvalidName); got != tt.wantErr {
				t.Fatalf("NewAgent() name validation disagrees with NewProject(): err = %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Ownership
// ---------------------------------------------------------------------

func TestOwnershipIsCarried(t *testing.T) {
	project, err := platform.NewProject("proj-1", "Checkout")
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	agent, err := platform.NewAgent("agent-1", project.ID, "Checkout Agent")
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	candidate, err := platform.NewCandidate("cand-1", agent.ID, platform.CandidateMetadata{})
	if err != nil {
		t.Fatalf("NewCandidate() error = %v", err)
	}

	if agent.ProjectID != project.ID {
		t.Errorf("Agent.ProjectID = %q, want %q", agent.ProjectID, project.ID)
	}
	if candidate.AgentID != agent.ID {
		t.Errorf("Candidate.AgentID = %q, want %q", candidate.AgentID, agent.ID)
	}
}

// TestAgentIdentityIsIndependentOfCandidateMetadata is the invariant that
// keeps an Agent stable across versions. An Agent has nowhere to put a commit
// or a digest, and this asserts the model rather than the wish: two
// candidates of the same agent, built from different commits, leave the agent
// untouched and identical.
func TestAgentIdentityIsIndependentOfCandidateMetadata(t *testing.T) {
	agent, err := platform.NewAgent("agent-1", "proj-1", "Checkout Agent")
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	before := agent

	for _, sha := range []string{"9f3c1a", "0b7e22"} {
		if _, err := platform.NewCandidate(
			platform.CandidateID("cand-"+sha), agent.ID,
			platform.CandidateMetadata{SourceRef: sha},
		); err != nil {
			t.Fatalf("NewCandidate(%s) error = %v", sha, err)
		}
	}

	if agent != before {
		t.Fatalf("creating candidates changed the agent: %+v -> %+v", before, agent)
	}
}

// ---------------------------------------------------------------------
// Candidate identity
// ---------------------------------------------------------------------

// TestSameSourceRefStillProducesTwoCandidates is the identity distinction
// that matters most: a candidate is platform identity, and metadata is
// description. Two candidates sharing a commit are still two candidates —
// nothing deduplicates them, because nothing here treats metadata as
// identifying.
func TestSameSourceRefStillProducesTwoCandidates(t *testing.T) {
	meta := platform.CandidateMetadata{SourceRef: "9f3c1a", Model: "claude-opus-5"}

	first, err := platform.NewCandidate("cand-1", "agent-1", meta)
	if err != nil {
		t.Fatalf("NewCandidate() error = %v", err)
	}
	second, err := platform.NewCandidate("cand-2", "agent-1", meta)
	if err != nil {
		t.Fatalf("NewCandidate() error = %v", err)
	}

	if first.ID == second.ID {
		t.Fatal("two candidates from one commit collapsed into one identity")
	}
	if first.Metadata != second.Metadata {
		t.Error("identical metadata did not survive construction identically")
	}
	if first.AgentID != second.AgentID {
		t.Error("both candidates should belong to the same agent")
	}
}

// TestCandidateIdentityCannotBeMutated is a structural assertion: there is no
// exported method on Candidate at all, so no SetSourceRef or ChangeAgent can
// turn one candidate into another. A caller wanting a different artifact
// creates a different Candidate, which is what keeps a finished run's record
// true.
//
// Assigning to an exported field of a local copy is still possible — this is
// Go, and the alternative is unexported fields plus a getter per field, which
// ADR 0025 rejects as worse. What the model guarantees is that the copy is a
// copy: mutating it cannot reach the original.
func TestCandidateIdentityCannotBeMutated(t *testing.T) {
	original, err := platform.NewCandidate("cand-1", "agent-1", platform.CandidateMetadata{SourceRef: "9f3c1a"})
	if err != nil {
		t.Fatalf("NewCandidate() error = %v", err)
	}

	copied := original
	copied.Metadata.SourceRef = "tampered"
	copied.AgentID = "other-agent"

	if original.Metadata.SourceRef != "9f3c1a" || original.AgentID != "agent-1" {
		t.Fatalf("mutating a copy reached the original: %+v", original)
	}
	// And the copy really did change — otherwise the assertion above would
	// hold for a reason that has nothing to do with value semantics.
	if copied.Metadata.SourceRef != "tampered" || copied.AgentID != "other-agent" {
		t.Fatalf("the copy was not modified, so this proves nothing: %+v", copied)
	}
}

func TestCandidateMetadataIsValidatedAndBounded(t *testing.T) {
	tests := []struct {
		name    string
		meta    platform.CandidateMetadata
		wantErr bool
	}{
		{"empty is valid: every field is optional", platform.CandidateMetadata{}, false},
		{"fully populated", platform.CandidateMetadata{
			Label: "v3", SourceRef: "9f3c1a", ArtifactDigest: "sha256:abc",
			Model: "claude-opus-5", ToolsetDigest: "sha256:def", ConfigDigest: "sha256:012",
		}, false},
		{"unicode label", platform.CandidateMetadata{Label: multiByte}, false},
		{"label at the limit", platform.CandidateMetadata{Label: maxLenID}, false},

		{"label over the limit", platform.CandidateMetadata{Label: overLenID}, true},
		{"source ref over the limit", platform.CandidateMetadata{SourceRef: overLenID}, true},
		{"digest over the limit", platform.CandidateMetadata{ArtifactDigest: overLenID}, true},
		{"model over the limit", platform.CandidateMetadata{Model: overLenID}, true},
		{"toolset over the limit", platform.CandidateMetadata{ToolsetDigest: overLenID}, true},
		{"config over the limit", platform.CandidateMetadata{ConfigDigest: overLenID}, true},
		{"control character", platform.CandidateMetadata{Label: "v3\x00"}, true},
		{"invalid UTF-8", platform.CandidateMetadata{Model: "gpt-\xff"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := platform.NewCandidate("cand-1", "agent-1", tt.meta)
			if tt.wantErr && !errors.Is(err, platform.ErrInvalidMetadata) {
				t.Fatalf("got %v, want an error wrapping ErrInvalidMetadata", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("got %v, want nil", err)
			}
		})
	}
}

// ---------------------------------------------------------------------
// References
// ---------------------------------------------------------------------

// TestEnvironmentRefIsOpaque: no enum. A future adapter may use "local" or
// "staging", and nothing here enforces a closed set — task 065 owns the real
// environment model.
func TestEnvironmentRefIsOpaque(t *testing.T) {
	for _, ref := range []string{"local", "sandbox", "staging", "production", "pr-4821", "team-b/scratch"} {
		if _, err := platform.NewEvaluationRun("run-1", "cand-1", platform.EnvironmentRef(ref), "profile-1", epoch); err != nil {
			t.Errorf("environment %q rejected: %v", ref, err)
		}
	}
}

// TestIdentifiersAreDistinctTypes is the reason every identifier is its own
// type. It is a compile-time assertion written as a test: if any two of these
// became the same type, the conversions below would still compile but the
// declarations would collapse, and a reviewer reading this file would see the
// intent.
//
// The real enforcement is that `run.CandidateID = project.ID` does not
// compile. That cannot be written here — it would break the build — so this
// documents the property and pins that the types exist separately.
func TestIdentifiersAreDistinctTypes(t *testing.T) {
	const raw = "shared-string"

	project := platform.ProjectID(raw)
	agent := platform.AgentID(raw)
	candidate := platform.CandidateID(raw)
	run := platform.EvaluationRunID(raw)
	env := platform.EnvironmentRef(raw)
	profile := platform.BehavioralProfileRef(raw)

	// Each is its own named type over string: comparable to its own kind,
	// and assignable across kinds only through an explicit conversion.
	if string(project) != raw || string(agent) != raw || string(candidate) != raw ||
		string(run) != raw || string(env) != raw || string(profile) != raw {
		t.Fatal("identifier types do not round-trip through string")
	}

	// A behavioral profile is not a candidate and not a run, even when a
	// caller happens to use the same text. Keeping them separate types is
	// what leaves a later service free to decide how profiles are allocated.
	if any(profile) == any(candidate) {
		t.Error("BehavioralProfileRef and CandidateID are the same type")
	}
	if any(profile) == any(run) {
		t.Error("BehavioralProfileRef and EvaluationRunID are the same type")
	}
	if any(env) == any(profile) {
		t.Error("EnvironmentRef and BehavioralProfileRef are the same type")
	}
}

// ---------------------------------------------------------------------
// EvaluationRun construction and lifecycle
// ---------------------------------------------------------------------

func newRun(t *testing.T) platform.EvaluationRun {
	t.Helper()
	run, err := platform.NewEvaluationRun("run-1", "cand-1", "staging", "profile-1", epoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	return run
}

func TestNewEvaluationRunBindsExactlyOneOfEach(t *testing.T) {
	run := newRun(t)

	if run.CandidateID != "cand-1" || run.Environment != "staging" || run.Profile != "profile-1" {
		t.Fatalf("run did not bind its references: %+v", run)
	}
	if run.Status != platform.RunPending {
		t.Errorf("Status = %q, want %q", run.Status, platform.RunPending)
	}
	if !run.CreatedAt.Equal(epoch) {
		t.Errorf("CreatedAt = %v, want %v", run.CreatedAt, epoch)
	}
	if !run.StartedAt.IsZero() || !run.FinishedAt.IsZero() {
		t.Error("a pending run must not carry start or finish times")
	}
	if run.FailureReason != "" {
		t.Error("a pending run must not carry a failure reason")
	}
}

func TestNewEvaluationRunRequiresACreationTime(t *testing.T) {
	_, err := platform.NewEvaluationRun("run-1", "cand-1", "local", "profile-1", time.Time{})
	if !errors.Is(err, platform.ErrInvalidTimestamp) {
		t.Fatalf("got %v, want an error wrapping ErrInvalidTimestamp", err)
	}
}

// TestLifecycleAllowedTransitions walks each legal path end to end.
func TestLifecycleAllowedTransitions(t *testing.T) {
	start := epoch.Add(time.Minute)
	finish := epoch.Add(2 * time.Minute)

	tests := []struct {
		name       string
		advance    func(platform.EvaluationRun) (platform.EvaluationRun, error)
		wantStatus platform.RunStatus
		wantReason string
	}{
		{"pending to cancelled", func(r platform.EvaluationRun) (platform.EvaluationRun, error) {
			return r.Cancel(start)
		}, platform.RunCancelled, ""},

		{"running to completed", func(r platform.EvaluationRun) (platform.EvaluationRun, error) {
			r, err := r.Start(start)
			if err != nil {
				return r, err
			}
			return r.Complete(finish)
		}, platform.RunCompleted, ""},

		{"running to failed", func(r platform.EvaluationRun) (platform.EvaluationRun, error) {
			r, err := r.Start(start)
			if err != nil {
				return r, err
			}
			return r.Fail(finish, "sandbox became unreachable")
		}, platform.RunFailed, "sandbox became unreachable"},

		{"running to cancelled", func(r platform.EvaluationRun) (platform.EvaluationRun, error) {
			r, err := r.Start(start)
			if err != nil {
				return r, err
			}
			return r.Cancel(finish)
		}, platform.RunCancelled, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.advance(newRun(t))
			if err != nil {
				t.Fatalf("transition error = %v", err)
			}
			if got.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if !got.Status.IsTerminal() {
				t.Error("the resulting status should be terminal")
			}
			if got.FailureReason != tt.wantReason {
				t.Errorf("FailureReason = %q, want %q", got.FailureReason, tt.wantReason)
			}
			if got.FinishedAt.IsZero() {
				t.Error("a terminal run must record when it finished")
			}
			// References never move.
			if got.CandidateID != "cand-1" || got.Environment != "staging" || got.Profile != "profile-1" {
				t.Errorf("a transition changed the run's references: %+v", got)
			}
		})
	}
}

// TestLifecycleRejectsDisallowedTransitions covers every edge the state
// machine must refuse, and asserts the run comes back unchanged — a caller
// that ignores the error must not be left holding a half-applied state.
func TestLifecycleRejectsDisallowedTransitions(t *testing.T) {
	start := epoch.Add(time.Minute)
	finish := epoch.Add(2 * time.Minute)
	later := epoch.Add(3 * time.Minute)

	pending := newRun(t)
	running, err := pending.Start(start)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	completed, err := running.Complete(finish)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	failed, err := running.Fail(finish, "boom")
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	cancelled, err := running.Cancel(finish)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	tests := []struct {
		name string
		from platform.EvaluationRun
		do   func(platform.EvaluationRun) (platform.EvaluationRun, error)
	}{
		{"pending cannot complete", pending, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Complete(later) }},
		{"pending cannot fail", pending, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Fail(later, "x") }},
		{"running cannot restart", running, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Start(later) }},

		{"completed cannot restart", completed, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Start(later) }},
		{"completed cannot complete again", completed, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Complete(later) }},
		{"completed cannot fail", completed, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Fail(later, "x") }},
		{"completed cannot cancel", completed, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Cancel(later) }},

		{"failed cannot restart", failed, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Start(later) }},
		{"failed cannot complete", failed, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Complete(later) }},
		{"failed cannot cancel", failed, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Cancel(later) }},

		{"cancelled cannot restart", cancelled, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Start(later) }},
		{"cancelled cannot complete", cancelled, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Complete(later) }},
		{"cancelled cannot fail", cancelled, func(r platform.EvaluationRun) (platform.EvaluationRun, error) { return r.Fail(later, "x") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.do(tt.from)
			if !errors.Is(err, platform.ErrInvalidTransition) {
				t.Fatalf("got %v, want an error wrapping ErrInvalidTransition", err)
			}
			if got != tt.from {
				t.Errorf("a rejected transition modified the run:\n got %+v\nwant %+v", got, tt.from)
			}
		})
	}
}

// TestChronologyIsValidatedNotRewritten: an out-of-order instant is refused.
// Silently correcting it would make a run's own record of when it ran untrue,
// which is the one thing the run is evidence of.
func TestChronologyIsValidatedNotRewritten(t *testing.T) {
	before := epoch.Add(-time.Hour)
	start := epoch.Add(time.Minute)

	t.Run("start cannot precede creation", func(t *testing.T) {
		run := newRun(t)
		got, err := run.Start(before)
		if !errors.Is(err, platform.ErrInvalidTimestamp) {
			t.Fatalf("got %v, want an error wrapping ErrInvalidTimestamp", err)
		}
		if got != run {
			t.Error("a rejected transition modified the run")
		}
	})

	t.Run("finish cannot precede start", func(t *testing.T) {
		run, err := newRun(t).Start(start)
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		for name, do := range map[string]func(time.Time) (platform.EvaluationRun, error){
			"Complete": run.Complete,
			"Cancel":   run.Cancel,
			"Fail":     func(at time.Time) (platform.EvaluationRun, error) { return run.Fail(at, "x") },
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := do(epoch); !errors.Is(err, platform.ErrInvalidTimestamp) {
					t.Fatalf("got %v, want an error wrapping ErrInvalidTimestamp", err)
				}
			})
		}
	})

	t.Run("cancelling an unstarted run compares against creation", func(t *testing.T) {
		if _, err := newRun(t).Cancel(before); !errors.Is(err, platform.ErrInvalidTimestamp) {
			t.Fatalf("got %v, want an error wrapping ErrInvalidTimestamp", err)
		}
		if _, err := newRun(t).Cancel(epoch); err != nil {
			t.Fatalf("cancelling at the creation instant should be allowed: %v", err)
		}
	})

	t.Run("a missing timestamp is rejected", func(t *testing.T) {
		if _, err := newRun(t).Start(time.Time{}); !errors.Is(err, platform.ErrInvalidTimestamp) {
			t.Fatalf("got %v, want an error wrapping ErrInvalidTimestamp", err)
		}
	})

	t.Run("an equal instant is allowed", func(t *testing.T) {
		// Two events in the same nanosecond are ordered, not impossible.
		run, err := newRun(t).Start(epoch)
		if err != nil {
			t.Fatalf("Start() at the creation instant: %v", err)
		}
		if _, err := run.Complete(epoch); err != nil {
			t.Fatalf("Complete() at the start instant: %v", err)
		}
	})
}

func TestFailureReasonIsBounded(t *testing.T) {
	run, err := newRun(t).Start(epoch)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	finish := epoch.Add(time.Minute)

	if _, err := run.Fail(finish, overLenID); !errors.Is(err, platform.ErrInvalidMetadata) {
		t.Errorf("an over-long failure reason: got %v, want ErrInvalidMetadata", err)
	}
	if _, err := run.Fail(finish, "crashed\x00"); !errors.Is(err, platform.ErrInvalidMetadata) {
		t.Errorf("a control character in a failure reason: got %v, want ErrInvalidMetadata", err)
	}
	if _, err := run.Fail(finish, ""); err != nil {
		t.Errorf("an empty failure reason should be allowed: %v", err)
	}
}

// ---------------------------------------------------------------------
// The invariant most at risk from a later task
// ---------------------------------------------------------------------

// TestCompletedIsNotAVerdict pins the distinction tasks 056 and 066 depend
// on. A completed run finished executing. Whether the candidate passed is a
// gate's answer, read from evidence this run does not hold.
//
// The structural half of the assertion is that there is no field to check:
// EvaluationRun has no Passed, Score, Grade, or Promotable. If one is ever
// added, this test's comment is the argument against it, and the compile-time
// shape below is what a reviewer will notice first.
func TestCompletedIsNotAVerdict(t *testing.T) {
	run, err := newRun(t).Start(epoch.Add(time.Minute))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	completed, err := run.Complete(epoch.Add(2 * time.Minute))
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	// A completed run and a failed run differ only in how the *execution*
	// ended. Neither carries a verdict, and a completed run carries no more
	// evaluative information than a failed one does.
	failed, err := run.Fail(epoch.Add(2*time.Minute), "sandbox died")
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	completed.Status, failed.Status = "", ""
	completed.FailureReason, failed.FailureReason = "", ""
	if completed != failed {
		t.Fatalf("completed and failed runs differ beyond status and failure reason:\n %+v\nvs %+v",
			completed, failed)
	}
}

// TestRunStatusTerminality documents the closed set and which members end a
// run's life.
func TestRunStatusTerminality(t *testing.T) {
	terminal := map[platform.RunStatus]bool{
		platform.RunPending:   false,
		platform.RunRunning:   false,
		platform.RunCompleted: true,
		platform.RunFailed:    true,
		platform.RunCancelled: true,
	}
	for status, want := range terminal {
		if got := status.IsTerminal(); got != want {
			t.Errorf("%q.IsTerminal() = %v, want %v", status, got, want)
		}
	}

	if platform.RunStatus("promoted").IsTerminal() {
		t.Error("an unrecognized status must not report itself terminal")
	}
}

// TestUnrecognizedStatusCannotTransition: a run whose status did not come
// from this package (a future deserializer, a hand-built value) fails closed
// rather than being treated as pending.
func TestUnrecognizedStatusCannotTransition(t *testing.T) {
	run := newRun(t)
	run.Status = "in-review"

	if _, err := run.Start(epoch.Add(time.Minute)); !errors.Is(err, platform.ErrInvalidTransition) {
		t.Fatalf("got %v, want an error wrapping ErrInvalidTransition", err)
	}
}
