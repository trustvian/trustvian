package httpapi_test

// Task 086's provenance on /v1/scenario-executions.

import (
	"encoding/json"
	"strings"
	"testing"
)

func sha(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }

// provenanceOf extracts execution.provenance from a begin or get response.
func provenanceOf(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Execution struct {
			Provenance json.RawMessage `json:"provenance"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return string(envelope.Execution.Provenance)
}

// TestScenarioExecutionProvenanceOverHTTP: what the begin request recorded is
// what the begin response and a later read return, field for field, and an
// execution begun without provenance says nothing was recorded.
func TestScenarioExecutionProvenanceOverHTTP(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()
	body := beginBody("e1", 1, nil)
	body["provenance"] = map[string]any{
		"scenario_digest": sha("a"), "inputs_declared": true, "input_digest": sha("b"),
		"reference": map[string]any{"model": "llama3.2"},
		"candidate": map[string]any{"model": "gemma3:4b",
			"prompt_ref": map[string]any{"name": "support/system@v14", "digest": sha("c")}},
	}
	r := a.do("POST", "/v1/scenario-executions", body)
	a.mustStatus(r, 201, "begin with provenance")
	want := `{"reference":{"scenario_digest":"` + sha("a") + `","inputs":"declared","input_digest":"` + sha("b") +
		`","model":"llama3.2"},"candidate":{"scenario_digest":"` + sha("a") + `","inputs":"declared","input_digest":"` +
		sha("b") + `","model":"gemma3:4b","prompt_ref":{"name":"support/system@v14","digest":"` + sha("c") + `"}}}`
	if got := provenanceOf(t, r.Body.Bytes()); got != want {
		t.Fatalf("begin provenance =\n%s\nwant\n%s", got, want)
	}
	read := a.do("GET", "/v1/scenario-executions/e1", nil)
	a.mustStatus(read, 200, "get")
	if got := provenanceOf(t, read.Body.Bytes()); got != want {
		t.Fatalf("read provenance =\n%s\nwant\n%s", got, want)
	}

	// No inputs declared is said as such, not as a missing digest.
	body = beginBody("e2", 1, nil)
	body["provenance"] = map[string]any{"scenario_digest": sha("a")}
	r = a.do("POST", "/v1/scenario-executions", body)
	a.mustStatus(r, 201, "begin without inputs")
	if got, want := provenanceOf(t, r.Body.Bytes()),
		`{"reference":{"scenario_digest":"`+sha("a")+`","inputs":"not_declared"},`+
			`"candidate":{"scenario_digest":"`+sha("a")+`","inputs":"not_declared"}}`; got != want {
		t.Fatalf("provenance = %s, want %s", got, want)
	}

	// A client that predates task 086 records nothing.
	if got, want := provenanceOf(t, a.beginExecution("e3", 1, nil, 201)),
		`{"reference":{"inputs":"not_recorded"},"candidate":{"inputs":"not_recorded"}}`; got != want {
		t.Fatalf("provenance = %s, want %s", got, want)
	}
}

func TestScenarioExecutionProvenanceIsValidated(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()
	for name, provenance := range map[string]map[string]any{
		"a malformed digest":      {"scenario_digest": "sha256:abc"},
		"a digest without a flag": {"scenario_digest": sha("a"), "input_digest": sha("b")},
		"prompt text as a model":  {"candidate": map[string]any{"model": "Be concise and polite."}},
		"a prompt text field": {"candidate": map[string]any{
			"prompt_ref": map[string]any{"name": "sys", "digest": sha("c"), "text": "Be concise."}}},
		"an unknown field": {"prompt": "Be concise."},
	} {
		t.Run(name, func(t *testing.T) {
			body := beginBody("refused", 1, nil)
			body["provenance"] = provenance
			r := a.do("POST", "/v1/scenario-executions", body)
			a.mustStatus(r, 400, name)
		})
	}
}
