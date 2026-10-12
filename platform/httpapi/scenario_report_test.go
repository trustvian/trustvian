package httpapi_test

// GET /v1/scenario-executions/{id}/report (task 107, D10 and D11): result
// document version 2 over HTTP.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	platform "trustvian-platform"
	"trustvian-platform/httpapi"
)

// reportLimits is every limit, the optional ones included, so the recorded
// gate holds evaluated, deferred and per-target checks.
func reportLimits() map[string]any {
	limits := repeatedLimitsBody("2", "0")
	limits["min_candidate_frequency"] = "1"
	limits["max_lost_behaviors"] = "0"
	limits["max_calls_per_run"] = []map[string]string{{"target": "api.example", "max": "5"}}
	limits["max_llm_calls_per_run"] = "3"
	return limits
}

// completeReportExecution runs a self-contained execution of two repetitions
// per side — read on both sides, export added on the candidate side, lookup
// lost from one candidate repetition — and returns the completion's body.
// reportCLIVersion is the version the fixture's runner records at begin.
const reportCLIVersion = "v0.13.0 3e86f9c"

func (a *api) completeReportExecution(id string) []byte {
	a.t.Helper()
	a.seedHierarchy()
	begin := beginBody(id, 2, nil)
	begin["cli_version"] = reportCLIVersion
	a.mustStatus(a.do("POST", "/v1/scenario-executions", begin), 201, "begin "+id)
	sides := map[string][][]string{
		"reference": {{"read", "lookup"}, {"read", "lookup"}},
		"candidate": {{"read", "export", "lookup"}, {"read", "export"}},
	}
	body := map[string]any{"gate_limits": reportLimits()}
	for _, side := range []string{"reference", "candidate"} {
		var ids []string
		for i, ops := range sides[side] {
			run := fmt.Sprintf("%s-%s-%d", id, side, i+1)
			a.completeIsolatedRun(run, ops)
			ids = append(ids, run)
		}
		body[side+"_run_ids"] = ids
	}
	r := a.do("POST", "/v1/scenario-executions/"+id+"/complete", body)
	a.mustStatus(r, 200, "complete "+id)
	return r.Body.Bytes()
}

func (a *api) report(id string) []byte {
	a.t.Helper()
	r := a.do("GET", "/v1/scenario-executions/"+id+"/report", nil)
	a.mustStatus(r, 200, "report "+id)
	return r.Body.Bytes()
}

func decodeAny(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

// requireSubset fails unless every member of want is in got with an equal
// value: objects member by member, arrays element by element and of equal
// length. got may carry members want lacks — version 2's additions.
func requireSubset(t *testing.T, path string, want, got any) {
	t.Helper()
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			t.Fatalf("%s: %T, want an object", path, got)
		}
		for k, v := range w {
			gv, present := g[k]
			if !present {
				t.Fatalf("%s.%s: missing from version 2", path, k)
			}
			requireSubset(t, path+"."+k, v, gv)
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			t.Fatalf("%s: %v, want %d elements", path, got, len(w))
		}
		for i := range w {
			requireSubset(t, fmt.Sprintf("%s[%d]", path, i), w[i], g[i])
		}
	default:
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("%s: version 2 has %v, version 1 %v", path, got, want)
		}
	}
}

// TestReportCarriesVersionOneUnchanged: the version 1 subset of the report
// equals what `eval run --json` writes for the same execution — the
// completion's comparison, verbatim, and every other member, the CLI version
// included now that the execution records it (D14). Only the version differs.
func TestReportCarriesVersionOneUnchanged(t *testing.T) {
	a := newAPI(t)
	var completion struct {
		Execution  json.RawMessage `json:"execution"`
		Comparison json.RawMessage `json:"comparison"`
	}
	if err := json.Unmarshal(a.completeReportExecution("e1"), &completion); err != nil {
		t.Fatal(err)
	}
	var execution struct {
		Provenance json.RawMessage `json:"provenance"`
	}
	if err := json.Unmarshal(a.do("GET", "/v1/scenario-executions/e1", nil).Body.Bytes(), &struct {
		Execution any `json:"execution"`
	}{&execution}); err != nil {
		t.Fatal(err)
	}
	// What eval run builds around the comparison (cmd/trustvian/eval_run.go).
	var comparison struct {
		Producer struct {
			Version string `json:"control_plane_version"`
		} `json:"producer"`
	}
	if err := json.Unmarshal(completion.Comparison, &comparison); err != nil {
		t.Fatal(err)
	}
	v1 := decodeAny(t, mustJSON(t, map[string]any{
		"version": "1", "scenario": map[string]any{"name": "support", "runs": 2},
		"execution_id": "e1",
		"producers": map[string]any{
			"cli_version": reportCLIVersion, "control_plane_version": comparison.Producer.Version,
		},
		"provenance": execution.Provenance, "comparison": completion.Comparison,
	}))
	report := decodeAny(t, a.report("e1"))
	if report["version"] != "2" {
		t.Fatalf("version = %v", report["version"])
	}
	delete(v1, "version")
	requireSubset(t, "$", v1, report)

	if _, ok := report["reference"]; ok {
		t.Fatal("a self-contained execution's report names a reference")
	}
	gate := report["comparison"].(map[string]any)["gate"].(map[string]any)
	if gate["state"] != "recorded" || gate["verdict"] != "fail" || len(gate["frequency_checks"].([]any)) != 4 {
		t.Fatalf("gate = %v", gate)
	}
}

