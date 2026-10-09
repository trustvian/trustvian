package main

// Task 086 in `eval run`: the scenario's digests and each side's declarations
// reach the begin request exactly as config computes them, nothing runs when
// they cannot be computed, and what the control plane recorded and compared is
// printed as it said it.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trustvian/trustvian/config"
)

const provenanceScenarioYAML = `version: v1
name: support
runs: 3
inputs: [fixtures/tickets.json]
reference:
  command: [sh, ref.sh]
  env: {MODE: reference}
  model: llama3.2
candidate:
  command: [sh, can.sh]
  env: {MODE: candidate}
  model_env: TV_TEST_MODEL
  prompt_ref: {name: support/system@v14, digest: "sha256:` + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" + `"}
gate:
  added_candidate_presence_minimum: 2
  added_reference_presence_maximum: 0
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0
`

// writeProvenanceScenario writes the scenario and its one input.
func writeProvenanceScenario(t *testing.T, scenario string) string {
	t.Helper()
	path := writeScenario(t, scenario)
	input := filepath.Join(filepath.Dir(path), "fixtures", "tickets.json")
	if err := os.MkdirAll(filepath.Dir(input), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte(`[{"ticket":1}]`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type beginProvenance struct {
	Provenance *struct {
		ScenarioDigest string          `json:"scenario_digest"`
		InputsDeclared bool            `json:"inputs_declared"`
		InputDigest    string          `json:"input_digest"`
		Reference      json.RawMessage `json:"reference"`
		Candidate      json.RawMessage `json:"candidate"`
	} `json:"provenance"`
}

func TestEvalRunSendsTheScenarioProvenance(t *testing.T) {
	t.Setenv("TV_TEST_MODEL", "gemma3:4b")
	path := writeProvenanceScenario(t, provenanceScenarioYAML)
	cfg, err := config.LoadScenarioFile(path)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := cfg.InputDigest(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name      string
		args      []string
		reference string
	}{
		{"self-contained", nil, `{"model":"llama3.2"}`},
		// The reused reference side's declarations are the recorded
		// execution's; none is sent for it.
		{"reused reference", []string{"--reference", "last"}, `{}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := newScenarioAPI(t, "pass")
			args := append([]string{"--scenario", path}, tt.args...)
			code, _, stderr := runScenario(t, &recorder{}, api.url(), args...)
			if code != exitOK {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			var body beginProvenance
			if err := json.Unmarshal(api.request(t, "/v1/scenario-executions"), &body); err != nil {
				t.Fatal(err)
			}
			p := body.Provenance
			if p == nil || p.ScenarioDigest != cfg.Digest() || !p.InputsDeclared || p.InputDigest != inputs.Digest {
				t.Fatalf("provenance = %+v, want digests %s and %s", p, cfg.Digest(), inputs.Digest)
			}
			if string(p.Reference) != tt.reference {
				t.Errorf("reference = %s, want %s", p.Reference, tt.reference)
			}
			if want := `{"model":"gemma3:4b","prompt_ref":{"name":"support/system@v14","digest":"sha256:` +
				strings.Repeat("c", 64) + `"}}`; string(p.Candidate) != want {
				t.Errorf("candidate = %s, want %s", p.Candidate, want)
			}
			if !strings.Contains(stderr, "recorded scenario_digest "+cfg.Digest()+", input_digest "+inputs.Digest) {
				t.Errorf("stderr does not report the recorded digests:\n%s", stderr)
			}
		})
	}
}

