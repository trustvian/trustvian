package httpapi_test

// The report route at the repetition bound: one GET of an execution of
// MaxRepetitions runs per side (task 107's PERFORMANCE figure).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	platform "trustvian-platform"
	"trustvian-platform/httpapi"
)

func benchRequest(b *testing.B, handler http.Handler, method, path string, body any, want int) []byte {
	b.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			b.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, reader)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		b.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body.Bytes())
	}
	return w.Body.Bytes()
}

// BenchmarkScenarioExecutionReportAtTheBound: 64 repetitions per side, each
// run observing 16 behaviors twice (the candidate side two more), every
// optional limit set.
func BenchmarkScenarioExecutionReportAtTheBound(b *testing.B) {
	const behaviors = 16
	store, err := platform.OpenSQLiteStore(b.Context(), filepath.Join(b.TempDir(), "platform.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		b.Fatal(err)
	}
	handler, err := httpapi.NewHandler(plane, httpapi.WithClock(func() time.Time { return testEpoch }))
	if err != nil {
		b.Fatal(err)
	}
	do := func(method, path string, body any, want int) []byte {
		return benchRequest(b, handler, method, path, body, want)
	}
	do("POST", "/v1/projects", map[string]string{"id": "proj-1", "name": "Checkout"}, 201)
	do("POST", "/v1/agents", map[string]string{"id": "agent-1", "project_id": "proj-1", "name": "Agent"}, 201)
	do("POST", "/v1/candidates", map[string]any{"id": "cand-1", "agent_id": "agent-1"}, 201)
	do("POST", "/v1/environments", map[string]any{"project_id": "proj-1", "ref": testEnvironment, "name": "Staging"}, 201)
	do("POST", "/v1/scenario-executions", beginBody("e-bound", platform.MaxRepetitions, nil), 201)

	body := map[string]any{"gate_limits": reportLimits()}
	for _, side := range []string{"reference", "candidate"} {
		var ids []string
		for i := range platform.MaxRepetitions {
			run := fmt.Sprintf("e-bound-%s-%d", side, i+1)
			profile := run + "-profile"
			do("POST", "/v1/evaluation-runs", map[string]string{"id": run, "candidate_id": "cand-1",
				"environment": testEnvironment, "behavioral_profile": profile}, 201)
			do("POST", "/v1/evaluation-runs/"+run+"/start", nil, 200)
			operations := behaviors
			if side == "candidate" {
				operations += 2
			}
			sequence := uint64(0)
			for op := range operations {
				for repeat := range 2 {
					sequence++
					record := apiRecord(fmt.Sprintf("%s-e%d-%d", run, op, repeat), fmt.Sprintf("fp-%d", op),
						fmt.Sprintf("op-%d", op))
					envelope := envelope(sequence, record)
					envelope["behavioral_profile"] = profile
					do("POST", "/v1/evaluation-runs/"+run+"/records", envelope, 200)
				}
			}
			do("POST", "/v1/evaluation-runs/"+run+"/complete", nil, 200)
			ids = append(ids, run)
		}
		body[side+"_run_ids"] = ids
	}
	do("POST", "/v1/scenario-executions/e-bound/complete", body, 200)

	// The report, and for scale the compare-repeated call over the same
	// runs, which the report's reduction is.
	b.Run("report", func(b *testing.B) {
		b.ReportAllocs()
		var doc []byte
		for b.Loop() {
			doc = benchRequest(b, handler, "GET", "/v1/scenario-executions/e-bound/report", nil, 200)
		}
		b.ReportMetric(float64(len(doc)), "bytes/doc")
		// The at-bound document, for rendering it with trustvian-ci-render
		// --document (docs/PERFORMANCE.md, task 107-B).
		if out := os.Getenv("TRUSTVIAN_BENCH_REPORT_OUT"); out != "" {
			if err := os.WriteFile(out, doc, 0o644); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("compare-repeated", func(b *testing.B) {
		b.ReportAllocs()
		var size int
		for b.Loop() {
			size = len(benchRequest(b, handler, "POST", "/v1/evaluations/compare-repeated", body, 200))
		}
		b.ReportMetric(float64(size), "bytes/doc")
	})
}
