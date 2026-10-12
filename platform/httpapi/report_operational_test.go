package httpapi_test

import (
	"encoding/json"
	"net/url"
	"reflect"
	"testing"

	"github.com/trustvian/trustvian/event"
)

// D16: each target of a report carries the latency and error sections GET
// /v1/evaluations/operational states for the same runs, member for member.
func TestReportTargetsCarryTheOperationalRoutesSections(t *testing.T) {
	a := newAPI(t)
	model := func(status, duration string) reportStep {
		return reportStep{op: "chat", category: event.OperationCategoryExternal, target: "api.model.example",
			duration: duration, status: event.StatusOK, envelope: map[string]string{"http_status_code": status}}
	}
	kb := reportStep{op: "search", target: "kb.internal", duration: "3000000", status: event.StatusOK,
		envelope: map[string]string{"http_status_code": "200"}}
	limits := repeatedLimitsBody("2", "0")
	reference, candidate := a.recordExecution(reportFixture{
		id:        "e-ops",
		reference: [][]reportStep{{model("200", "40000000"), kb}, {model("200", "45000000"), kb}},
		candidate: [][]reportStep{{model("429", "900000000"), model("200", "60000000"), kb}, {model("200", "70000000")}},
		limits:    limits,
	})

	var report struct {
		Comparison struct {
			Targets []struct {
				TargetName     string          `json:"target_name"`
				TargetCategory string          `json:"target_category"`
				Operational    json.RawMessage `json:"operational"`
			} `json:"targets"`
		} `json:"comparison"`
	}
	if err := json.Unmarshal(a.report("e-ops"), &report); err != nil {
		t.Fatal(err)
	}
	route := a.do("GET", "/v1/evaluations/operational?"+url.Values{
		"reference_run_id": reference, "candidate_run_id": candidate}.Encode(), nil)
	a.mustStatus(route, 200, "operational")
	var rows struct {
		Targets []struct {
			TargetName     string          `json:"target_name"`
			TargetCategory string          `json:"target_category"`
			Latency        json.RawMessage `json:"latency"`
			Errors         json.RawMessage `json:"errors"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(route.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(report.Comparison.Targets) != 2 || len(rows.Targets) != 2 {
		t.Fatalf("report targets %d, route targets %d; want 2 and 2", len(report.Comparison.Targets), len(rows.Targets))
	}
	byTarget := map[string]any{}
	for _, r := range rows.Targets {
		byTarget[r.TargetCategory+"/"+r.TargetName] = decodeAny(t, mustJSON(t, map[string]json.RawMessage{
			"latency": r.Latency, "errors": r.Errors}))
	}
	for _, target := range report.Comparison.Targets {
		want, ok := byTarget[target.TargetCategory+"/"+target.TargetName]
		if !ok {
			t.Fatalf("report target %s is not on the route", target.TargetName)
		}
		if got := decodeAny(t, target.Operational); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: report %v\nroute %v", target.TargetName, got, want)
		}
	}
	model429 := decodeAny(t, report.Comparison.Targets[0].Operational)
	errors := model429["errors"].(map[string]any)
	if errors["candidate"].(map[string]any)["http_429"] != "1" ||
		errors["candidate"].(map[string]any)["runs_with_evidence"] != "2" {
		t.Errorf("the model target's candidate errors = %v", errors["candidate"])
	}
}
