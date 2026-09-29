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
import { observationRow } from "./evidence.js";
const payload = JSON.parse(process.argv[2]);
process.stdout.write(JSON.stringify(payload.observations.map(observationRow)));
`

// renderObservations runs the shipped row projection over one route's response.
func renderObservations(t *testing.T, payload []byte) []renderedRow {
	t.Helper()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; this cross-layer check needs the shipped assets run")
	}

	dir := t.TempDir()
	for _, name := range []string{"evidence.js", "trace.js", "render.js"} {
		body, readErr := os.ReadFile(filepath.Join(webuiAssetDir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "driver.mjs"), []byte(rowDriver), 0o600); err != nil {
		t.Fatalf("write driver: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodeBin, "driver.mjs", string(payload))
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the shipped renderer under node: %v\n%s", err, stderr.String())
	}

	var rows []renderedRow
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