// TestReportOfARecordedReference names the reused execution and the mode the
// runner asked for, recorded at begin since schema 15 (D14).
func TestReportOfARecordedReference(t *testing.T) {
	a := newAPI(t)
	a.completeReportExecution("e1")
	a.beginExecution("e2", 2, map[string]string{"mode": "last"}, 201)
	var ids []string
	for i := range 2 {
		run := fmt.Sprintf("e2-candidate-%d", i+1)
		a.completeIsolatedRun(run, []string{"read", "lookup"})
		ids = append(ids, run)
	}
	r := a.do("POST", "/v1/scenario-executions/e2/complete",
		map[string]any{"candidate_run_ids": ids, "gate_limits": reportLimits()})
	a.mustStatus(r, 200, "complete e2")
	reference := decodeAny(t, a.report("e2"))["reference"].(map[string]any)
	if reference["execution_id"] != "e1" || reference["mode"] != "last" {
		t.Fatalf("reference = %v", reference)
	}
}

// D14: the begin request's cli_version is the report's producers.cli_version,
// as recorded; a begin without one says not_recorded rather than inventing
// one, and a malformed one is refused before anything is recorded.
func TestReportCarriesTheRecordedCLIVersion(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()
	body := beginBody("e-cli", 2, nil)
	body["cli_version"] = "v0.13.0 3e86f9c"
	a.mustStatus(a.do("POST", "/v1/scenario-executions", body), 201, "begin e-cli")
	sides := map[string][][]string{
		"reference": {{"read"}, {"read"}},
		"candidate": {{"read"}, {"read"}},
	}
	complete := map[string]any{"gate_limits": reportLimits()}
	for _, side := range []string{"reference", "candidate"} {
		var ids []string
		for i, ops := range sides[side] {
			run := fmt.Sprintf("e-cli-%s-%d", side, i+1)
			a.completeIsolatedRun(run, ops)
			ids = append(ids, run)
		}
		complete[side+"_run_ids"] = ids
	}
	a.mustStatus(a.do("POST", "/v1/scenario-executions/e-cli/complete", complete), 200, "complete e-cli")
	producers := decodeAny(t, a.report("e-cli"))["producers"].(map[string]any)
	if producers["cli_version"] != "v0.13.0 3e86f9c" {
		t.Fatalf("producers = %v", producers)
	}

	for _, bad := range []string{" v1", "v1\nforged", strings.Repeat("v", 257)} {
		body := beginBody("e-bad", 1, nil)
		body["cli_version"] = bad
		a.mustStatus(a.do("POST", "/v1/scenario-executions", body), 400, "cli_version "+bad)
	}
}

func TestReportRefusesAnExecutionWithoutAComparison(t *testing.T) {
	a := newAPI(t)
	a.mustStatus(a.do("GET", "/v1/scenario-executions/no-such/report", nil), 404, "unknown")
	a.beginExecution("e-running", 1, nil, 201)
	a.mustStatus(a.do("GET", "/v1/scenario-executions/e-running/report", nil), 409, "running")
	a.mustStatus(a.do("POST", "/v1/scenario-executions/e-running/fail", map[string]any{}), 200, "fail")
	a.mustStatus(a.do("GET", "/v1/scenario-executions/e-running/report", nil), 409, "failed")
}

