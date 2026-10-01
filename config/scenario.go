package config

// The behavioral scenario file (task 078): how to run a workload repeatably,
// how many times, and the limits its behavior must satisfy.
//
//	version: v1
//	name: support-login
//	runs: 5
//	reference:
//	  command: [python, agent.py]
//	  env: {AGENT_MODE: reference}
//	candidate:
//	  command: [python, agent.py]
//	  env: {AGENT_MODE: candidate}
//	gate:
//	  added_candidate_presence_minimum: 1      # k
//	  added_reference_presence_maximum: 0      # j
//	  max_repeated_added_behaviors: 0
//	  max_block_decisions_per_run: 0
//	  max_critical_risk_observations_per_run: 0
//
// Every gate field and `runs` is required and none has a default: zero is a
// meaningful strict maximum, so zero cannot also mean "unset", and an inferred
// default fails toward looking permissive (task 056). The documented guidance
// `k: 1, j: 0` is guidance, never a value this loader supplies. Unknown fields
// are refused — a misspelled limit that decoded to its zero value would be
// strict only by luck.
//
// A scenario file is executable configuration: it names a command, read from
// the developer's own repository and run under their privileges, exactly as a
// Makefile is. Nothing here sandboxes it.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	yaml "go.yaml.in/yaml/v3"
)

// ScenarioSchemaVersionV1 is the only scenario schema version this build reads.
const ScenarioSchemaVersionV1 = "v1"

// MaxScenarioRuns bounds `runs`, per side: one execution at runs: N creates 2N
// evaluation runs and 2N learning scopes, and 64 is where that stops being a
// CI job (task 078 § runs is required and bounded).
const MaxScenarioRuns = 64

// Scenario bounds on the command each side runs.
const (
	maxScenarioArgs     = 64
	maxScenarioEnv      = 64
	maxScenarioValueLen = 4096
)

// ErrInvalidScenario reports a scenario file that cannot run as written. The
// message names the field.
var ErrInvalidScenario = errors.New("config: invalid scenario")

// ScenarioConfig is one parsed, validated scenario file.
type ScenarioConfig struct {
	Version string `yaml:"version"`
	Name    string `yaml:"name"`
	// Runs is N: required, 1..64. A pointer, so an omitted field is told
	// apart from an explicit value.
	Runs *int `yaml:"runs"`

	// Optional pass-throughs to `trustvian dev`, which derives each when
	// omitted exactly as it does on its own command line. Not interpreted here.
	Project         string `yaml:"project"`
	Agent           string `yaml:"agent"`
	Environment     string `yaml:"environment"`
	Instrumentation string `yaml:"instrumentation"`

	Reference ScenarioSide `yaml:"reference"`
	Candidate ScenarioSide `yaml:"candidate"`
	Gate      ScenarioGate `yaml:"gate"`
}

// ScenarioSide is how one side's repetitions run.
type ScenarioSide struct {
	Command []string          `yaml:"command"`
	Env     map[string]string `yaml:"env"`
	// Candidate optionally names the candidate this side evaluates; omitted,
	// `trustvian dev` derives it from the repository as it always does.
	Candidate string `yaml:"candidate"`
}

// ScenarioGate holds the five required thresholds. Pointers, so each omission
// is reported by name rather than decoded to a strict zero nobody chose.
type ScenarioGate struct {
	AddedCandidatePresenceMinimum     *uint64 `yaml:"added_candidate_presence_minimum"`
	AddedReferencePresenceMaximum     *uint64 `yaml:"added_reference_presence_maximum"`
	MaxRepeatedAddedBehaviors         *uint64 `yaml:"max_repeated_added_behaviors"`
	MaxBlockDecisionsPerRun           *uint64 `yaml:"max_block_decisions_per_run"`
	MaxCriticalRiskObservationsPerRun *uint64 `yaml:"max_critical_risk_observations_per_run"`
}

var (
	scenarioNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	envNamePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

func scenarioError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidScenario}, args...)...)
}

// Validate checks every field and bound, naming the first field that fails.
func (c ScenarioConfig) Validate() error {
	if c.Version != ScenarioSchemaVersionV1 {
		return scenarioError("version must be %q, got %q", ScenarioSchemaVersionV1, c.Version)
	}
	if !scenarioNamePattern.MatchString(c.Name) {
		return scenarioError("name is required: 1..128 of [A-Za-z0-9._-], starting alphanumeric")
	}
	if c.Runs == nil {
		return scenarioError("runs is required; there is no default")
	}
	if *c.Runs < 1 || *c.Runs > MaxScenarioRuns {
		return scenarioError("runs is %d; it must be within 1..%d", *c.Runs, MaxScenarioRuns)
	}
	for _, side := range []struct {
		name string
		side ScenarioSide
	}{{"reference", c.Reference}, {"candidate", c.Candidate}} {
		if err := side.side.validate(side.name); err != nil {
			return err
		}
	}
	return c.Gate.validate(uint64(*c.Runs))
}

