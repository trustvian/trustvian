package httpapi_test

// Task 087's cost section over /v1: present only with a pricing table, always
// with its provenance, and its absence changes nothing else byte for byte.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"

	platform "trustvian-platform"
	"trustvian-platform/httpapi"
)

// pricedHandler is a second handler over the same store, whose control plane
// was given a pricing table.
func (a *api) pricedHandler(models map[string]platform.ModelPrice) http.Handler {
	a.t.Helper()
	pricing, err := platform.NewPricing("team-2026-10", "test fixture", "USD", "sha256:"+strings.Repeat("ab", 32), models)
	if err != nil {
		a.t.Fatal(err)
	}
	plane, err := platform.NewControlPlane(a.store, a.store, a.store, platform.WithPricing(pricing))
	if err != nil {
		a.t.Fatal(err)
	}
	handler, err := httpapi.NewHandler(plane, httpapi.WithClock(func() time.Time { return testEpoch }))
	if err != nil {
		a.t.Fatal(err)
	}
	return handler
}

func post(t *testing.T, handler http.Handler, path string, body any) []byte {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST %s = %d %s", path, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}

// withoutField removes `,"<name>":{…}` — one JSON object member — from a
// compact document, by brace depth.
func withoutField(t *testing.T, doc []byte, name string) []byte {
	t.Helper()
	marker := []byte(`,"` + name + `":{`)
	start := bytes.Index(doc, marker)
	if start < 0 {
		t.Fatalf("%s not found in %s", name, doc)
	}
	depth := 0
	for i := start + len(marker) - 1; i < len(doc); i++ {
		switch doc[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return append(append([]byte{}, doc[:start]...), doc[i+1:]...)
			}
		}
	}
	t.Fatalf("unterminated %s", name)
	return nil
}

func TestCostIsOptionalAndCarriesItsProvenance(t *testing.T) {
	a := newAPI(t)
	usage := func(in, out string) map[string]string {
		return map[string]string{"tokens_input": in, "tokens_output": out}
	}
	// "chat" is priced and "summarize" is not, so summarize's tokens are
	// unpriced. The candidate's unsplit total is unpriced too, although it
	// belongs to a priced model: no input or output rate applies to a total.
	a.completeOperationalRun("run-ref", "cand-1", []operationalStep{
		{op: "chat", duration: "1000", status: event.StatusOK, fields: usage("1000000", "200000")},
		{op: "summarize", duration: "1000", status: event.StatusOK, fields: usage("10", "5")},
	})
	a.completeOperationalRun("run-can", "cand-2", []operationalStep{
		{op: "chat", duration: "1000", status: event.StatusOK, fields: usage("1500001", "200000")},
		{op: "chat", duration: "1000", status: event.StatusOK, fields: map[string]string{"tokens_unsplit": "7"}},
	})
	compare := map[string]any{
		"reference_run_id": "run-ref", "candidate_run_id": "run-can",
		"gate_limits": limitsBody("10", "10", "10"),
	}
	plain := post(t, a.server, "/v1/evaluations/compare", compare)
	priced := post(t, a.pricedHandler(map[string]platform.ModelPrice{
		// $2.50 and $10.00 per million tokens, in micro-dollars.
		"chat": {InputMicrosPerMillion: 2_500_000, OutputMicrosPerMillion: 10_000_000},
	}), "/v1/evaluations/compare", compare)

	if bytes.Contains(plain, []byte(`"cost"`)) {
		t.Fatalf("a control plane without pricing published a cost: %s", plain)
	}
	if got := withoutField(t, priced, "cost"); !bytes.Equal(got, plain) {
		t.Fatalf("pricing changed something other than the cost section:\n priced %s\n  plain %s", got, plain)
	}

	var body struct {
		Scorecard struct {
			Cost json.RawMessage `json:"cost"`
		} `json:"scorecard"`
	}
	if err := json.Unmarshal(priced, &body); err != nil {
		t.Fatal(err)
	}
	// Reference: 1 000 000 × 2.5 + 200 000 × 10 = 4 500 000 micro-dollars.
	// Candidate: (1 500 001 × 2 500 000 + 200 000 × 10 000 000) / 1e6 =
	// 5 750 002.5, rounded down once at the model to 5 750 002.
	want := `{"pricing_version":"team-2026-10","pricing_digest":"sha256:` + strings.Repeat("ab", 32) +
		`","source":"test fixture","currency":"USD","comparable":true,` +
		`"reference":{"runs_with_evidence":"1","cost_micros":"4500000","priced_tokens":"1200000","unpriced_tokens":"15",` +
		`"token_observations":"2","observations_without_tokens":"0"},` +
		`` +
		`"candidate":{"runs_with_evidence":"1","cost_micros":"5750002","priced_tokens":"1700001","unpriced_tokens":"7",` +
		`"token_observations":"2","observations_without_tokens":"0"},` +
		`` +
		`"delta":{"cost_micros":"1250002"}}`
	if string(body.Scorecard.Cost) != want {
		t.Fatalf("cost\n got %s\nwant %s", body.Scorecard.Cost, want)
	}
}

