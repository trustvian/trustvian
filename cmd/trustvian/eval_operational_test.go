package main

// trustvian eval operational, task 087: the run lists reach the route as
// repeated query parameters, and both output modes print the server's body.

import (
	"net/url"
	"slices"
	"testing"
)

const operationalBody = `{"version":"1","reference_run_ids":["ref-1","ref-2"],` +
	`"candidate_run_ids":["can-1","can-2"],"targets":[],"extra":"kept"}`

func TestEvalOperationalForwardsRunsAndPrintsTheBody(t *testing.T) {
	for _, mode := range [][]string{nil, {"--json"}} {
		api := newFakeAPI(t)
		api.reply(200, operationalBody)
		args := append([]string{"eval", "operational",
			"--reference-run", "ref-1", "--reference-run", "ref-2",
			"--candidate-run", "can-1", "--candidate-run", "can-2",
			"--after", "external/api.example", "--limit", "5", "--api-url", api.url()}, mode...)
		result := runPlatformCLI(t, args...)
		result.mustExit(t, exitOK, "eval operational")

		request := api.only()
		if request.method != "GET" || request.escapedPath != "/v1/evaluations/operational" {
			t.Fatalf("request = %s %s", request.method, request.escapedPath)
		}
		query, err := url.ParseQuery(request.rawQuery)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(query["reference_run_id"], []string{"ref-1", "ref-2"}) ||
			!slices.Equal(query["candidate_run_id"], []string{"can-1", "can-2"}) ||
			query.Get("after") != "external/api.example" || query.Get("limit") != "5" || len(query) != 4 {
			t.Fatalf("query = %v", query)
		}
		if result.stdout != operationalBody+"\n" {
			t.Fatalf("stdout %q is not the server's body", result.stdout)
		}
	}
}

func TestEvalOperationalRequiresBothSides(t *testing.T) {
	result := runPlatformCLI(t, "eval", "operational", "--reference-run", "ref-1", "--api-url", "http://127.0.0.1:1")
	result.mustExit(t, exitUsage, "missing candidate")
}