// TestEvalRunRefusesProvenanceItCannotRecord: a missing input or a model
// variable holding prose is a usage error, and nothing is begun or run.
func TestEvalRunRefusesProvenanceItCannotRecord(t *testing.T) {
	for _, tt := range []struct {
		name, model string
		dropInput   bool
		mentions    string
	}{
		{"a missing input", "gemma3:4b", true, "fixtures/tickets.json"},
		{"prose as a model", "You are a helpful assistant", false, "model_env"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TV_TEST_MODEL", tt.model)
			path := writeProvenanceScenario(t, provenanceScenarioYAML)
			if tt.dropInput {
				if err := os.Remove(filepath.Join(filepath.Dir(path), "fixtures", "tickets.json")); err != nil {
					t.Fatal(err)
				}
			}
			api := newScenarioAPI(t, "pass")
			rec := &recorder{}
			code, _, stderr := runScenario(t, rec, api.url(), "--scenario", path)
			if code != exitUsage || !strings.Contains(stderr, tt.mentions) {
				t.Fatalf("exit %d, stderr %q; want 2 naming %s", code, stderr, tt.mentions)
			}
			if len(api.paths()) != 0 || len(rec.calls) != 0 {
				t.Fatalf("requests %v and %d repetitions after a usage error", api.paths(), len(rec.calls))
			}
		})
	}
}

const (
	recordedProvenance = `{"reference":{"scenario_digest":"sha256:aaaa","inputs":"not_declared","model":"llama3.2"},` +
		`"candidate":{"scenario_digest":"sha256:aaaa","inputs":"not_declared","model":"gemma3:4b"}}`
	samenessMembers = `"sameness":{"same_scenario":"true","same_inputs":"true","same_model":"false",` +
		`"same_prompt_ref":"not_recorded",` +
		`"reference":{"scenario_digest":"sha256:aaaa","inputs":"not_declared","model":"llama3.2"},` +
		`"candidate":{"scenario_digest":"sha256:aaaa","inputs":"not_declared","model":"gemma3:4b"}},` +
		`"warnings":[{"code":"model_differs","text":"The reference and candidate sides declared different models."}]`
)

// TestEvalRunPrintsWhatWasRecordedAndCompared: the human rendering transcribes
// the recorded provenance and the sameness block; --json carries both
// verbatim, the comparison untouched.
func TestEvalRunPrintsWhatWasRecordedAndCompared(t *testing.T) {
	api := newScenarioAPI(t, "pass")
	api.beginProvenance, api.sameness = recordedProvenance, samenessMembers
	code, stdout, _ := runScenario(t, &recorder{}, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{
		"Recorded\n  reference: scenario sha256:aaaa   inputs not_declared   model llama3.2   prompt_ref not_recorded\n" +
			"  candidate: scenario sha256:aaaa   inputs not_declared   model gemma3:4b   prompt_ref not_recorded\n",
		"Sameness\n  same_scenario    true\n  same_inputs      true\n  same_model       false\n" +
			"  same_prompt_ref  not_recorded\n",
		"  warning model_differs: The reference and candidate sides declared different models.\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}

	api = newScenarioAPI(t, "pass")
	api.beginProvenance, api.sameness = recordedProvenance, samenessMembers
	code, stdout, _ = runScenario(t, &recorder{}, api.url(), "--scenario", writeScenario(t, runScenarioYAML),
		"--json")
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	var document struct {
		Provenance json.RawMessage `json:"provenance"`
		Comparison json.RawMessage `json:"comparison"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatal(err)
	}
	if string(document.Provenance) != recordedProvenance {
		t.Errorf("document provenance = %s", document.Provenance)
	}
	var server bytes.Buffer
	if err := json.Compact(&server, []byte(strings.TrimSuffix(repeatedReply("pass"), "}")+","+samenessMembers+"}")); err != nil {
		t.Fatal(err)
	}
	if string(document.Comparison) != server.String() {
		t.Errorf("comparison changed on the way through:\n got %s\nwant %s", document.Comparison, server.String())
	}
}

// TestEvalRunToleratesAnOlderServerWithoutProvenance: an older control plane
// returns neither, and the rendering says nothing about them.
func TestEvalRunToleratesAnOlderServerWithoutProvenance(t *testing.T) {
	api := newScenarioAPI(t, "pass")
	code, stdout, _ := runScenario(t, &recorder{}, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	if code != exitOK || strings.Contains(stdout, "Recorded") || strings.Contains(stdout, "Sameness") {
		t.Fatalf("exit %d:\n%s", code, stdout)
	}
}
