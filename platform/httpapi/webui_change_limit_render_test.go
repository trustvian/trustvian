package httpapi_test

// The browser's rendering of the optional counted-change check is the
// server's result, cell for cell (issue 131).
//
// Runs the shipped changeCountCells under node over real /v1 responses: a
// check not evaluated, one evaluated and passed, one evaluated and failed.
// A not_recorded check cannot be produced by a comparison — only a promotion
// stored before schema 8 carries it, which the persistence suites cover — so
// its rendering is asserted over the published wire shape.

import (
	"encoding/json"
	"slices"
	"testing"
)

const changeCellsDriver = `
import { changeCountCells } from "./v1/render.js";
const payloads = JSON.parse(process.argv[2]);
process.stdout.write(JSON.stringify(payloads.map((p) => changeCountCells(p))));
`

func TestBrowserRendersTheChangeCheckTheServerReturned(t *testing.T) {
	a := newAPI(t)
	a.oneActRuns()

	checks := make([]json.RawMessage, 0, 4)
	for _, limits := range []map[string]any{
		limitsWithChanges(nil, false), // not evaluated
		limitsWithChanges("1", true),  // evaluated, passed
		limitsWithChanges("0", true),  // evaluated, failed
	} {
		r := a.do("POST", "/v1/evaluations/compare", map[string]any{
			"reference_run_id": "run-ref", "candidate_run_id": "run-can", "gate_limits": limits,
		})
		a.mustStatus(r, 200, "compare")
		var body struct {
			Gate struct {
				AddedBehaviorChanges json.RawMessage `json:"added_behavior_changes"`
			} `json:"gate"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		checks = append(checks, body.Gate.AddedBehaviorChanges)
	}
	checks = append(checks, json.RawMessage(`{"state":"not_recorded"}`))
	arg, err := json.Marshal(checks)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var cells [][]string
	if err := json.Unmarshal(runShippedDriver(t, changeCellsDriver, string(arg)), &cells); err != nil {
		t.Fatalf("decode rendered cells: %v", err)
	}
	want := [][]string{
		{"—", "not set", "not evaluated"},
		{"1", "1", "pass"},
		{"1", "0", "fail"},
		{"—", "not set", "not recorded"},
	}
	if len(cells) != len(want) {
		t.Fatalf("rendered %d rows, want %d: %v", len(cells), len(want), cells)
	}
	for i := range want {
		if !slices.Equal(cells[i], want[i]) {
			t.Errorf("row %d = %q, want %q (from %s)", i, cells[i], want[i], checks[i])
		}
	}
}
