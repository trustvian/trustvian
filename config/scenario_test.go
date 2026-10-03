package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validScenario = `version: v1
name: support-login
runs: 5
reference:
  command: [python, agent.py]
  env: {AGENT_MODE: reference}
candidate:
  command: [python, agent.py]
  env: {AGENT_MODE: candidate}
gate:
  added_candidate_presence_minimum: 1
  added_reference_presence_maximum: 0
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0
`

func TestAValidScenarioLoads(t *testing.T) {
	cfg, err := LoadScenario([]byte(validScenario))
	if err != nil {
		t.Fatalf("LoadScenario() error = %v", err)
	}
	if *cfg.Runs != 5 || *cfg.Gate.AddedCandidatePresenceMinimum != 1 ||
		cfg.Candidate.Env["AGENT_MODE"] != "candidate" || cfg.Reference.Command[1] != "agent.py" {
		t.Errorf("parsed %+v", cfg)
	}
}

func replaceLine(t *testing.T, doc, prefix, with string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			if with == "" {
				return strings.Join(append(lines[:i], lines[i+1:]...), "\n")
			}
			indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
			lines[i] = indent + with
			return strings.Join(lines, "\n")
		}
	}
	t.Fatalf("no line starting %q", prefix)
	return ""
}

// Every bound is a usage error naming the field, and no field has a default.
func TestScenarioBoundsAndRequiredFieldsAreRefusedByName(t *testing.T) {
	cases := []struct {
		name, prefix, with, mentions string
	}{
		{"runs absent", "runs:", "", "runs is required"},
		{"runs zero", "runs:", "runs: 0", "runs is 0"},
		{"runs 65", "runs:", "runs: 65", "runs is 65"},
		{"k absent", "added_candidate_presence_minimum", "", "added_candidate_presence_minimum is required"},
		{"j absent", "added_reference_presence_maximum", "", "added_reference_presence_maximum is required"},
		{"a maximum absent", "max_block_decisions_per_run", "", "max_block_decisions_per_run is required"},
		{"k zero", "added_candidate_presence_minimum", "added_candidate_presence_minimum: 0", "(k) is 0"},
		{"k above N", "added_candidate_presence_minimum", "added_candidate_presence_minimum: 6", "(k) is 6"},
		{"j equal to k", "added_reference_presence_maximum", "added_reference_presence_maximum: 1", "(j) is 1"},
		{"j above N", "added_reference_presence_maximum", "added_reference_presence_maximum: 6", "(j) is 6"},
		{"no version", "version:", "", "version must be"},
		{"wrong version", "version:", "version: v2", "version must be"},
		{"no name", "name:", "", "name is required"},
		{"bad env name", "env: {AGENT_MODE: candidate}", "env: {9BAD: x}", "invalid variable name"},
		{"no command", "command: [python, agent.py]", "command: []", "reference.command is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadScenario([]byte(replaceLine(t, validScenario, tc.prefix, tc.with)))
			if !errors.Is(err, ErrInvalidScenario) {
				t.Fatalf("error = %v, want ErrInvalidScenario", err)
			}
			if !strings.Contains(err.Error(), tc.mentions) {
				t.Errorf("error %q does not name %q", err, tc.mentions)
			}
		})
	}
}

func TestScenarioAcceptsTheBoundaryValues(t *testing.T) {
	for _, tc := range []struct{ runs, k, j string }{
		{"1", "1", "0"}, {"64", "64", "63"}, {"5", "5", "0"},
	} {
		doc := replaceLine(t, validScenario, "runs:", "runs: "+tc.runs)
		doc = replaceLine(t, doc, "added_candidate_presence_minimum", "added_candidate_presence_minimum: "+tc.k)
		doc = replaceLine(t, doc, "added_reference_presence_maximum", "added_reference_presence_maximum: "+tc.j)
		if _, err := LoadScenario([]byte(doc)); err != nil {
			t.Errorf("runs %s k %s j %s refused: %v", tc.runs, tc.k, tc.j, err)
		}
	}
}

func TestScenarioRefusesUnknownMalformedAndNegativeFields(t *testing.T) {
	for name, doc := range map[string]string{
		"misspelled limit": replaceLine(t, validScenario, "max_repeated_added_behaviors",
			"max_repeated_added_behavior: 0"),
		"negative limit": replaceLine(t, validScenario, "max_block_decisions_per_run",
			"max_block_decisions_per_run: -1"),
		"duplicate key": validScenario + "runs: 3\n",
		"not yaml":      "version: [unclosed\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadScenario([]byte(doc)); err == nil {
				t.Error("accepted")
			}
		})
	}
	if _, err := LoadScenario(nil); !errors.Is(err, ErrEmptyInput) {
		t.Errorf("empty input: %v", err)
	}
}

