package config

// Task 106's optional frequency limits in the scenario file.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const frequencyLimits = `  min_candidate_frequency: 4
  max_lost_behaviors: 0
  max_calls_per_run:
    - target: crm.internal
      max: 6
    - target: kb.internal
      max: 10
`

func TestScenarioFrequencyLimitsLoad(t *testing.T) {
	cfg, err := LoadScenario([]byte(validScenario + frequencyLimits))
	if err != nil {
		t.Fatalf("LoadScenario() error = %v", err)
	}
	g := cfg.Gate
	if *g.MinCandidateFrequency != 4 || *g.MaxLostBehaviors != 0 || len(g.MaxCallsPerRun) != 2 ||
		g.MaxCallsPerRun[1].Target != "kb.internal" || *g.MaxCallsPerRun[1].Max != 10 {
		t.Fatalf("gate = %+v", g)
	}
	// Omitted is nil: the control plane does not evaluate it, and nothing
	// here defaults it.
	plain, err := LoadScenario([]byte(validScenario))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Gate.MinCandidateFrequency != nil || plain.Gate.MaxLostBehaviors != nil || plain.Gate.MaxCallsPerRun != nil {
		t.Fatalf("omitted limits were defaulted: %+v", plain.Gate)
	}
}

func TestScenarioFrequencyLimitsAreRefusedByName(t *testing.T) {
	targets := func(n int) string {
		var b strings.Builder
		b.WriteString("  max_calls_per_run:\n")
		for i := range n {
			fmt.Fprintf(&b, "    - {target: t%d, max: 1}\n", i)
		}
		return b.String()
	}
	tests := []struct {
		name, extra, field string
	}{
		{"min frequency past runs", "  min_candidate_frequency: 6\n", "min_candidate_frequency"},
		{"min frequency as a float", "  min_candidate_frequency: 2.5\n", "min_candidate_frequency"},
		{"lost as a string", "  max_lost_behaviors: \"1\"\n", "cannot unmarshal"}, // the decoder names the line
		{"an empty list", "  max_calls_per_run: []\n", "max_calls_per_run"},
		{"seventeen targets", targets(17), "max_calls_per_run"},
		{"a duplicate target", "  max_calls_per_run:\n    - {target: a, max: 1}\n    - {target: a, max: 2}\n", "twice"},
		{"an empty target", "  max_calls_per_run:\n    - {target: \"\", max: 1}\n", "target"},
		{"a 256-byte target", "  max_calls_per_run:\n    - {target: " + strings.Repeat("a", 256) + ", max: 1}\n", "target"},
		{"a missing max", "  max_calls_per_run:\n    - {target: a}\n", "max"},
		{"a fractional max", "  max_calls_per_run:\n    - {target: a, max: 1.5}\n", "max"},
		{"an unknown entry key", "  max_calls_per_run:\n    - {target: a, max: 1, per: run}\n", "per"},
		// D3: not part of task 106, so still an unknown field.
		{"max_llm_calls_per_run", "  max_llm_calls_per_run: 12\n", "max_llm_calls_per_run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadScenario([]byte(validScenario + tt.extra))
			if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("LoadScenario() error = %v, want ErrInvalidScenario naming %s", err, tt.field)
			}
		})
	}
	// The bounds themselves load.
	for _, extra := range []string{"  min_candidate_frequency: 5\n", targets(16),
		"  max_calls_per_run:\n    - {target: " + strings.Repeat("a", 255) + ", max: 0}\n"} {
		if _, err := LoadScenario([]byte(validScenario + extra)); err != nil {
			t.Errorf("a boundary value was refused: %v\n%s", err, extra)
		}
	}
}
