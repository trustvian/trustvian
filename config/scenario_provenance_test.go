package config

// Task 086: scenario_digest, input_digest and the side provenance
// declarations.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// provenanceScenario exercises every field the digest covers, including the
// optional limits and inputs.
const provenanceScenario = `version: v1
name: support-login
runs: 3
project: shop
agent: support
environment: local
instrumentation: existing
inputs: [fixtures/a.json, fixtures/b.json]
reference:
  command: [python, agent.py, --mode, reference]
  env: {AGENT_MODE: reference, API_TOKEN: s3cret}
  candidate: ref-candidate
  model: llama3.2
candidate:
  command: [python, agent.py, --mode, candidate]
  env: {AGENT_MODE: candidate, API_TOKEN: s3cret}
  model_env: OLLAMA_MODEL
  prompt_ref: {name: support/system@v14, digest: "sha256:` + zeros64 + `"}
gate:
  added_candidate_presence_minimum: 2
  added_reference_presence_maximum: 0
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0
  min_candidate_frequency: 1
  max_calls_per_run:
    - {target: crm.localhost, max: 9}
    - {target: kb.localhost, max: 4}
`

const zeros64 = "0000000000000000000000000000000000000000000000000000000000000000"

func mustScenario(t *testing.T, doc string) ScenarioConfig {
	t.Helper()
	cfg, err := LoadScenario([]byte(doc))
	if err != nil {
		t.Fatalf("LoadScenario() error = %v", err)
	}
	return cfg
}

// TestScenarioDigestIgnoresFormattingCommentsAndOrder: the same definition
// written differently — keys reordered, block versus flow style, comments,
// quoting, map and list order, "./" on a path — has one digest.
func TestScenarioDigestIgnoresFormattingCommentsAndOrder(t *testing.T) {
	rewritten := `# the same scenario, written by someone else
gate:
  max_calls_per_run: [{max: 4, target: kb.localhost}, {target: crm.localhost, max: 9}]
  min_candidate_frequency: 1
  max_critical_risk_observations_per_run: 0
  max_block_decisions_per_run: 0
  max_repeated_added_behaviors: 0
  added_reference_presence_maximum: 0
  added_candidate_presence_minimum: 2   # k
candidate:
  prompt_ref:
    digest: "sha256:` + zeros64 + `"
    name: "support/system@v14"
  model_env: OLLAMA_MODEL
  env:
    API_TOKEN: s3cret
    AGENT_MODE: candidate
  command:
    - python
    - agent.py
    - --mode
    - candidate
reference:
  model: "llama3.2"
  candidate: ref-candidate
  env: {API_TOKEN: s3cret, AGENT_MODE: reference}
  command: [python, agent.py, --mode, reference]
inputs:
  - ./fixtures/b.json
  - fixtures//a.json
instrumentation: existing
environment: local
agent: support
project: shop
runs: 3
name: "support-login"
version: "v1"
`
	want := mustScenario(t, provenanceScenario).Digest()
	if got := mustScenario(t, rewritten).Digest(); got != want {
		t.Fatalf("a reformatted scenario digests to %s, the original to %s", got, want)
	}
	if !digestPattern.MatchString(want) {
		t.Fatalf("digest %q is not sha256:<64 hex>", want)
	}
}

// TestScenarioDigestChangesOnAnyDefinitionValue: every value the definition
// holds is digested, so changing any one of them changes the digest.
func TestScenarioDigestChangesOnAnyDefinitionValue(t *testing.T) {
	base := mustScenario(t, provenanceScenario).Digest()
	for _, tt := range []struct{ name, prefix, with string }{
		{"name", "name:", "name: support-logout"},
		{"runs", "runs:", "runs: 4"},
		{"project", "project:", "project: other"},
		{"agent", "agent:", "agent: other"},
		{"environment", "environment:", "environment: staging"},
		{"instrumentation", "instrumentation:", "instrumentation: auto"},
		{"an input path", "inputs:", "inputs: [fixtures/a.json, fixtures/c.json]"},
		{"an input removed", "inputs:", "inputs: [fixtures/a.json]"},
		{"a command argument", "command: [python, agent.py, --mode, reference]",
			"command: [python, agent.py, --mode, candidate]"},
		{"argument order", "command: [python, agent.py, --mode, reference]",
			"command: [python, --mode, agent.py, reference]"},
		{"an env key", "env: {AGENT_MODE: reference, API_TOKEN: s3cret}",
			"env: {AGENT_MODE: reference, API_KEY: s3cret}"},
		{"a side's candidate", "candidate: ref-candidate", "candidate: other-candidate"},
		{"k", "added_candidate_presence_minimum:", "added_candidate_presence_minimum: 3"},
		{"a required limit", "max_block_decisions_per_run:", "max_block_decisions_per_run: 1"},
		{"an optional limit", "min_candidate_frequency:", "min_candidate_frequency: 2"},
		{"an optional limit removed", "min_candidate_frequency:", ""},
		{"an optional limit added", "max_critical_risk_observations_per_run:",
			"max_critical_risk_observations_per_run: 0\n  max_lost_behaviors: 0"},
		{"a per-target maximum", "- {target: crm.localhost, max: 9}", "- {target: crm.localhost, max: 10}"},
		{"a per-target name", "- {target: kb.localhost, max: 4}", "- {target: search.localhost, max: 4}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changed := mustScenario(t, replaceLine(t, provenanceScenario, tt.prefix, tt.with)).Digest()
			if changed == base {
				t.Fatalf("changing %s left the digest at %s", tt.name, base)
			}
		})
	}
}

