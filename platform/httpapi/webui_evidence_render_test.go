package httpapi_test

// What the browser draws must be what /v1 returned (task 076, criterion 15).
//
// The WebUI package's own suite proves the *shape* of the rendering — that no
// status is computed there, that a degenerate parent reference is classified
// honestly, that a stale response is discarded. What it structurally cannot
// prove is that the values reaching a cell are the values the control plane
// published, because that package may not reach the platform and its tests have
// no control plane to ask.
//
// This test closes that gap from the side that has one. It builds real evidence
// through the real ingest path, reads two authoritative responses over `/v1`,
// runs the shipped row projection over each of them under `node`, and compares
// cell by cell. It asserts the rendering's *inputs* against the route's
// response rather than trusting the view — which is what the criterion asks for
// and is a different thing from asserting the view looks right.
//
// The assets are read from disk rather than through the webui package, which
// exports no reader and must not gain one: a package handed a way to serve
// arbitrary files is the filesystem web root ADR 0036 declined.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// webuiAssetDir is where the shipped browser source lives, relative to here.
const webuiAssetDir = "../webui/assets"

// renderedRow is what evidence.js's observationRow produces for one observation.
type renderedRow struct {
	Sequence         string `json:"sequence"`
	RecordedAt       string `json:"recordedAt"`
	Behavior         string `json:"behavior"`
	Fingerprint      string `json:"fingerprint"`
	NewBehavior      string `json:"newBehavior"`
	Decision         string `json:"decision"`
	Risk             string `json:"risk"`
	Trust            string `json:"trust"`
	Anomaly          string `json:"anomaly"`
	Confidence       string `json:"confidence"`
	Duration         string `json:"duration"`
	DurationMeasured bool   `json:"durationMeasured"`
	SpanStatus       string `json:"spanStatus"`
	Session          string `json:"session"`
	Trace            string `json:"trace"`
	Span             string `json:"span"`
}

// rowDriver projects a whole /v1 payload through the shipped renderer.
//
// The payload is passed in as the bytes the route returned, parsed by the same
// JSON the browser would parse. Nothing is reshaped on the way in, because a
// reshaping step here is where a mismatch would be hidden.
const rowDriver = `
import { observationRow } from "./views/evidence.js";
const payload = JSON.parse(process.argv[2]);
process.stdout.write(JSON.stringify(payload.observations.map(observationRow)));
`

// runShippedDriver executes one driver against the shipped browser assets.
func runShippedDriver(t *testing.T, driver string, arg string) []byte {
	t.Helper()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; this cross-layer check needs the shipped assets run")
	}

	// The whole script tree, with its directories, rather than the three
	// modules this driver names. The bundle is layered — a view imports the
	// /v1 renderer, which imports the DOM core — and a flattened copy would
	// run modules whose relative imports mean something other than what
	// ships, which is the opposite of what a cross-layer check is for.
	dir := t.TempDir()
	copied := 0
	walkErr := filepath.WalkDir(webuiAssetDir, func(from string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".js") {
			return nil
		}
		rel, relErr := filepath.Rel(webuiAssetDir, from)
		if relErr != nil {
			return relErr
		}
		body, readErr := os.ReadFile(from)
		if readErr != nil {
			return readErr
		}
		target := filepath.Join(dir, rel)
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o700); mkErr != nil {
			return mkErr
		}
		copied++
		return os.WriteFile(target, body, 0o600)
	})
	if walkErr != nil {
		t.Fatalf("copy the shipped assets: %v", walkErr)
	}
	if copied == 0 {
		t.Fatalf("no script assets found under %s", webuiAssetDir)
	}
	if err := os.WriteFile(filepath.Join(dir, "driver.mjs"), []byte(driver), 0o600); err != nil {
		t.Fatalf("write driver: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodeBin, "driver.mjs", arg)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the shipped assets under node: %v\n%s", err, stderr.String())
	}
	return stdout
}

// renderObservations runs the shipped row projection over one route's response.
func renderObservations(t *testing.T, payload []byte) []renderedRow {
	t.Helper()
	var rows []renderedRow
	stdout := runShippedDriver(t, rowDriver, string(payload))
	if err := json.Unmarshal(stdout, &rows); err != nil {
		t.Fatalf("decode rendered rows: %v\n%s", err, stdout)
	}
	return rows
}