// Task 078 criterion 9: a scenario asserts no ordering of its own. There is
// no ordering field, so an ordering-shaped key is refused as unknown rather
// than silently ignored.
func TestScenarioRefusesOrderingKeys(t *testing.T) {
	for _, key := range []string{
		"order: [plan, fetch, apply]",
		"sequence: [plan, fetch, apply]",
		"steps:\n  - plan\n  - fetch",
	} {
		t.Run(strings.SplitN(key, ":", 2)[0], func(t *testing.T) {
			_, err := LoadScenario([]byte(validScenario + key + "\n"))
			if err == nil {
				t.Fatal("accepted an ordering key")
			}
			if !strings.Contains(err.Error(), "not found") {
				t.Errorf("error = %v, want an unknown-field refusal", err)
			}
		})
	}
}

func TestScenarioFileIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.yaml")
	if err := os.WriteFile(path, []byte(validScenario+strings.Repeat("#", maxConfigFileSize)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadScenarioFile(path); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("error = %v, want ErrFileTooLarge", err)
	}
}

// A count or limit must be a YAML integer. The decoder would otherwise convert:
// runs 5.9 to 5, k 1.9 to 1, a negative fraction into an unsigned limit as 0,
// and an integer past 64 bits — which resolves as a float — to a wrapped value.
// Each of those is a threshold the author did not write.
func TestScenarioCountsAndLimitsMustBeWholeNumbers(t *testing.T) {
	for _, tc := range []struct {
		name, field, value string
	}{
		{"fractional runs", "runs", "5.9"},
		{"fractional runs below one", "runs", "0.5"},
		{"exponent runs", "runs", "1e1"},
		{"fractional k", "added_candidate_presence_minimum", "1.9"},
		{"fractional j", "added_reference_presence_maximum", "0.4"},
		{"fractional budget", "max_repeated_added_behaviors", "0.5"},
		{"negative fraction", "max_block_decisions_per_run", "-0.5"},
		{"negative fraction near zero", "max_critical_risk_observations_per_run", "-0.0"},
		{"past 64 bits", "max_repeated_added_behaviors", "18446744073709551616"},
		{"far past 64 bits", "max_block_decisions_per_run", "99999999999999999999999"},
		{"infinity", "max_critical_risk_observations_per_run", ".inf"},
		{"not a number", "max_repeated_added_behaviors", ".nan"},
		{"quoted integer", "runs", `"5"`},
		{"boolean", "max_block_decisions_per_run", "true"},
		{"through an alias", "max_repeated_added_behaviors", "*frac"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := replaceLine(t, validScenario, tc.field, tc.field+": "+tc.value)
			if tc.value == "*frac" {
				// The anchor sits where a float is legal, an env value.
				doc = strings.Replace(doc, "{AGENT_MODE: reference}", "{AGENT_MODE: &frac 0.5}", 1)
			}
			_, err := LoadScenario([]byte(doc))
			if !errors.Is(err, ErrInvalidScenario) {
				t.Fatalf("error = %v, want ErrInvalidScenario", err)
			}
		})
	}
}

// Whole numbers at the bounds still load, in every integer spelling YAML has.
func TestScenarioAcceptsWholeNumbersAtTheBounds(t *testing.T) {
	for _, tc := range []struct {
		name, field, value string
		check              func(ScenarioConfig) bool
	}{
		{"runs 1", "runs", "1", func(c ScenarioConfig) bool { return *c.Runs == 1 }},
		{"runs 64", "runs", "64", func(c ScenarioConfig) bool { return *c.Runs == 64 }},
		{"k = N", "added_candidate_presence_minimum", "5",
			func(c ScenarioConfig) bool { return *c.Gate.AddedCandidatePresenceMinimum == 5 }},
		{"largest unsigned limit", "max_repeated_added_behaviors", "18446744073709551615",
			func(c ScenarioConfig) bool { return *c.Gate.MaxRepeatedAddedBehaviors == 1<<64-1 }},
		{"hexadecimal", "max_block_decisions_per_run", "0x10",
			func(c ScenarioConfig) bool { return *c.Gate.MaxBlockDecisionsPerRun == 16 }},
		{"explicit plus", "max_critical_risk_observations_per_run", "+3",
			func(c ScenarioConfig) bool { return *c.Gate.MaxCriticalRiskObservationsPerRun == 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadScenario([]byte(replaceLine(t, validScenario, tc.field, tc.field+": "+tc.value)))
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if !tc.check(cfg) {
				t.Errorf("%s decoded to an unexpected value: %+v", tc.value, cfg.Gate)
			}
		})
	}
}

// scenarioWith is a valid scenario with root in place of its `runs` line and
// gate as its whole gate mapping. A top-level `x-…` key would be an unknown
// field, so anchors are defined inside env values, which the schema allows.
func scenarioWith(root, gate string) string {
	return `version: v1
name: support-login
reference:
  command: [python, agent.py]
  env: {AGENT_MODE: reference}
candidate:
  command: [python, agent.py]
  env: {AGENT_MODE: candidate}
` + root + "\ngate:\n" + gate + "\n"
}

const fullGate = `  added_candidate_presence_minimum: 1
  added_reference_presence_maximum: 0
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0`