func (s ScenarioSide) validate(name string) error {
	if len(s.Command) == 0 {
		return scenarioError("%s.command is required", name)
	}
	if len(s.Command) > maxScenarioArgs {
		return scenarioError("%s.command has %d arguments; at most %d", name, len(s.Command), maxScenarioArgs)
	}
	for i, arg := range s.Command {
		if (i == 0 && arg == "") || len(arg) > maxScenarioValueLen {
			return scenarioError("%s.command[%d] is empty or longer than %d bytes", name, i, maxScenarioValueLen)
		}
	}
	if len(s.Env) > maxScenarioEnv {
		return scenarioError("%s.env has %d entries; at most %d", name, len(s.Env), maxScenarioEnv)
	}
	for key, value := range s.Env {
		if !envNamePattern.MatchString(key) {
			return scenarioError("%s.env has an invalid variable name %q", name, key)
		}
		if len(value) > maxScenarioValueLen {
			return scenarioError("%s.env.%s is longer than %d bytes", name, key, maxScenarioValueLen)
		}
	}
	return nil
}

func (g ScenarioGate) validate(runs uint64) error {
	for _, field := range []struct {
		name  string
		value *uint64
	}{
		{"added_candidate_presence_minimum", g.AddedCandidatePresenceMinimum},
		{"added_reference_presence_maximum", g.AddedReferencePresenceMaximum},
		{"max_repeated_added_behaviors", g.MaxRepeatedAddedBehaviors},
		{"max_block_decisions_per_run", g.MaxBlockDecisionsPerRun},
		{"max_critical_risk_observations_per_run", g.MaxCriticalRiskObservationsPerRun},
	} {
		if field.value == nil {
			return scenarioError("gate.%s is required; there is no default", field.name)
		}
	}
	k, j := *g.AddedCandidatePresenceMinimum, *g.AddedReferencePresenceMaximum
	if k < 1 || k > runs {
		return scenarioError("gate.added_candidate_presence_minimum (k) is %d; it must be within 1..%d", k, runs)
	}
	if j >= k {
		return scenarioError("gate.added_reference_presence_maximum (j) is %d; it must be below k (%d)", j, k)
	}
	return nil
}

// LoadScenario decodes and validates one scenario document. Strict: unknown
// fields and duplicate keys are refused, as every loader here does.
func LoadScenario(data []byte) (ScenarioConfig, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return ScenarioConfig{}, ErrEmptyInput
	}
	var cfg ScenarioConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return ScenarioConfig{}, ErrEmptyInput
		}
		return ScenarioConfig{}, fmt.Errorf("%w: decode: %w", ErrInvalidScenario, err)
	}
	if err := requireIntegerCounts(data); err != nil {
		return ScenarioConfig{}, err
	}
	if err := cfg.Validate(); err != nil {
		return ScenarioConfig{}, err
	}
	return cfg, nil
}

// LoadScenarioFile reads the file at path, bounded like every other config
// file, and calls LoadScenario.
func LoadScenarioFile(path string) (ScenarioConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return ScenarioConfig{}, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFileSize+1))
	if err != nil {
		return ScenarioConfig{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if len(data) > maxConfigFileSize {
		return ScenarioConfig{}, fmt.Errorf("%w: %s (max %d bytes)", ErrFileTooLarge, path, maxConfigFileSize)
	}
	cfg, err := LoadScenario(data)
	if err != nil {
		return ScenarioConfig{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// requireIntegerCounts refuses a count or limit written as anything but a YAML
// integer.
//
// The decoder converts a float into an integer field rather than refusing it:
// `runs: 5.9` decodes as 5, `k: 1.9` as 1, `-0.5` into an unsigned limit as 0,
// and an integer too large for 64 bits resolves as a float and wraps. Each is a
// threshold nobody wrote, so the scalar's resolved type is checked on the
// document itself, before Validate reads the converted value. A null is left
// to Validate, which reports the field as missing.
func requireIntegerCounts(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidScenario, err)
	}
	root := &doc
	fields := []struct {
		name  string
		value *yaml.Node
	}{{"runs", mappingValue(root, "runs")}}
	if gate := mappingValue(root, "gate"); gate != nil {
		for _, name := range []string{
			"added_candidate_presence_minimum",
			"added_reference_presence_maximum",
			"max_repeated_added_behaviors",
			"max_block_decisions_per_run",
			"max_critical_risk_observations_per_run",
		} {
			fields = append(fields, struct {
				name  string
				value *yaml.Node
			}{"gate." + name, mappingValue(gate, name)})
		}
	}
	for _, field := range fields {
		node := field.value
		if node == nil || node.ShortTag() == "!!null" {
			continue
		}
		if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!int" {
			return scenarioError("%s must be a whole number, got %q", field.name, node.Value)
		}
	}
	return nil
}

// resolved follows a document or alias node to the node it stands for.
func resolved(node *yaml.Node) *yaml.Node {
	for node != nil {
		switch {
		case node.Kind == yaml.AliasNode:
			node = node.Alias
		case node.Kind == yaml.DocumentNode && len(node.Content) == 1:
			node = node.Content[0]
		default:
			return node
		}
	}
	return nil
}

// mappingValue returns the value under key when node is a mapping, or nil.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	node = resolved(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return resolved(node.Content[i+1])
		}
	}
	return nil
}