// authoritativeObservation is the response's own view of one observation, read
// with json.Number so a float's published text is compared rather than a
// re-formatting of it.
type authoritativeObservation struct {
	Sequence          string      `json:"sequence"`
	Timestamp         string      `json:"timestamp"`
	FingerprintID     string      `json:"fingerprint_id"`
	NewBehavior       bool        `json:"new_behavior"`
	Decision          string      `json:"decision"`
	RiskLevel         string      `json:"risk_level"`
	TrustScore        json.Number `json:"trust_score"`
	AnomalyScore      json.Number `json:"anomaly_score"`
	AnomalyConfidence json.Number `json:"anomaly_confidence"`
	DurationNanos     string      `json:"duration_nanos"`
	SpanStatus        string      `json:"span_status"`
	SessionID         string      `json:"session_id"`
	TraceID           string      `json:"trace_id"`
	SpanID            string      `json:"span_id"`
	Behavior          struct {
		ActorType         string `json:"actor_type"`
		OperationCategory string `json:"operation_category"`
		OperationName     string `json:"operation_name"`
		TargetName        string `json:"target_name"`
		TargetCategory    string `json:"target_category"`
		Environment       string `json:"environment"`
	} `json:"behavior"`
}

func decodeAuthoritative(t *testing.T, payload []byte) []authoritativeObservation {
	t.Helper()
	var body struct {
		Observations []authoritativeObservation `json:"observations"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("decode authoritative payload: %v\n%s", err, payload)
	}
	return body.Observations
}

// compareRendering asserts every rendered cell against the response's own field.
func compareRendering(
	t *testing.T, label string,
	authoritative []authoritativeObservation, rendered []renderedRow,
) {
	t.Helper()
	if len(authoritative) == 0 {
		t.Fatalf("%s: the route returned no observations, so this comparison would "+
			"prove nothing", label)
	}
	if len(rendered) != len(authoritative) {
		t.Fatalf("%s: the renderer produced %d rows for %d observations",
			label, len(rendered), len(authoritative))
	}

	for i, want := range authoritative {
		got := rendered[i]
		for _, field := range []struct {
			name      string
			got, want string
		}{
			{"sequence", got.Sequence, want.Sequence},
			{"recorded at", got.RecordedAt, want.Timestamp},
			{"fingerprint", got.Fingerprint, want.FingerprintID},
			{"decision", got.Decision, want.Decision},
			{"risk level", got.RiskLevel(), want.RiskLevel},
			{"trust score", got.Trust, want.TrustScore.String()},
			{"anomaly score", got.Anomaly, want.AnomalyScore.String()},
			{"anomaly confidence", got.Confidence, want.AnomalyConfidence.String()},
			{"session", got.Session, want.SessionID},
			{"trace", got.Trace, want.TraceID},
			{"span", got.Span, want.SpanID},
		} {
			if field.got != field.want {
				t.Errorf("%s row %d: %s rendered %q, the route returned %q",
					label, i, field.name, field.got, field.want)
			}
		}

		// The descriptor is rendered as recorded, with no name invented and no
		// recorded part dropped.
		for _, part := range []string{
			want.Behavior.OperationCategory, want.Behavior.OperationName,
			want.Behavior.TargetName, want.Behavior.TargetCategory,
		} {
			if part == "" {
				continue
			}
			if !strings.Contains(got.Behavior, part) {
				t.Errorf("%s row %d: the rendered action %q omits the recorded %q",
					label, i, got.Behavior, part)
			}
		}

		// The three fields whose rendering is a documented mapping rather than a
		// passthrough. Each is a distinction task 084 made on the record, and the
		// point of checking them here is that the mapping is applied to the
		// published value rather than to a guess about it.
		if measured := want.DurationNanos != ""; got.DurationMeasured != measured {
			t.Errorf("%s row %d: duration_nanos %q rendered measured=%v",
				label, i, want.DurationNanos, got.DurationMeasured)
		}
		if want.DurationNanos != "" && !strings.Contains(got.Duration, "ms") {
			t.Errorf("%s row %d: a measured duration rendered %q with no unit",
				label, i, got.Duration)
		}
		if want.SpanStatus == "ok" && got.SpanStatus != "ok" {
			t.Errorf("%s row %d: span_status ok rendered %q", label, i, got.SpanStatus)
		}
		wantNew := "already seen in this run"
		if want.NewBehavior {
			wantNew = "new to this run"
		}
		if got.NewBehavior != wantNew {
			t.Errorf("%s row %d: new_behavior %v rendered %q, want %q",
				label, i, want.NewBehavior, got.NewBehavior, wantNew)
		}
	}
}

// RiskLevel reads the risk cell under the name the response uses, so the table
// above reads as field-against-field.
func (r renderedRow) RiskLevel() string { return r.Risk }

// seedComparisonWithHistory builds two completed runs whose candidate added a
// behavior, with correlation on every record.
func seedComparisonWithHistory(a *api) {
	a.t.Helper()
	a.seedHierarchy()
	for _, runID := range []string{"run-ref", "run-cand"} {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
			"id": runID, "candidate_id": "cand-1",
			"environment": testEnvironment, "behavioral_profile": testProfile,
		}), 201, "create "+runID)
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start "+runID)
	}

	ingest := func(runID string, sequence uint64, record trustvian.DecisionRecord) {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/records",
			envelope(sequence, record)), 200, "ingest "+runID)
	}

	shared := func(eventID string) trustvian.DecisionRecord {
		record := apiRecord(eventID, "fp-shared", "tool_list")
		record.TraceID = "trace-1"
		record.SpanID = "span-" + eventID
		record.SessionID = "session-1"
		record.SpanLineage = event.LineageRoot
		record.DurationNanos = "1500000"
		record.SpanStatus = event.StatusOK
		return record
	}

	ingest("run-ref", 1, shared("r1"))

	ingest("run-cand", 1, shared("c1"))
	added := apiRecord("c2", "fp-export", "tool_export")
	added.TraceID = "trace-1"
	added.SpanID = "span-c2"
	added.ParentSpanID = "span-c1"
	added.SpanLineage = event.LineageChild
	added.SessionID = "session-1"
	// A measured zero, deliberately: the one value a renderer is most likely to
	// flatten into "unavailable".
	added.DurationNanos = "0"
	added.SpanStatus = event.StatusError
	added.Decision = "block"
	added.RiskLevel = "critical"
	ingest("run-cand", 2, added)

	for _, runID := range []string{"run-ref", "run-cand"} {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/complete", nil), 200,
			"complete "+runID)
	}
}

// TestResolvedEvidenceRendersWhatTheRouteReturned is criterion 15.
func TestResolvedEvidenceRendersWhatTheRouteReturned(t *testing.T) {
	a := newAPI(t)
	seedComparisonWithHistory(a)

	resolution := a.do("GET",
		"/v1/evidence/observations?reference_run_id=run-ref&candidate_run_id=run-cand"+
			"&behavior=fp-export", nil)
	a.mustStatus(resolution, 200, "resolve observations")
	payload := resolution.Body.Bytes()

	authoritative := decodeAuthoritative(t, payload)
	rendered := renderObservations(t, payload)
	compareRendering(t, "resolution", authoritative, rendered)

	// The added behavior's observation carries a measured zero and an error
	// status, so this fixture actually exercises the two mappings that matter.
	if len(authoritative) != 1 {
		t.Fatalf("the resolution returned %d observations, want the one added "+
			"behavior's", len(authoritative))
	}
	if authoritative[0].DurationNanos != "0" {
		t.Fatalf("the fixture's measured zero did not survive ingest: %q",
			authoritative[0].DurationNanos)
	}
	if !rendered[0].DurationMeasured {
		t.Error("a measured zero rendered as unmeasured")
	}
	if rendered[0].SpanStatus == "ok" {
		t.Errorf("an error status rendered %q", rendered[0].SpanStatus)
	}
}

// TestCorrelatedHistoryRendersWhatTheRouteReturned is the same check over the
// run-scoped narrowings this task added.
func TestCorrelatedHistoryRendersWhatTheRouteReturned(t *testing.T) {
	a := newAPI(t)
	seedComparisonWithHistory(a)

	for _, tc := range []struct {
		label string
		query string
	}{
		{"unnarrowed", ""},
		{"session", "?session_id=" + url.QueryEscape("session-1")},
		{"trace", "?trace_id=" + url.QueryEscape("trace-1")},
		{"behavior", "?fingerprint_id=" + url.QueryEscape("fp-export")},
	} {
		t.Run(tc.label, func(t *testing.T) {
			response := a.do("GET",
				fmt.Sprintf("/v1/evaluation-runs/run-cand/observations%s", tc.query), nil)
			a.mustStatus(response, 200, tc.label)
			payload := response.Body.Bytes()
			compareRendering(t, tc.label,
				decodeAuthoritative(t, payload), renderObservations(t, payload))
		})
	}
}

// ---------------------------------------------------------------------
// Aggregate-only results describe applicability, not retained history
// ---------------------------------------------------------------------

// headerDriver reports what the shipped header rule would render for a payload,
// together with the sentences it would have used.
//
// The sentences are returned as well as the flags, so the assertions can be
// about the words a developer reads rather than about a boolean somebody could
// rename.
const headerDriver = `
import { findingPresentation, statusSentence, historySentence, EXHAUSTIVE_CAVEAT }
  from "./views/evidence.js";
const payload = JSON.parse(process.argv[2]);
const shape = findingPresentation(payload);
const history = payload.history === undefined || payload.history === null
  ? {} : payload.history;
process.stdout.write(JSON.stringify({
  shape,
  status: payload.status,
  recordedCount: payload.recorded_count,
  statusSentence: statusSentence(payload.status),
  // What the header *would* have said had it rendered the history block. Not
  // rendered when shape.showHistory is false; returned here so a test can show
  // what the unconditional rendering was claiming.
  historySentenceIfShown: historySentence(history.state),
  exhaustiveCaveat: EXHAUSTIVE_CAVEAT,
}));
`

type headerRendering struct {
	Shape struct {
		DescribesRetainedHistory bool   `json:"describesRetainedHistory"`
		ShowHistory              bool   `json:"showHistory"`
		ShowExhaustiveCaveat     bool   `json:"showExhaustiveCaveat"`
		ShowRows                 bool   `json:"showRows"`
		CountNote                string `json:"countNote"`
	} `json:"shape"`
	Status                 string `json:"status"`
	RecordedCount          string `json:"recordedCount"`
	StatusSentence         string `json:"statusSentence"`
	HistorySentenceIfShown string `json:"historySentenceIfShown"`
	ExhaustiveCaveat       string `json:"exhaustiveCaveat"`
}

func renderHeader(t *testing.T, payload []byte) headerRendering {
	t.Helper()
	var out headerRendering
	stdout := runShippedDriver(t, headerDriver, string(payload))
	if err := json.Unmarshal(stdout, &out); err != nil {
		t.Fatalf("decode header rendering: %v\n%s", err, stdout)
	}
	return out
}

// TestAggregateOnlyFindingsClaimNothingAboutRetainedHistory is the second review
// finding.
//
// `reference_evidence` and `candidate_evidence` are minimum-count checks: they
// fail when a run observed *too little*, so the control plane reports
// `aggregate_only` and returns **without reading any history at all**. The
// history and exhaustiveness fields on that response are therefore the zero
// value — `unavailable`, `retained_count: "0"`, `complete: false`,
// `exhaustive: false`.
//
// Rendered unconditionally, those defaults told a developer that a run whose
// retained history is **complete** had none of it retained, and that a result
// with no rows at all was "a sample". Both runs here are seeded to completeness
// on purpose, so the payload's defaults and the run's actual state disagree — and
// the test asserts the header believes the run rather than the zero value.
func TestAggregateOnlyFindingsClaimNothingAboutRetainedHistory(t *testing.T) {
	a := newAPI(t)
	seedComparisonWithHistory(a)

	// Both sides really do have complete retained history, or the whole premise
	// of this test is wrong.
	for _, runID := range []string{"run-ref", "run-cand"} {
		response := a.do("GET", "/v1/evaluation-runs/"+runID+"/observations", nil)
		a.mustStatus(response, 200, "history of "+runID)
		var body struct {
			HistoryState string `json:"history_state"`
			Complete     bool   `json:"complete"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode history of %s: %v", runID, err)
		}
		if body.HistoryState != "complete" || !body.Complete {
			t.Fatalf("%s reports history %q (complete=%v); this test needs a run "+
				"whose retained history is complete", runID, body.HistoryState, body.Complete)
		}
	}

	for _, check := range []string{"reference_evidence", "candidate_evidence"} {
		for _, route := range []string{"behaviors", "observations"} {
			t.Run(check+"/"+route, func(t *testing.T) {
				response := a.do("GET", fmt.Sprintf(
					"/v1/evidence/%s?reference_run_id=run-ref&candidate_run_id=run-cand&check=%s",
					route, check), nil)
				a.mustStatus(response, 200, check)
				payload := response.Body.Bytes()
				got := renderHeader(t, payload)

				if got.Status != "aggregate_only" {
					t.Fatalf("status = %q, want aggregate_only; this fixture no longer "+
						"exercises the case", got.Status)
				}

				// The applicability answer and the authoritative count both stay.
				if !strings.Contains(got.StatusSentence, "no observation that caused it") {
					t.Errorf("the aggregate-only explanation is %q", got.StatusSentence)
				}
				if got.RecordedCount == "" || got.RecordedCount == "0" {
					t.Errorf("recorded count = %q; the gate's own actual must stay "+
						"visible", got.RecordedCount)
				}

				// And nothing about retained history is claimed.
				if got.Shape.ShowHistory {
					t.Errorf("the header would render the history block, which for "+
						"this result says %q — about a run whose retained history is "+
						"complete", got.HistorySentenceIfShown)
				}
				if got.Shape.ShowExhaustiveCaveat {
					t.Errorf("the header would render %q, about a result with no "+
						"observations at all", got.ExhaustiveCaveat)
				}
				if got.Shape.DescribesRetainedHistory || got.Shape.ShowRows {
					t.Errorf("the header would describe retained observations: %+v",
						got.Shape)
				}
				for _, claim := range []string{
					"Retained history is bounded", "sample", "not complete", "retained",
				} {
					if strings.Contains(got.Shape.CountNote, claim) {
						t.Errorf("the count note claims %q: %s", claim, got.Shape.CountNote)
					}
				}

				// The premise, stated: the payload really does carry the defaults
				// that the old rendering was reading as facts.
				if !strings.Contains(string(payload), `"state":"unavailable"`) {
					t.Errorf("the aggregate-only payload no longer carries an "+
						"unavailable history default, so this guard would pass for "+
						"the wrong reason:\n%s", payload)
				}
			})
		}
	}
}

