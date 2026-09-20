package platform

// The one test in this module that lives inside the package.
//
// Everything in domain_test.go is deliberately an external-package test, so
// it exercises the API a real consumer sees. This file exists for the single
// case that API cannot reach: an EvaluationRun whose status is not one this
// package produces.
//
// A caller cannot construct one — the field is unexported and the transitions
// only ever write a known status — which is exactly why the guard needs a
// test here rather than deleting the guard. The state becomes reachable the
// moment something deserializes a run from storage or a wire format, and a
// run that came back as "in-review" must fail closed instead of being treated
// as pending and quietly restarted.

import (
	"errors"
	"testing"
	"time"
)

func TestUnrecognizedStatusFailsClosed(t *testing.T) {
	createdAt := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	at := createdAt.Add(time.Minute)

	// Built by hand on purpose: this models a value arriving from a future
	// deserializer, not something the exported API can produce.
	run := EvaluationRun{
		id:          "run-1",
		candidateID: "cand-1",
		environment: "staging",
		profile:     "profile-1",
		status:      "in-review",
		createdAt:   createdAt,
	}

	for name, transition := range map[string]func() (EvaluationRun, error){
		"Start":    func() (EvaluationRun, error) { return run.Start(at) },
		"Complete": func() (EvaluationRun, error) { return run.Complete(at) },
		"Fail":     func() (EvaluationRun, error) { return run.Fail(at, "x") },
		"Cancel":   func() (EvaluationRun, error) { return run.Cancel(at) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := transition()
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("got %v, want an error wrapping ErrInvalidTransition", err)
			}
			if got != run {
				t.Errorf("a rejected transition modified the run:\n got %+v\nwant %+v", got, run)
			}
		})
	}
}

// TestUnrecognizedStatusIsNotTerminal: an unknown status must not claim to be
// terminal either. Reporting it as terminal would make a corrupted run look
// like finished evidence rather than something to reject.
func TestUnrecognizedStatusIsNotTerminal(t *testing.T) {
	for _, status := range []RunStatus{"", "in-review", "promoted", "passed"} {
		if status.IsTerminal() {
			t.Errorf("RunStatus(%q).IsTerminal() = true, want false", status)
		}
		if status.valid() {
			t.Errorf("RunStatus(%q).valid() = true, want false", status)
		}
	}
}
