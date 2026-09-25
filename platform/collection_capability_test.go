package platform

// The capability boundary task 074 had to preserve rather than collapse.
//
// Browsing a hierarchy is convenient to implement as one flat interface, and
// that convenience is exactly what these tests refuse. Task 057 split
// ControlStore from EvaluationStore because "a later backend may reasonably
// implement one and not the other"; a list method is a read, and a read does
// not move an entity between capabilities. Put CandidateEvaluationRuns on
// ControlStore and a backend implementing only the control capability is
// obliged to serve evaluation runs it does not store.
//
// The interesting proof is not that the methods exist — the compile-time
// assertions in store.go already cover that. It is that the service asks each
// capability only for what that capability owns, which is asserted below by
// giving ControlPlane two distinct doubles and watching which one is called.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// recordingControlStore answers the three control collections and records the
// calls. Every other method panics: reaching one from a browsing path would be
// the bug this file exists to catch.
type recordingControlStore struct {
	ControlStore // embedded and nil: any unexpected call panics rather than silently succeeding

	calls []string
}

func (s *recordingControlStore) Projects(
	_ context.Context, after ProjectID, limit int,
) ([]Project, error) {
	s.calls = append(s.calls, "Projects")
	_ = after
	_ = limit
	return nil, nil
}

func (s *recordingControlStore) ProjectAgents(
	_ context.Context, _ ProjectID, _ AgentID, _ int,
) ([]Agent, error) {
	s.calls = append(s.calls, "ProjectAgents")
	return nil, nil
}

func (s *recordingControlStore) AgentCandidates(
	_ context.Context, _ AgentID, _ CandidateID, _ int,
) ([]Candidate, error) {
	s.calls = append(s.calls, "AgentCandidates")
	return nil, nil
}

// recordingEvaluationStore answers the one evaluation collection.
type recordingEvaluationStore struct {
	EvaluationStore // embedded and nil, for the same reason

	calls []string
}

func (s *recordingEvaluationStore) CandidateEvaluationRuns(
	_ context.Context, _ CandidateID, _ EvaluationRunID, _ int,
) ([]EvaluationRun, error) {
	s.calls = append(s.calls, "CandidateEvaluationRuns")
	return nil, nil
}

// panicIngestStore satisfies the third constructor argument and nothing else.
type panicIngestStore struct{ EvaluationIngestStore }

// TestBrowsingAsksEachCapabilityOnlyForWhatItOwns drives all four collections
// through the service with two distinct doubles.
//
// If CandidateEvaluationRuns were ever served from ControlStore, the control
// double would record a call it does not own and the evaluation double would
// record none — which is what the assertions below are shaped to catch,
// rather than merely checking that the call succeeded.
func TestBrowsingAsksEachCapabilityOnlyForWhatItOwns(t *testing.T) {
	control := &recordingControlStore{}
	evaluations := &recordingEvaluationStore{}

	plane, err := NewControlPlane(control, evaluations, panicIngestStore{})
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	ctx := context.Background()

	if _, err := plane.Projects(ctx, "", MaxListPage); err != nil {
		t.Fatalf("Projects() error = %v", err)
	}
	if _, err := plane.ProjectAgents(ctx, "proj-1", "", MaxListPage); err != nil {
		t.Fatalf("ProjectAgents() error = %v", err)
	}
	if _, err := plane.AgentCandidates(ctx, "agent-1", "", MaxListPage); err != nil {
		t.Fatalf("AgentCandidates() error = %v", err)
	}
	if _, err := plane.CandidateEvaluationRuns(ctx, "cand-1", "", MaxListPage); err != nil {
		t.Fatalf("CandidateEvaluationRuns() error = %v", err)
	}

	wantControl := "Projects ProjectAgents AgentCandidates"
	if got := strings.Join(control.calls, " "); got != wantControl {
		t.Errorf("ControlStore calls = %q, want %q", got, wantControl)
	}
	if got := strings.Join(evaluations.calls, " "); got != "CandidateEvaluationRuns" {
		t.Errorf("EvaluationStore calls = %q, want CandidateEvaluationRuns", got)
	}

	// Stated as its own assertion because it is the actual rule: the run
	// collection never reaches the control capability.
	for _, call := range control.calls {
		if call == "CandidateEvaluationRuns" {
			t.Error("the run collection was served from ControlStore; runs are " +
				"evaluation state, and a control-only backend does not hold them")
		}
	}
}

// TestBrowsingValidatesItsParentIdentifier proves the service rejects a
// malformed parent before it reaches a store.
//
// The store would refuse it too, but refusing here keeps a transport's typo
// from becoming a database round trip, and keeps the error the same shape as
// every other caller-owned identifier rejection.
func TestBrowsingValidatesItsParentIdentifier(t *testing.T) {
	control := &recordingControlStore{}
	evaluations := &recordingEvaluationStore{}
	plane, err := NewControlPlane(control, evaluations, panicIngestStore{})
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"ProjectAgents", func() error {
			_, err := plane.ProjectAgents(ctx, "", "", MaxListPage)
			return err
		}},
		{"AgentCandidates", func() error {
			_, err := plane.AgentCandidates(ctx, "", "", MaxListPage)
			return err
		}},
		{"CandidateEvaluationRuns", func() error {
			_, err := plane.CandidateEvaluationRuns(ctx, "", "", MaxListPage)
			return err
		}},
	}
	for _, tt := range cases {
		if err := tt.call(); !errors.Is(err, ErrInvalidID) {
			t.Errorf("%s with an empty parent error = %v, want ErrInvalidID", tt.name, err)
		}
	}
	if len(control.calls)+len(evaluations.calls) != 0 {
		t.Errorf("a malformed parent still reached a store: %v %v",
			control.calls, evaluations.calls)
	}
}

// TestNoGenericStoreInterfaceExists scans the package's exported interface
// declarations.
//
// Task 074 adds four collections and could have justified a fifth interface
// for them. It does not, and this is what keeps that true: a HierarchyStore or
// a Database with one implementation pair and one consumer is the abstraction
// CLAUDE.md says not to build ahead of need, and once it exists everything
// else gets hung off it.
func TestNoGenericStoreInterfaceExists(t *testing.T) {
	forbidden := map[string]string{
		"HierarchyStore": "browsing is three methods on ControlStore and one on EvaluationStore",
		"DiscoveryStore": "discovery is a client workflow, not a persistence capability",
		"ListStore":      "a list method does not need an interface of its own",
		"TraceStore":     "task 067 owns history; nothing here retains an observation",
		"WebUIStore":     "the WebUI is a static same-origin client and holds no store",
		"Database":       "a generic query surface is the boundary this package does not have",
		"Repository":     "the stores are named capabilities, not a repository pattern",
	}

	declared := map[string]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if _, isInterface := spec.Type.(*ast.InterfaceType); isInterface {
				declared[spec.Name.Name] = true
			}
			return true
		})
	}

	if len(declared) == 0 {
		t.Fatal("no interface declarations found; this guard would pass vacuously")
	}
	for name, why := range forbidden {
		if declared[name] {
			t.Errorf("the package declares %s; %s", name, why)
		}
	}
	// And the ones that must still be there, so a rename cannot slip the
	// check above.
	for _, want := range []string{"ControlStore", "EvaluationStore", "EvaluationIngestStore"} {
		if !declared[want] {
			t.Errorf("%s is no longer declared; the capability split is the contract", want)
		}
	}
}