// TestApplicableResolutionsStillDescribeTheirRetainedHistory is the other half.
//
// Narrowing what an aggregate-only result may claim must not narrow what a result
// that actually read history says. This drives the two resolutions that do read
// it, through the real routes.
//
// The partial and unavailable *states* are asserted at the presentation layer in
// platform/webui's controller test rather than here: reaching either through this
// API needs a run of 4096 observations or a schema-6 migration fixture, and the
// property under test is which block the header renders for a given state.
func TestApplicableResolutionsStillDescribeTheirRetainedHistory(t *testing.T) {
	a := newAPI(t)
	seedComparisonWithHistory(a)

	for name, path := range map[string]string{
		"behaviors": "/v1/evidence/behaviors?reference_run_id=run-ref&" +
			"candidate_run_id=run-cand&check=added_behaviors",
		"observations": "/v1/evidence/observations?reference_run_id=run-ref&" +
			"candidate_run_id=run-cand&behavior=fp-export",
	} {
		t.Run(name, func(t *testing.T) {
			response := a.do("GET", path, nil)
			a.mustStatus(response, 200, name)
			got := renderHeader(t, response.Body.Bytes())

			if got.Status != "resolved" {
				t.Fatalf("status = %q, want resolved", got.Status)
			}
			if !got.Shape.ShowHistory || !got.Shape.ShowRows ||
				!got.Shape.DescribesRetainedHistory {
				t.Errorf("a resolution that read history would render %+v", got.Shape)
			}
			if !strings.Contains(got.HistorySentenceIfShown, "Every accepted record") {
				t.Errorf("the history sentence for a complete run is %q",
					got.HistorySentenceIfShown)
			}
			// Complete history, so nothing here is a sample.
			if got.Shape.ShowExhaustiveCaveat {
				t.Error("a resolution over complete history claims to be a sample")
			}
			if !strings.Contains(got.Shape.CountNote, "Retained history is bounded") {
				t.Errorf("the count note lost its retained-history caveat: %s",
					got.Shape.CountNote)
			}
		})
	}
}
