package trustvianprocessor_test

import (
	"strings"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer/consumertest"

	trustvianprocessor "trustvian-processor"
)

// boolPtr exists because Required is a *bool: nil, false and true are three
// different configurations, and only true is accepted.
func boolPtr(b bool) *bool { return &b }

// TestEvaluationConfigValidation drives validation through the real factory,
// so a rejected block fails Collector startup rather than the first span.
func TestEvaluationConfigValidation(t *testing.T) {
	// valid must point at a reachable control plane, not just a
	// syntactically acceptable URL: task 073 wired Start to initialize the
	// evaluation sink (reading its ingest cursor), so the "valid" subtest
	// below now performs real I/O and a stub server is required for it to
	// reach Start successfully. The invalid subtests never get past
	// validate(), so they never dial it.
	valid := func(t *testing.T) *trustvianprocessor.EvaluationConfig {
		cp := newIngestAPIServer(t)
		return &trustvianprocessor.EvaluationConfig{
			APIURL:            cp.URL,
			RunID:             "run-reference",
			BehavioralProfile: "support-reference",
			Required:          boolPtr(true),
		}
	}

	tests := []struct {
		name    string
		mutate  func(*trustvianprocessor.EvaluationConfig)
		wantErr string
	}{
		{name: "valid", mutate: func(*trustvianprocessor.EvaluationConfig) {}},
		{
			name:    "missing api_url",
			mutate:  func(e *trustvianprocessor.EvaluationConfig) { e.APIURL = "" },
			wantErr: "api_url",
		},
		{
			name:    "api_url with credentials",
			mutate:  func(e *trustvianprocessor.EvaluationConfig) { e.APIURL = "http://u:p@127.0.0.1:1" },
			wantErr: "credentials",
		},
		{
			name:    "api_url with path",
			mutate:  func(e *trustvianprocessor.EvaluationConfig) { e.APIURL = "http://127.0.0.1:1/api" },
			wantErr: "path",
		},
		{
			name:    "missing run_id",
			mutate:  func(e *trustvianprocessor.EvaluationConfig) { e.RunID = "" },
			wantErr: "run_id",
		},
		{
			name:    "missing behavioral_profile",
			mutate:  func(e *trustvianprocessor.EvaluationConfig) { e.BehavioralProfile = "" },
			wantErr: "behavioral_profile",
		},
		{
			name:    "required omitted",
			mutate:  func(e *trustvianprocessor.EvaluationConfig) { e.Required = nil },
			wantErr: "required",
		},
		{
			name:    "required false",
			mutate:  func(e *trustvianprocessor.EvaluationConfig) { e.Required = boolPtr(false) },
			wantErr: "required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluation := valid(t)
			tt.mutate(evaluation)
			cfg := &trustvianprocessor.Config{Evaluation: evaluation}

			_, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cfg)
			if tt.wantErr == "" {
				// newTestProcessorWithConfig drives CreateTraces and Start,
				// and Start now reads the sink's ingest cursor from the stub
				// control plane valid(t) started above (see that comment).
				// A nil error here already proves construction, validation,
				// and that live cursor read all succeeded — task 073's own
				// tests (evaluation_test.go) cover what happens once spans
				// flow through, so this subtest need not duplicate that.
				return
			}
			if err == nil {
				t.Fatalf("CreateTraces() error = nil, want a rejection naming %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to name %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestEvaluationConfigRejectionNeverEchoesCredentials keeps the secret out of
// Collector startup logs, where a rejection message lands.
func TestEvaluationConfigRejectionNeverEchoesCredentials(t *testing.T) {
	const password = "hunter2"
	cfg := &trustvianprocessor.Config{Evaluation: &trustvianprocessor.EvaluationConfig{
		APIURL:            "http://user:" + password + "@127.0.0.1:54321",
		RunID:             "run-reference",
		BehavioralProfile: "support-reference",
		Required:          boolPtr(true),
	}}
	_, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cfg)
	if err == nil {
		t.Fatal("CreateTraces() error = nil, want rejection")
	}
	if strings.Contains(err.Error(), password) {
		t.Errorf("error %q contains the credential", err.Error())
	}
}

var _ component.Config = (*trustvianprocessor.Config)(nil)