// The decoder resolves merges and aliased keys before it converts a number, so
// the integer check must see what the decoder assigned, not only keys spelled
// out. Every document here decoded to a truncated, zeroed or saturated
// threshold on 63bdacb.
func TestScenarioIntegerCheckSeesMergesAndAliases(t *testing.T) {
	gateWithout := func(drop string) string {
		var kept []string
		for _, line := range strings.Split(fullGate, "\n") {
			if !strings.Contains(line, drop+":") {
				kept = append(kept, line)
			}
		}
		return strings.Join(kept, "\n")
	}
	for name, doc := range map[string]string{
		"root merge carrying a fractional runs": scenarioWith(
			"<<: {runs: 2.9}", fullGate),
		"gate merge carrying a fractional k": scenarioWith("runs: 5",
			"  <<: {added_candidate_presence_minimum: 1.9}\n"+gateWithout("added_candidate_presence_minimum")),
		"merged negative fraction": scenarioWith("runs: 5",
			"  <<: {max_block_decisions_per_run: -0.5}\n"+gateWithout("max_block_decisions_per_run")),
		"merge sequence, the first carrying the fraction": scenarioWith("runs: 5",
			"  <<: [{max_repeated_added_behaviors: 0.5}, {max_repeated_added_behaviors: 0}]\n"+
				gateWithout("max_repeated_added_behaviors")),
		"merge of an anchored mapping": strings.Replace(scenarioWith("runs: 5",
			"  <<: *limits\n"+gateWithout("max_critical_risk_observations_per_run")),
			"env: {AGENT_MODE: candidate}", "env: &limits {max_critical_risk_observations_per_run: 1.5}", 1),
		"aliased runs key": strings.Replace(scenarioWith("*runskey : 5.9", fullGate),
			"env: {AGENT_MODE: reference}", "env: {AGENT_MODE: &runskey runs}", 1),
		"aliased limit key past 64 bits": strings.Replace(scenarioWith("runs: 5",
			gateWithout("max_repeated_added_behaviors")+"\n  *limitkey : 18446744073709551616"),
			"env: {AGENT_MODE: reference}", "env: {AGENT_MODE: &limitkey max_repeated_added_behaviors}", 1),
		"aliased fractional value": strings.Replace(scenarioWith("runs: *five", fullGate),
			"env: {AGENT_MODE: reference}", "env: {AGENT_MODE: &five 5.9}", 1),
		"merged overflow": scenarioWith("runs: 5",
			"  <<: {max_block_decisions_per_run: 99999999999999999999999}\n"+gateWithout("max_block_decisions_per_run")),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadScenario([]byte(doc))
			if !errors.Is(err, ErrInvalidScenario) {
				t.Fatalf("error = %v (runs %v, gate %+v), want ErrInvalidScenario\n%s",
					err, cfg.Runs, cfg.Gate, doc)
			}
		})
	}
}

// Integer merges and aliases are ordinary YAML and still load, with the
// decoder's precedence: an explicit key overrides a merged one, and the first
// mapping of a merge sequence wins over later ones.
func TestScenarioAcceptsIntegerMergesAndAliases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		doc   string
		check func(ScenarioConfig) bool
	}{
		{"root merge", scenarioWith("<<: {runs: 64}", fullGate),
			func(c ScenarioConfig) bool { return *c.Runs == 64 }},
		{"gate merge at the bounds", scenarioWith("runs: 1",
			"  <<: {added_candidate_presence_minimum: 1, max_repeated_added_behaviors: 18446744073709551615}\n"+
				"  added_reference_presence_maximum: 0\n  max_block_decisions_per_run: 0\n"+
				"  max_critical_risk_observations_per_run: 0"),
			func(c ScenarioConfig) bool {
				return *c.Gate.AddedCandidatePresenceMinimum == 1 && *c.Gate.MaxRepeatedAddedBehaviors == 1<<64-1
			}},
		{"an explicit integer overrides a merged fraction", scenarioWith("runs: 5",
			"  <<: {max_block_decisions_per_run: 0.5}\n"+fullGate),
			func(c ScenarioConfig) bool { return *c.Gate.MaxBlockDecisionsPerRun == 0 }},
		{"a merge sequence's first mapping wins", scenarioWith("runs: 5",
			"  <<: [{max_block_decisions_per_run: 2}, {max_block_decisions_per_run: 0.5}]\n"+
				"  added_candidate_presence_minimum: 1\n  added_reference_presence_maximum: 0\n"+
				"  max_repeated_added_behaviors: 0\n  max_critical_risk_observations_per_run: 0"),
			func(c ScenarioConfig) bool { return *c.Gate.MaxBlockDecisionsPerRun == 2 }},
		{"aliased key and value", strings.Replace(scenarioWith("*runskey : *three", fullGate),
			"env: {AGENT_MODE: reference}", "env: {AGENT_MODE: &runskey runs, OTHER: &three 3}", 1),
			func(c ScenarioConfig) bool { return *c.Runs == 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadScenario([]byte(tc.doc))
			if err != nil {
				t.Fatalf("error = %v\n%s", err, tc.doc)
			}
			if !tc.check(cfg) {
				t.Errorf("decoded runs %v, gate %+v", *cfg.Runs, cfg.Gate)
			}
		})
	}
}