// TestCostOfARunWithoutTokensIsUnavailable: a side that reported no tokens has
// no cost — not a zero one — and the section carries no delta.
func TestCostOfARunWithoutTokensIsUnavailable(t *testing.T) {
	a := newAPI(t)
	a.completeOperationalRun("run-ref", "cand-1", []operationalStep{
		{op: "chat", duration: "1000", status: event.StatusOK,
			fields: map[string]string{"tokens_input": "10", "tokens_output": "1"}}})
	a.completeOperationalRun("run-can", "cand-2", []operationalStep{
		{op: "chat", duration: "1000", status: event.StatusOK}})
	priced := post(t, a.pricedHandler(map[string]platform.ModelPrice{"chat": {InputMicrosPerMillion: 1, OutputMicrosPerMillion: 1}}),
		"/v1/evaluations/compare", map[string]any{
			"reference_run_id": "run-ref", "candidate_run_id": "run-can",
			"gate_limits": limitsBody("10", "10", "10"),
		})
	var body struct {
		Scorecard struct {
			Cost map[string]json.RawMessage `json:"cost"`
		} `json:"scorecard"`
	}
	if err := json.Unmarshal(priced, &body); err != nil {
		t.Fatal(err)
	}
	cost := body.Scorecard.Cost
	if string(cost["comparable"]) != "false" || string(cost["reason"]) != `"candidate_unavailable"` {
		t.Fatalf("cost = %s", priced)
	}
	if _, has := cost["delta"]; has {
		t.Fatal("an unavailable cost carries a delta")
	}
	if strings.Contains(string(cost["candidate"]), "cost_micros") {
		t.Fatalf("a side with no tokens states a cost: %s", cost["candidate"])
	}
}

// TestRepeatedCostIsOptional: the same byte-for-byte property on the repeated
// comparison.
func TestRepeatedCostIsOptional(t *testing.T) {
	a := newAPI(t)
	for _, id := range []string{"ref-1", "can-1"} {
		a.completeIsolatedRun(id, []string{"read"})
	}
	compare := map[string]any{
		"reference_run_ids": []string{"ref-1"}, "candidate_run_ids": []string{"can-1"},
		"gate_limits": repeatedLimitsBody("1", "0"),
	}
	plain := post(t, a.server, "/v1/evaluations/compare-repeated", compare)
	priced := post(t, a.pricedHandler(map[string]platform.ModelPrice{"read": {InputMicrosPerMillion: 1, OutputMicrosPerMillion: 1}}),
		"/v1/evaluations/compare-repeated", compare)
	if bytes.Contains(plain, []byte(`"cost"`)) {
		t.Fatal("a control plane without pricing published a cost")
	}
	if got := withoutField(t, priced, "cost"); !bytes.Equal(got, plain) {
		t.Fatalf("pricing changed something other than the cost section:\n priced %s\n  plain %s", got, plain)
	}
}