// TestScenarioDigestExcludesEnvValuesAndProvenance: an environment value is
// never digested (it may be a secret), and the model and prompt declarations
// are compared on their own, so none of them moves scenario_digest.
func TestScenarioDigestExcludesEnvValuesAndProvenance(t *testing.T) {
	base := mustScenario(t, provenanceScenario).Digest()
	for _, tt := range []struct{ name, prefix, with string }{
		{"an env value", "env: {AGENT_MODE: reference, API_TOKEN: s3cret}",
			"env: {AGENT_MODE: reference, API_TOKEN: another-secret}"},
		{"a model", "model: llama3.2", "model: gemma3:4b"},
		{"a model removed", "model: llama3.2", ""},
		{"a model variable", "model_env:", "model_env: MODEL"},
		{"a prompt reference", "prompt_ref:", `prompt_ref: {name: support/system@v15, digest: "sha256:` +
			strings.Repeat("1", 64) + `"}`},
		{"a prompt reference variable", "prompt_ref:", "prompt_ref_env: PROMPT_REF"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustScenario(t, replaceLine(t, provenanceScenario, tt.prefix, tt.with)).Digest(); got != base {
				t.Fatalf("changing %s changed the digest", tt.name)
			}
		})
	}
}

// TestScenarioDigestMatchesTheDocumentedEncoding reproduces the digest by
// hand from the documented canonical form, field order and all.
func TestScenarioDigestMatchesTheDocumentedEncoding(t *testing.T) {
	canonical := `{"version":"v1","name":"support-login","runs":5,"project":"","agent":"",` +
		`"environment":"","instrumentation":"","inputs":[],` +
		`"reference":{"command":["python","agent.py"],"env_keys":["AGENT_MODE"],"candidate":""},` +
		`"candidate":{"command":["python","agent.py"],"env_keys":["AGENT_MODE"],"candidate":""},` +
		`"gate":{"added_candidate_presence_minimum":1,"added_reference_presence_maximum":0,` +
		`"max_repeated_added_behaviors":0,"max_block_decisions_per_run":0,` +
		`"max_critical_risk_observations_per_run":0,"min_candidate_frequency":null,` +
		`"max_lost_behaviors":null,"max_calls_per_run":null}}`
	sum := sha256.Sum256([]byte(canonical))
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got := mustScenario(t, validScenario).Digest(); got != want {
		t.Fatalf("Digest() = %s, the documented encoding gives %s", got, want)
	}
}