// TestReportOfAnExecutionCompletedBeforeSchema14: no gate was recorded, and
// none is derived. The evidence that needs no limit is still reported.
func TestReportOfAnExecutionCompletedBeforeSchema14(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	a := openAPI(t, path)
	a.completeReportExecution("e-old")
	a.store.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE platform_scenario_executions SET gate_snapshot = NULL`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	a = openAPI(t, path)
	report := decodeAny(t, a.report("e-old"))
	comparison := report["comparison"].(map[string]any)
	if gate := comparison["gate"].(map[string]any); len(gate) != 1 || gate["state"] != "not_recorded" {
		t.Fatalf("gate = %v; want only state not_recorded", gate)
	}
	if _, ok := comparison["gate_limits"]; ok {
		t.Fatal("limits nobody recorded were reported")
	}
	behaviors := comparison["behaviors"].([]any)
	if len(behaviors) != 3 {
		t.Fatalf("behaviors = %v", behaviors)
	}
	for _, b := range behaviors {
		row := b.(map[string]any)
		if row["classification"] != "not_recorded" || row["reference_runs_present"] == nil {
			t.Fatalf("behavior = %v", row)
		}
	}
	if comparison["suggestions"] == nil || comparison["sameness"] == nil {
		t.Fatal("evidence that needs no limit is missing")
	}
	if report["scenario"].(map[string]any)["name"] != "support" {
		t.Fatalf("scenario = %v", report["scenario"])
	}
}

// TestReportSaysWhyThereIsNoCost: without pricing the cost section is absent,
// and the report states the reason.
func TestReportSaysWhyThereIsNoCost(t *testing.T) {
	a := newAPI(t)
	a.completeReportExecution("e1")
	operational := decodeAny(t, a.report("e1"))["comparison"].(map[string]any)["operational"].(map[string]any)
	if _, ok := operational["cost"]; ok || operational["cost_reason"] != "pricing_not_configured" {
		t.Fatalf("operational = %v", operational)
	}
	for _, name := range []string{"latency", "errors", "tokens"} {
		section := operational[name].(map[string]any)
		if section["comparable"] == false {
			if _, ok := section["delta"]; ok || section["reason"] == nil {
				t.Fatalf("%s is not comparable and carries a delta or no reason: %v", name, section)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// apiOver serves a store through the real control plane and handler, with
// the fixed clock every API test uses.
func apiOver(t *testing.T, store platform.Store) *api {
	t.Helper()
	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewHandler(plane, httpapi.WithClock(func() time.Time { return testEpoch }))
	if err != nil {
		t.Fatal(err)
	}
	return &api{t: t, server: handler}
}

// TestReportIsByteIdenticalOnBothBackends: the same execution, recorded on
// SQLite and on PostgreSQL, reports the same bytes. Skips without a database.
func TestReportIsByteIdenticalOnBothBackends(t *testing.T) {
	dsn := os.Getenv("TRUSTVIAN_TEST_POSTGRES_DSN")
	if strings.TrimSpace(dsn) == "" {
		t.Skip("TRUSTVIAN_TEST_POSTGRES_DSN is not set; skipping the PostgreSQL half")
	}
	sqlite, err := platform.OpenSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlite.Close() })
	postgres, err := platform.OpenPostgresStore(t.Context(), platform.PostgresConfig{DSN: isolatedSchema(t, dsn)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { postgres.Close() })

	var reports [][]byte
	for _, store := range []platform.Store{sqlite, postgres} {
		a := apiOver(t, store)
		a.completeReportExecution("e-golden")
		reports = append(reports, a.report("e-golden"))
	}
	if !bytes.Equal(reports[0], reports[1]) {
		t.Fatalf("SQLite and PostgreSQL disagree:\n%s\n%s", reports[0], reports[1])
	}
}

// isolatedSchema gives a test its own PostgreSQL schema, dropped afterwards.
func isolatedSchema(t *testing.T, dsn string) string {
	t.Helper()
	schema := fmt.Sprintf("tv_httpapi_report_%d", time.Now().UnixNano())
	admin, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(context.Background(), `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "search_path=" + schema
}

type reportRowsBody struct {
	Comparison struct {
		Runs        int `json:"runs"`
		Repetitions []struct {
			Side  string `json:"side"`
			Index int    `json:"index"`
			RunID string `json:"run_id"`
		} `json:"repetitions"`
		Behaviors []struct {
			FingerprintID        string `json:"fingerprint_id"`
			ReferenceRunsPresent string `json:"reference_runs_present"`
			CandidateRunsPresent string `json:"candidate_runs_present"`
			ReferenceFrequency   struct {
				Runs                 string  `json:"runs"`
				CallsPerRunMin       *string `json:"calls_per_run_min"`
				CallsPerRunMax       *string `json:"calls_per_run_max"`
				CallsPerRunMeanMilli *string `json:"calls_per_run_mean_milli"`
			} `json:"reference_frequency"`
			Display   map[string]string `json:"display"`
			PresentIn map[string][]struct {
				Index int    `json:"index"`
				RunID string `json:"run_id"`
			} `json:"present_in"`
		} `json:"behaviors"`
		Targets []struct {
			TargetName         string            `json:"target_name"`
			CallRatioAvailable bool              `json:"call_ratio_available"`
			CallRatioPermille  *string           `json:"call_ratio_permille"`
			Display            map[string]string `json:"display"`
		} `json:"targets"`
	} `json:"comparison"`
}

