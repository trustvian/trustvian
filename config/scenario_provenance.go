package config

// Scenario provenance (task 086): what a scenario execution records about the
// scenario it ran, so a comparison can say whether both sides ran the same
// definition, over the same inputs, with the same model and prompt.
//
// Two digests, computed by the CLI because the CLI reads the files:
//
//   - scenario_digest covers the scenario definition: the validated file
//     re-encoded as canonical JSON, so a comment, a reformat or reordered keys
//     change nothing. Environment variable values are excluded — they commonly
//     hold secrets, and a digest of a short secret can be brute-forced — and
//     only the keys are digested. Declared inputs are digested by path only;
//     their contents are input_digest's. The model and prompt declarations are
//     excluded: they are compared on their own, so an execution that changed
//     only its model reports same_scenario true and same_model false.
//   - input_digest covers the declared input files, byte for byte.
//
// The model and the prompt reference are declarations, never observations and
// never content: a model identifier and a prompt's name and digest. Trustvian
// does not store, fetch or render prompt text.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Input bounds (task 086 § 1).
const (
	// MaxScenarioInputs bounds the inputs: list.
	MaxScenarioInputs = 64
	// MaxScenarioInputBytes bounds one input file: 64 MiB.
	MaxScenarioInputBytes = 64 << 20
)

// scenarioDigestAlgorithm prefixes every digest, as the pricing digest's does.
const scenarioDigestAlgorithm = "sha256"

