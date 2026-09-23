package trustvianprocessor_test

import (
	"testing"

	"go.opentelemetry.io/collector/confmap"

	trustvianprocessor "trustvian-processor"
)

// TestConfigUnmarshalDecodesPolicyBlock proves Collector's own confmap
// decoder (mapstructure, case-sensitive tag matching, ErrorUnused) can
// actually decode a real `policy:` block into Config.Policy — the
// generic map handed to decodePolicy — using the exact schema
// (snake_case field names) config.PolicyConfig's own yaml tags define.
// This is the specific mechanism task 022 exists to prove works:
// Collector's decoder never sees config.PolicyConfig's yaml tags
// directly (it only reads mapstructure tags), so this test exercises
// the real boundary, not an assumption about it.
func TestConfigUnmarshalDecodesPolicyBlock(t *testing.T) {
	conf := confmap.NewFromStringMap(map[string]any{
		"policy": map[string]any{
			"version":          "v1",
			"default_decision": "observe_only",
			"default_reason":   "no rule matched",
			"rules": []any{
				map[string]any{
					"name": "block-critical",
					"when": map[string]any{
						"min_risk_level": "critical",
					},
					"decision": "block",
					"reason":   "critical risk is blocked",
				},
			},
		},
	})

	var cfg trustvianprocessor.Config
	if err := conf.Unmarshal(&cfg); err != nil {
		t.Fatalf("conf.Unmarshal() error = %v", err)
	}
	if cfg.Policy == nil {
		t.Fatal("cfg.Policy = nil, want the decoded policy block")
	}
	if got := cfg.Policy["version"]; got != "v1" {
		t.Errorf("cfg.Policy[%q] = %v, want %q", "version", got, "v1")
	}
}

// TestConfigUnmarshalOmittedPolicyLeavesNilMap proves a Collector
// config with no `policy:` key at all leaves Config.Policy nil, not
// an empty-but-present map — the distinction newTrustvianProcessor
// relies on to tell "no policy configured" (preserve default
// behavior) apart from "an explicit, if empty, policy block" (must be
// validated, and will fail validation).
func TestConfigUnmarshalOmittedPolicyLeavesNilMap(t *testing.T) {
	conf := confmap.NewFromStringMap(map[string]any{})

	var cfg trustvianprocessor.Config
	if err := conf.Unmarshal(&cfg); err != nil {
		t.Fatalf("conf.Unmarshal() error = %v", err)
	}
	if cfg.Policy != nil {
		t.Errorf("cfg.Policy = %v, want nil when policy: is omitted entirely", cfg.Policy)
	}
}

// TestConfigUnmarshalEmptyPolicyBlockIsNonNil proves the opposite
// case: `policy: {}`, present but empty, decodes to a non-nil, empty
// map — treated as an explicit (and, per config.PolicyConfig.Validate,
// invalid — it's missing default_decision/default_reason) policy
// request, not silently identical to omitting the key.
func TestConfigUnmarshalEmptyPolicyBlockIsNonNil(t *testing.T) {
	conf := confmap.NewFromStringMap(map[string]any{
		"policy": map[string]any{},
	})

	var cfg trustvianprocessor.Config
	if err := conf.Unmarshal(&cfg); err != nil {
		t.Fatalf("conf.Unmarshal() error = %v", err)
	}
	if cfg.Policy == nil {
		t.Error("cfg.Policy = nil, want a non-nil empty map for an explicit but empty policy: block")
	}
}

// TestConfigUnmarshalDecodesEvaluationBlock proves Collector's own confmap
// decoder reads the evaluation block's mapstructure tags. Unlike policy: and
// storage:, this block decodes directly into a typed struct — there is no
// canonical Trustvian config type to defer to, because which evaluation run
// a Collector feeds is a property of this runtime rather than of the engine.
func TestConfigUnmarshalDecodesEvaluationBlock(t *testing.T) {
	conf := confmap.NewFromStringMap(map[string]any{
		"evaluation": map[string]any{
			"api_url":            "http://127.0.0.1:54321",
			"run_id":             "run-reference",
			"behavioral_profile": "support-reference",
			"required":           true,
		},
	})

	var cfg trustvianprocessor.Config
	if err := conf.Unmarshal(&cfg); err != nil {
		t.Fatalf("conf.Unmarshal() error = %v", err)
	}
	if cfg.Evaluation == nil {
		t.Fatal("cfg.Evaluation = nil, want the decoded evaluation block")
	}
	if cfg.Evaluation.APIURL != "http://127.0.0.1:54321" {
		t.Errorf("APIURL = %q", cfg.Evaluation.APIURL)
	}
	if cfg.Evaluation.RunID != "run-reference" {
		t.Errorf("RunID = %q", cfg.Evaluation.RunID)
	}
	if cfg.Evaluation.BehavioralProfile != "support-reference" {
		t.Errorf("BehavioralProfile = %q", cfg.Evaluation.BehavioralProfile)
	}
	if cfg.Evaluation.Required == nil || !*cfg.Evaluation.Required {
		t.Errorf("Required = %v, want true", cfg.Evaluation.Required)
	}
}

// TestConfigUnmarshalOmittedEvaluationLeavesNil is the property every
// existing deployment depends on: no evaluation block means no behavior
// change anywhere.
func TestConfigUnmarshalOmittedEvaluationLeavesNil(t *testing.T) {
	conf := confmap.NewFromStringMap(map[string]any{})

	var cfg trustvianprocessor.Config
	if err := conf.Unmarshal(&cfg); err != nil {
		t.Fatalf("conf.Unmarshal() error = %v", err)
	}
	if cfg.Evaluation != nil {
		t.Errorf("cfg.Evaluation = %+v, want nil when evaluation: is omitted", cfg.Evaluation)
	}
}