// TestReportRowsCarryTheirDisplayStringsBesideTheIntegers: every string is
// the formatting of an integer the row carries, which stays beside it.
func TestReportRowsCarryTheirDisplayStringsBesideTheIntegers(t *testing.T) {
	a := newAPI(t)
	a.completeReportExecution("e1")
	var body reportRowsBody
	if err := json.Unmarshal(a.report("e1"), &body); err != nil {
		t.Fatal(err)
	}
	c := body.Comparison
	if len(c.Behaviors) != 3 || len(c.Targets) == 0 {
		t.Fatalf("fixture has %d behaviors, %d targets", len(c.Behaviors), len(c.Targets))
	}
	for _, b := range c.Behaviors {
		if b.Display["reference_presence"] != fmt.Sprintf("%s/%d", b.ReferenceRunsPresent, c.Runs) ||
			b.Display["candidate_presence"] != fmt.Sprintf("%s/%d", b.CandidateRunsPresent, c.Runs) {
			t.Errorf("%s presence display %v", b.FingerprintID, b.Display)
		}
		f := b.ReferenceFrequency
		if want := *f.CallsPerRunMin + "–" + *f.CallsPerRunMax; b.Display["reference_calls_per_run"] != want {
			t.Errorf("%s calls per run %q, want %q", b.FingerprintID, b.Display["reference_calls_per_run"], want)
		}
		if len(b.Display) != 6 || b.Display["reference_calls_per_run_mean"] == "" {
			t.Errorf("%s display %v", b.FingerprintID, b.Display)
		}
		if b.ReferenceRunsPresent == "0" && b.Display["reference_calls_per_run"] != "0–0" {
			t.Errorf("an absent behavior's range is not 0–0: %v", b.Display)
		}
	}
	for _, target := range c.Targets {
		want := "not available"
		if target.CallRatioAvailable {
			want = platform.RenderRatio(mustUint(t, *target.CallRatioPermille))
		}
		if target.Display["call_ratio"] != want {
			t.Errorf("%s ratio %q, want %q", target.TargetName, target.Display["call_ratio"], want)
		}
	}
}