var (
	// provenanceIdentifierPattern is a model identifier or a prompt name: no
	// whitespace, so neither field can hold a sentence of prompt text.
	// "llama3.2", "gemma3:4b", "gpt-4o-2024-08-06", "support-agent/system@v14".
	provenanceIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`)

	// digestPattern is "sha256:" and 64 lowercase hex digits.
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ScenarioPromptRef identifies the prompt a side used: a name from the
// producer's own prompt store and the digest of the prompt's bytes. It is
// never prompt text, and no field for prompt text will be added beside it.
type ScenarioPromptRef struct {
	Name   string `yaml:"name"`
	Digest string `yaml:"digest"`
}

func (p ScenarioPromptRef) validate(field string) error {
	if !provenanceIdentifierPattern.MatchString(p.Name) {
		return scenarioError("%s.name must be 1..128 of [A-Za-z0-9._:/@+-], starting alphanumeric", field)
	}
	if !digestPattern.MatchString(p.Digest) {
		return scenarioError("%s.digest must be sha256: followed by 64 lowercase hex digits", field)
	}
	return nil
}

// validateProvenance checks one side's model and prompt declarations: each
// stated or named as an environment variable, never both.
func (s ScenarioSide) validateProvenance(side string) error {
	if s.Model != "" && s.ModelEnv != "" {
		return scenarioError("%s.model and %s.model_env are mutually exclusive", side, side)
	}
	if s.Model != "" && !provenanceIdentifierPattern.MatchString(s.Model) {
		return scenarioError("%s.model must be 1..128 of [A-Za-z0-9._:/@+-], starting alphanumeric", side)
	}
	if s.ModelEnv != "" && !envNamePattern.MatchString(s.ModelEnv) {
		return scenarioError("%s.model_env is not a valid variable name", side)
	}
	if s.PromptRef != nil && s.PromptRefEnv != "" {
		return scenarioError("%s.prompt_ref and %s.prompt_ref_env are mutually exclusive", side, side)
	}
	if s.PromptRef != nil {
		if err := s.PromptRef.validate(side + ".prompt_ref"); err != nil {
			return err
		}
	}
	if s.PromptRefEnv != "" && !envNamePattern.MatchString(s.PromptRefEnv) {
		return scenarioError("%s.prompt_ref_env is not a valid variable name", side)
	}
	return nil
}

// SideProvenance is what one side declared: a model and a prompt reference,
// each absent when the side declared none.
type SideProvenance struct {
	Model     string
	PromptRef *ScenarioPromptRef
}

// Provenance resolves the side's declarations. lookup reads the side's
// environment as its workload will see it — the process environment with the
// side's env applied — so model_env names the same variable the workload
// reads its model from. A variable that is unset or empty declares nothing.
//
// A prompt_ref_env value is "<name>@sha256:<64 hex>", split at its last
// "@sha256:". A value either variable holds that is not an identifier or a
// prompt reference is refused rather than recorded.
func (s ScenarioSide) Provenance(side string, lookup func(string) (string, bool)) (SideProvenance, error) {
	out := SideProvenance{Model: s.Model, PromptRef: s.PromptRef}
	if s.ModelEnv != "" {
		if value, ok := lookup(s.ModelEnv); ok && value != "" {
			if !provenanceIdentifierPattern.MatchString(value) {
				return SideProvenance{}, scenarioError("%s.model_env: %s does not hold a model identifier "+
					"(1..128 of [A-Za-z0-9._:/@+-])", side, s.ModelEnv)
			}
			out.Model = value
		}
	}
	if s.PromptRefEnv != "" {
		if value, ok := lookup(s.PromptRefEnv); ok && value != "" {
			at := strings.LastIndex(value, "@"+scenarioDigestAlgorithm+":")
			if at < 0 {
				return SideProvenance{}, scenarioError("%s.prompt_ref_env: %s must hold <name>@sha256:<digest>",
					side, s.PromptRefEnv)
			}
			ref := ScenarioPromptRef{Name: value[:at], Digest: value[at+1:]}
			if err := ref.validate(side + ".prompt_ref_env " + s.PromptRefEnv); err != nil {
				return SideProvenance{}, err
			}
			out.PromptRef = &ref
		}
	}
	return out, nil
}

// validateScenarioInputs checks the inputs: list as written. That each path
// exists and stays inside the repository is checked when the inputs are
// digested, against the file system.
func validateScenarioInputs(inputs []string) error {
	if len(inputs) > MaxScenarioInputs {
		return scenarioError("inputs lists %d files; at most %d", len(inputs), MaxScenarioInputs)
	}
	seen := make(map[string]bool, len(inputs))
	for i, p := range inputs {
		clean, err := cleanInputPath(p)
		if err != nil {
			return scenarioError("inputs[%d]: %v", i, err)
		}
		if seen[clean] {
			return scenarioError("inputs names %q twice", clean)
		}
		seen[clean] = true
	}
	return nil
}

// cleanInputPath is the form a path is digested under: slash-separated and
// lexically clean, so "./a//b.json" and "a/b.json" are one input on every
// operating system. A backslash is refused rather than read as a separator on
// one system and as a file name character on another.
func cleanInputPath(p string) (string, error) {
	switch {
	case p == "" || len(p) > maxScenarioValueLen:
		return "", fmt.Errorf("a path must be 1..%d bytes", maxScenarioValueLen)
	case strings.ContainsRune(p, '\\'):
		return "", errors.New("use / as the path separator")
	case path.IsAbs(p) || filepath.IsAbs(p) || filepath.VolumeName(p) != "":
		return "", fmt.Errorf("%q is absolute; inputs are relative to the scenario file", p)
	case !printableTarget(p):
		return "", errors.New("a path must be printable text")
	}
	clean := path.Clean(p)
	if clean == "." {
		return "", fmt.Errorf("%q names the scenario's directory, not a file", p)
	}
	return clean, nil
}

// Digest is scenario_digest: "sha256:" and the hex SHA-256 of the validated
// scenario's canonical encoding. Call only on a validated scenario.
//
// The encoding is JSON with a fixed field order. Environment variables appear
// as their sorted keys, inputs as their sorted clean paths, and
// max_calls_per_run sorted by target, so no ordering in the file changes the
// digest. A command's arguments keep their order: it is an argument vector.
// An omitted optional limit encodes as null, distinct from any value.
func (c ScenarioConfig) Digest() string {
	type canonicalSide struct {
		Command   []string `json:"command"`
		EnvKeys   []string `json:"env_keys"`
		Candidate string   `json:"candidate"`
	}
	side := func(s ScenarioSide) canonicalSide {
		keys := make([]string, 0, len(s.Env))
		for key := range s.Env {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		return canonicalSide{Command: s.Command, EnvKeys: keys, Candidate: s.Candidate}
	}
	type canonicalTargetLimit struct {
		Target string `json:"target"`
		Max    uint64 `json:"max"`
	}
	var calls []canonicalTargetLimit
	if c.Gate.MaxCallsPerRun != nil {
		calls = make([]canonicalTargetLimit, 0, len(c.Gate.MaxCallsPerRun))
		for _, l := range c.Gate.MaxCallsPerRun {
			calls = append(calls, canonicalTargetLimit{l.Target, *l.Max})
		}
		slices.SortFunc(calls, func(a, b canonicalTargetLimit) int { return strings.Compare(a.Target, b.Target) })
	}
	type canonicalGate struct {
		K                uint64                 `json:"added_candidate_presence_minimum"`
		J                uint64                 `json:"added_reference_presence_maximum"`
		MaxRepeatedAdded uint64                 `json:"max_repeated_added_behaviors"`
		MaxBlock         uint64                 `json:"max_block_decisions_per_run"`
		MaxCritical      uint64                 `json:"max_critical_risk_observations_per_run"`
		MinCandidateFreq *uint64                `json:"min_candidate_frequency"`
		MaxLostBehaviors *uint64                `json:"max_lost_behaviors"`
		MaxCallsPerRun   []canonicalTargetLimit `json:"max_calls_per_run"`
		MaxLLMCalls      *uint64                `json:"max_llm_calls_per_run"`
	}
	inputs := make([]string, 0, len(c.Inputs))
	for _, p := range c.Inputs {
		clean, _ := cleanInputPath(p) // validated
		inputs = append(inputs, clean)
	}
	slices.Sort(inputs)
	g := c.Gate
	canonical, err := json.Marshal(struct {
		Version         string        `json:"version"`
		Name            string        `json:"name"`
		Runs            int           `json:"runs"`
		Project         string        `json:"project"`
		Agent           string        `json:"agent"`
		Environment     string        `json:"environment"`
		Instrumentation string        `json:"instrumentation"`
		Inputs          []string      `json:"inputs"`
		Reference       canonicalSide `json:"reference"`
		Candidate       canonicalSide `json:"candidate"`
		Gate            canonicalGate `json:"gate"`
	}{
		Version: c.Version, Name: c.Name, Runs: *c.Runs, Project: c.Project, Agent: c.Agent,
		Environment: c.Environment, Instrumentation: c.Instrumentation, Inputs: inputs,
		Reference: side(c.Reference), Candidate: side(c.Candidate),
		Gate: canonicalGate{
			*g.AddedCandidatePresenceMinimum, *g.AddedReferencePresenceMaximum, *g.MaxRepeatedAddedBehaviors,
			*g.MaxBlockDecisionsPerRun, *g.MaxCriticalRiskObservationsPerRun,
			g.MinCandidateFrequency, g.MaxLostBehaviors, calls, g.MaxLLMCallsPerRun,
		},
	})
	if err != nil {
		// Unreachable: every field is a string, an integer or a list of them.
		panic("config: encoding a validated scenario: " + err.Error())
	}
	return digestOf(canonical)
}

func digestOf(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return scenarioDigestAlgorithm + ":" + hex.EncodeToString(sum[:])
}

// ScenarioInputs is what an execution records about its declared inputs.
// Declared is false when the scenario lists none, and Digest is then empty:
// no inputs is recorded as absence, never as the digest of an empty set.
type ScenarioInputs struct {
	Declared bool
	Digest   string
}

// InputDigest digests the declared inputs of the scenario at scenarioPath.
//
// Each path is resolved relative to the scenario file and must name a regular
// file of at most MaxScenarioInputBytes that stays inside the repository —
// the nearest directory above the scenario file holding .git, or the scenario
// file's own directory when there is none — after symbolic links are
// resolved.
//
// input_digest is "sha256:" and the hex SHA-256 of the canonical JSON list of
// {"path", "sha256"} pairs, sorted by clean path, where sha256 is the hex
// digest of the file's exact bytes: a changed line ending is a changed input.
func (c ScenarioConfig) InputDigest(scenarioPath string) (ScenarioInputs, error) {
	if len(c.Inputs) == 0 {
		return ScenarioInputs{}, nil
	}
	scenarioFile, err := filepath.Abs(scenarioPath)
	if err != nil {
		return ScenarioInputs{}, fmt.Errorf("config: resolve %s: %w", scenarioPath, err)
	}
	base := filepath.Dir(scenarioFile)
	root, err := filepath.EvalSymlinks(repositoryRoot(base))
	if err != nil {
		return ScenarioInputs{}, fmt.Errorf("config: resolve the repository of %s: %w", scenarioPath, err)
	}
	type pair struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	pairs := make([]pair, 0, len(c.Inputs))
	for _, p := range c.Inputs {
		clean, err := cleanInputPath(p)
		if err != nil {
			return ScenarioInputs{}, scenarioError("inputs: %v", err)
		}
		sum, err := digestInputFile(root, filepath.Join(base, filepath.FromSlash(clean)))
		if err != nil {
			return ScenarioInputs{}, scenarioError("inputs: %s: %v", clean, err)
		}
		pairs = append(pairs, pair{Path: clean, SHA256: sum})
	}
	slices.SortFunc(pairs, func(a, b pair) int { return strings.Compare(a.Path, b.Path) })
	canonical, err := json.Marshal(pairs)
	if err != nil {
		panic("config: encoding input digests: " + err.Error())
	}
	return ScenarioInputs{Declared: true, Digest: digestOf(canonical)}, nil
}

// repositoryRoot is the nearest directory at or above dir that holds .git, or
// dir itself when none does.
func repositoryRoot(dir string) string {
	for candidate := dir; ; {
		if _, err := os.Lstat(filepath.Join(candidate, ".git")); err == nil {
			return candidate
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return dir
		}
		candidate = parent
	}
}

// digestInputFile is the hex SHA-256 of one input file, refusing one outside
// root, one that is not a regular file and one past the size bound.
func digestInputFile(root, name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", errors.New("does not exist or cannot be resolved")
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("is outside the repository")
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", errors.New("cannot be opened")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("is not a regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxScenarioInputBytes+1))
	if err != nil {
		return "", fmt.Errorf("cannot be read: %w", err)
	}
	if n > MaxScenarioInputBytes {
		return "", errors.New("is larger than " + strconv.Itoa(MaxScenarioInputBytes>>20) + " MiB")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