// inputRepository is a repository with a scenario file one directory down and
// two fixtures beside it.
func inputRepository(t *testing.T) (root, scenarioPath string) {
	t.Helper()
	root = t.TempDir()
	for name, content := range map[string]string{
		".git/HEAD":                         "ref: refs/heads/main\n",
		"scenarios/support.yaml":            "",
		"scenarios/fixtures/a.json":         "{\"ticket\": 1}\n",
		"scenarios/fixtures/b.json":         "{\"ticket\": 2}\n",
		"shared/c.json":                     "{\"ticket\": 3}\n",
		"scenarios/fixtures/nested/d.jsonl": "{}\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, filepath.Join(root, "scenarios", "support.yaml")
}

func withInputs(t *testing.T, inputs ...string) ScenarioConfig {
	t.Helper()
	cfg := mustScenario(t, validScenario)
	cfg.Inputs = inputs
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	return cfg
}

func inputDigest(t *testing.T, cfg ScenarioConfig, scenarioPath string) string {
	t.Helper()
	inputs, err := cfg.InputDigest(scenarioPath)
	if err != nil {
		t.Fatalf("InputDigest() error = %v", err)
	}
	if !inputs.Declared || !digestPattern.MatchString(inputs.Digest) {
		t.Fatalf("InputDigest() = %+v", inputs)
	}
	return inputs.Digest
}

// TestInputDigestIsStableAndByteExact: reordering and respelling the list
// changes nothing, a path inside the repository but above the scenario is an
// input like any other, and one changed byte — a line ending included — is a
// changed digest.
func TestInputDigestIsStableAndByteExact(t *testing.T) {
	root, scenarioPath := inputRepository(t)
	base := inputDigest(t, withInputs(t, "fixtures/a.json", "fixtures/b.json", "../shared/c.json"), scenarioPath)
	if got := inputDigest(t, withInputs(t, "../shared/c.json", "./fixtures/b.json", "fixtures//a.json"),
		scenarioPath); got != base {
		t.Fatalf("reordered inputs digest to %s, want %s", got, base)
	}

	a := filepath.Join(root, "scenarios", "fixtures", "a.json")
	for _, content := range []string{"{\"ticket\": 9}\n", "{\"ticket\": 1}\r\n"} {
		if err := os.WriteFile(a, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := inputDigest(t, withInputs(t, "fixtures/a.json", "fixtures/b.json", "../shared/c.json"),
			scenarioPath); got == base {
			t.Fatalf("changing a.json to %q left the digest unchanged", content)
		}
	}
}

// TestInputDigestMatchesTheDocumentedEncoding reproduces input_digest by hand.
func TestInputDigestMatchesTheDocumentedEncoding(t *testing.T) {
	_, scenarioPath := inputRepository(t)
	hexOf := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	canonical := `[{"path":"fixtures/a.json","sha256":"` + hexOf("{\"ticket\": 1}\n") + `"},` +
		`{"path":"fixtures/nested/d.jsonl","sha256":"` + hexOf("{}\n") + `"}]`
	want := "sha256:" + hexOf(canonical)
	if got := inputDigest(t, withInputs(t, "fixtures/nested/d.jsonl", "fixtures/a.json"), scenarioPath); got != want {
		t.Fatalf("InputDigest() = %s, the documented encoding gives %s", got, want)
	}
}

// TestNoInputsIsRecordedAsAbsence: not declared, and no digest — never the
// digest of an empty list.
func TestNoInputsIsRecordedAsAbsence(t *testing.T) {
	_, scenarioPath := inputRepository(t)
	inputs, err := mustScenario(t, validScenario).InputDigest(scenarioPath)
	if err != nil || inputs != (ScenarioInputs{}) {
		t.Fatalf("InputDigest() = %+v, %v; want not declared", inputs, err)
	}
	empty := withInputs(t)
	if inputs, err := empty.InputDigest(scenarioPath); err != nil || inputs.Declared {
		t.Fatalf("inputs: [] = %+v, %v; want not declared", inputs, err)
	}
}

// TestInputDigestRefusesWhatItCannotDigest names the input.
func TestInputDigestRefusesWhatItCannotDigest(t *testing.T) {
	root, scenarioPath := inputRepository(t)
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "scenarios", "fixtures", "link.json")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	large := filepath.Join(root, "scenarios", "fixtures", "large.bin")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxScenarioInputBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, tt := range []struct{ input, mentions string }{
		{"fixtures/missing.json", "does not exist"},
		{"fixtures/nested", "not a regular file"},
		{"../../outside.json", "does not exist"},
		{"fixtures/link.json", "outside the repository"},
		{"fixtures/large.bin", "larger than 64 MiB"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			_, err := withInputs(t, tt.input).InputDigest(scenarioPath)
			if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), tt.mentions) ||
				!strings.Contains(err.Error(), filepath.ToSlash(filepathClean(tt.input))) {
				t.Fatalf("error = %v, want one naming %s and %q", err, tt.input, tt.mentions)
			}
		})
	}
	// Exactly at the bound is accepted.
	if err := os.Truncate(large, MaxScenarioInputBytes); err != nil {
		t.Fatal(err)
	}
	inputDigest(t, withInputs(t, "fixtures/large.bin"), scenarioPath)
}

func filepathClean(p string) string { return filepath.ToSlash(filepath.Clean(p)) }

// TestScenarioInputsAreValidatedAsWritten: before the file system is read.
func TestScenarioInputsAreValidatedAsWritten(t *testing.T) {
	tooMany := make([]string, MaxScenarioInputs+1)
	for i := range tooMany {
		tooMany[i] = "f" + strings.Repeat("x", i)
	}
	for _, tt := range []struct {
		name     string
		inputs   []string
		mentions string
	}{
		{"too many", tooMany, "at most 64"},
		{"empty", []string{""}, "1..4096 bytes"},
		{"absolute", []string{"/etc/passwd"}, "absolute"},
		{"backslash", []string{`fixtures\a.json`}, "separator"},
		{"the directory itself", []string{"./"}, "not a file"},
		{"the same file twice", []string{"a.json", "./a.json"}, "twice"},
		{"a control character", []string{"a\x01.json"}, "printable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustScenario(t, validScenario)
			cfg.Inputs = tt.inputs
			err := cfg.Validate()
			if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), tt.mentions) {
				t.Fatalf("Validate() = %v, want it to mention %q", err, tt.mentions)
			}
		})
	}
	cfg := mustScenario(t, validScenario)
	cfg.Inputs = tooMany[:MaxScenarioInputs]
	if err := cfg.Validate(); err != nil {
		t.Fatalf("64 inputs: %v", err)
	}
}