func mustUint(t *testing.T, s string) uint64 {
	t.Helper()
	var v uint64
	if _, err := fmt.Sscan(s, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestEveryReportRowResolvesThroughEvidence: present_in names, per side, as
// many repetitions as the presence count, with their runs, and the
// behavior's observations resolve through task 085 from any of them.
func TestEveryReportRowResolvesThroughEvidence(t *testing.T) {
	a := newAPI(t)
	a.completeReportExecution("e1")
	var body reportRowsBody
	if err := json.Unmarshal(a.report("e1"), &body); err != nil {
		t.Fatal(err)
	}
	c := body.Comparison
	runOf := map[string]string{}
	first := map[string]string{}
	for _, r := range c.Repetitions {
		runOf[fmt.Sprintf("%s/%d", r.Side, r.Index)] = r.RunID
		if _, ok := first[r.Side]; !ok {
			first[r.Side] = r.RunID
		}
	}
	for _, b := range c.Behaviors {
		for side, count := range map[string]string{"reference": b.ReferenceRunsPresent, "candidate": b.CandidateRunsPresent} {
			present := b.PresentIn[side]
			if fmt.Sprint(len(present)) != count {
				t.Fatalf("%s %s: present_in %v, count %s", b.FingerprintID, side, present, count)
			}
			for _, p := range present {
				if runOf[fmt.Sprintf("%s/%d", side, p.Index)] != p.RunID {
					t.Fatalf("%s %s: repetition %d is not run %s", b.FingerprintID, side, p.Index, p.RunID)
				}
				pair := map[string]string{"reference": first["reference"], "candidate": first["candidate"]}
				pair[side] = p.RunID
				r := a.do("GET", fmt.Sprintf("/v1/evidence/observations?reference_run_id=%s&candidate_run_id=%s&behavior=%s&side=%s",
					pair["reference"], pair["candidate"], b.FingerprintID, side), nil)
				a.mustStatus(r, 200, "resolve "+b.FingerprintID)
				var resolved struct {
					Observations []struct {
						FingerprintID string `json:"fingerprint_id"`
					} `json:"observations"`
				}
				if err := json.Unmarshal(r.Body.Bytes(), &resolved); err != nil {
					t.Fatal(err)
				}
				if len(resolved.Observations) == 0 || resolved.Observations[0].FingerprintID != b.FingerprintID {
					t.Fatalf("%s %s repetition %d resolved %+v", b.FingerprintID, side, p.Index, resolved)
				}
			}
		}
	}
}

// reportExamplePath is the version 2 example docs/compatibility.md publishes.
const reportExamplePath = "testdata/report-v2-example.json"

// TestReportVersion2ExampleIsPinned: the example the compatibility contract
// publishes is this route's output for the fixture execution, with a pricing
// table configured, byte for byte once indented — and the document quotes it
// verbatim. Set TRUSTVIAN_UPDATE_GOLDEN=1 to rewrite the testdata file.
func TestReportVersion2ExampleIsPinned(t *testing.T) {
	a := newAPI(t)
	a.completeReportExecution("e1")
	priced := &api{t: t, server: a.pricedHandler(map[string]platform.ModelPrice{
		"op-read": {InputMicrosPerMillion: 1, OutputMicrosPerMillion: 1}}), store: a.store}
	raw := priced.report("e1")
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// The producer version is the build's; the example names none.
	version := doc["producers"].(map[string]any)["control_plane_version"].(string)
	raw = bytes.ReplaceAll(raw, []byte(`"control_plane_version":"`+version+`"`),
		[]byte(`"control_plane_version":"v0.12.0"`))
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	indented.WriteByte('\n')
	if os.Getenv("TRUSTVIAN_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(reportExamplePath, indented.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(reportExamplePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(indented.Bytes(), want) {
		t.Fatalf("the report changed from the published example:\n%s", indented.Bytes())
	}
	contract, err := os.ReadFile(filepath.Join("..", "..", "docs", "compatibility.md"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "<!-- result-document-v2-example: platform/httpapi/" + reportExamplePath + " -->\n\n```json\n"
	start := bytes.Index(contract, []byte(marker))
	if start < 0 {
		t.Fatal("docs/compatibility.md does not publish the version 2 example")
	}
	block := contract[start+len(marker):]
	end := bytes.Index(block, []byte("```\n"))
	if end < 0 || !bytes.Equal(block[:end], want) {
		t.Fatal("docs/compatibility.md's version 2 example differs from " + reportExamplePath)
	}
}

// Exit criterion 7: the report's counts are the control plane's totals of its
// own rows, so no adapter counts them. With the gate not recorded every row
// counts as not recorded.
func TestReportCountsAreTheRowsTotals(t *testing.T) {
	type counts struct {
		Rows, Added, Removed, Neither, Lost string
		NotRecorded                         string `json:"not_recorded"`
	}
	type body struct {
		Comparison struct {
			Counts    counts `json:"counts"`
			Behaviors []struct {
				Classification string `json:"classification"`
				Lost           bool   `json:"lost"`
			} `json:"behaviors"`
		} `json:"comparison"`
	}
	tally := func(b body) counts {
		n := map[string]int{}
		lost := 0
		for _, row := range b.Comparison.Behaviors {
			n[row.Classification]++
			if row.Lost {
				lost++
			}
		}
		return counts{Rows: fmt.Sprint(len(b.Comparison.Behaviors)), Added: fmt.Sprint(n["added"]),
			Removed: fmt.Sprint(n["removed"]), Neither: fmt.Sprint(n["neither"]),
			NotRecorded: fmt.Sprint(n["not_recorded"]), Lost: fmt.Sprint(lost)}
	}
	path := filepath.Join(t.TempDir(), "platform.db")
	a := openAPI(t, path)
	a.completeReportExecution("e1")
	var recorded body
	if err := json.Unmarshal(a.report("e1"), &recorded); err != nil {
		t.Fatal(err)
	}
	if got, want := recorded.Comparison.Counts, tally(recorded); got != want || got.Added == "0" || got.Lost == "0" {
		t.Fatalf("counts %+v, rows give %+v", got, want)
	}

	a.store.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE platform_scenario_executions SET gate_snapshot = NULL WHERE id = 'e1'`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	a = openAPI(t, path)
	var unrecorded body
	if err := json.Unmarshal(a.report("e1"), &unrecorded); err != nil {
		t.Fatal(err)
	}
	if got := unrecorded.Comparison.Counts; got != tally(unrecorded) || got.NotRecorded != got.Rows || got.Added != "0" {
		t.Fatalf("counts without a recorded gate %+v", got)
	}
}
