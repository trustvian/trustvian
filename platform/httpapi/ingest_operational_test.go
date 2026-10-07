package httpapi_test

// Task 087's envelope fields at the ingest boundary.

import (
	"maps"
	"testing"
)

// TestIngestRefusesMalformedOperationalFields: a field the adapter could not
// read is omitted, so a present but malformed one is a client defect.
func TestIngestRefusesMalformedOperationalFields(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]any
	}{
		{"non-canonical count", map[string]any{"tokens_input": "012"}},
		{"empty string", map[string]any{"tokens_output": ""}},
		{"negative", map[string]any{"tokens_input": "-1"}},
		{"a JSON number", map[string]any{"tokens_input": 12}},
		{"status below range", map[string]any{"http_status_code": "99"}},
		{"status above range", map[string]any{"http_status_code": "600"}},
		{"total beside a part", map[string]any{"tokens_input": "1", "tokens_unsplit": "3"}},
		{"past the per-observation maximum", map[string]any{"tokens_output": "4294967297"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAPI(t)
			a.seedRunning("run-1")
			body := envelope(1, apiRecord("evt-1", "fp-read", "read"))
			maps.Copy(body, tt.fields)
			a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records", body), 400, tt.name)
		})
	}
}