// TestSideProvenanceResolution: stated, read from the side's environment, or
// absent — and a value that is not an identifier is refused, not recorded.
func TestSideProvenanceResolution(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	env := map[string]string{
		"OLLAMA_MODEL": "gemma3:4b", "PROMPT_REF": "support/system@v14@" + digest,
		"EMPTY": "", "SENTENCE": "You are a helpful assistant", "NO_DIGEST": "support/system@v14",
	}
	lookup := func(name string) (string, bool) { v, ok := env[name]; return v, ok }
	ref := &ScenarioPromptRef{Name: "support/system@v14", Digest: digest}
	for _, tt := range []struct {
		name     string
		side     ScenarioSide
		want     SideProvenance
		mentions string
	}{
		{"nothing declared", ScenarioSide{}, SideProvenance{}, ""},
		{"stated", ScenarioSide{Model: "llama3.2", PromptRef: ref}, SideProvenance{"llama3.2", ref}, ""},
		{"from the environment", ScenarioSide{ModelEnv: "OLLAMA_MODEL", PromptRefEnv: "PROMPT_REF"},
			SideProvenance{"gemma3:4b", ref}, ""},
		{"unset", ScenarioSide{ModelEnv: "UNSET", PromptRefEnv: "UNSET"}, SideProvenance{}, ""},
		{"empty", ScenarioSide{ModelEnv: "EMPTY", PromptRefEnv: "EMPTY"}, SideProvenance{}, ""},
		{"prose is not a model", ScenarioSide{ModelEnv: "SENTENCE"}, SideProvenance{}, "model identifier"},
		{"prose is not a prompt reference", ScenarioSide{PromptRefEnv: "SENTENCE"}, SideProvenance{},
			"<name>@sha256:<digest>"},
		{"a name without a digest", ScenarioSide{PromptRefEnv: "NO_DIGEST"}, SideProvenance{},
			"<name>@sha256:<digest>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.side.Provenance("candidate", lookup)
			if tt.mentions != "" {
				if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), tt.mentions) {
					t.Fatalf("error = %v, want one mentioning %q", err, tt.mentions)
				}
				return
			}
			if err != nil || got.Model != tt.want.Model ||
				(got.PromptRef == nil) != (tt.want.PromptRef == nil) ||
				(got.PromptRef != nil && *got.PromptRef != *tt.want.PromptRef) {
				t.Fatalf("Provenance() = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

// TestSideProvenanceDeclarationsAreValidated by name, when the file loads.
func TestSideProvenanceDeclarationsAreValidated(t *testing.T) {
	digest := `"sha256:` + zeros64 + `"`
	for _, tt := range []struct{ name, prefix, with, mentions string }{
		{"model and model_env", "model: llama3.2", "model: llama3.2\n  model_env: OLLAMA_MODEL",
			"mutually exclusive"},
		{"prompt_ref and prompt_ref_env", "model_env:", "model_env: OLLAMA_MODEL\n  prompt_ref_env: PROMPT_REF",
			"mutually exclusive"},
		{"a model with spaces", "model: llama3.2", `model: "you are a helpful assistant"`, "reference.model"},
		{"a model too long", "model: llama3.2", "model: " + strings.Repeat("m", 129), "reference.model"},
		{"an invalid variable", "model_env:", "model_env: 1BAD", "model_env"},
		{"a prompt name with spaces", "prompt_ref:", `prompt_ref: {name: "be nice", digest: ` + digest + `}`,
			"prompt_ref.name"},
		{"a prompt digest in upper case", "prompt_ref:", `prompt_ref: {name: sys, digest: "sha256:` +
			strings.Repeat("A", 64) + `"}`, "prompt_ref.digest"},
		{"a prompt digest of another algorithm", "prompt_ref:", `prompt_ref: {name: sys, digest: "md5:abc"}`,
			"prompt_ref.digest"},
		{"a prompt text field", "prompt_ref:", `prompt_ref: {name: sys, digest: ` + digest + `, text: hi}`,
			"text"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadScenario([]byte(replaceLine(t, provenanceScenario, tt.prefix, tt.with)))
			if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), tt.mentions) {
				t.Fatalf("error = %v, want one mentioning %q", err, tt.mentions)
			}
		})
	}
}
