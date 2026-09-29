package httpapi_test

// The /v1 observation-history route, task 067.
//
// It is one bounded read over evidence the ingest path already wrote, so the
// assertions here are about the collection contract and the honesty fields —
// what the platform retains is asserted in the platform package, and what it
// must never retain is asserted by the privacy sweep.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

type observationPageBody struct {
	Version       string `json:"version"`
	RunID         string `json:"run_id"`
	HistoryState  string `json:"history_state"`
	RetainedCount string `json:"retained_count"`
	Complete      bool   `json:"complete"`
	NextAfter     string `json:"next_after"`

	Observations []struct {
		Sequence      string `json:"sequence"`
		EventID       string `json:"event_id"`
		Timestamp     string `json:"timestamp"`
		FingerprintID string `json:"fingerprint_id"`
		NewBehavior   bool   `json:"new_behavior"`
		Decision      string `json:"decision"`
		RiskLevel     string `json:"risk_level"`
		TraceID       string `json:"trace_id"`
		SpanID        string `json:"span_id"`
		SessionID     string `json:"session_id"`
		ParentSpanID  string `json:"parent_span_id"`
		SpanLineage   string `json:"span_lineage"`
		DurationNanos string `json:"duration_nanos"`
		SpanStatus    string `json:"span_status"`
		Behavior      struct {
			OperationName string `json:"operation_name"`
		} `json:"behavior"`
	} `json:"observations"`
}

func decodeObservationPage(t *testing.T, r *httptest.ResponseRecorder) observationPageBody {
	t.Helper()
	var body observationPageBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode observation page: %v (%s)", err, r.Body.String())
	}
	return body
}

// observationRecord is a record carrying every correlation and operational
// field the route is expected to publish.
func observationRecord(eventID, fingerprintID, operation string) trustvian.DecisionRecord {
	record := apiRecord(eventID, fingerprintID, operation)
	record.TraceID = "trace-" + eventID
	record.SpanID = "span-" + eventID
	record.SessionID = "session-1"
	record.ParentSpanID = "parent-" + eventID
	record.SpanLineage = event.LineageChild
	record.DurationNanos = "1500"
	record.SpanStatus = event.StatusOK
	return record
}

// The route publishes what was retained, in ingest order, with the history
// state that says how much of the run it describes.
func TestObservationRouteReturnsRetainedHistory(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")

	for i := 1; i <= 3; i++ {
		record := observationRecord(
			fmt.Sprintf("e%d", i), fmt.Sprintf("fp-%d", i), fmt.Sprintf("tool_%d", i))
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
			envelope(uint64(i), record)), 200, "ingest")
	}

	response := a.do("GET", "/v1/evaluation-runs/run-1/observations", nil)
	a.mustStatus(response, 200, "observations")
	page := decodeObservationPage(t, response)

	if page.HistoryState != "complete" {
		t.Errorf("history_state = %q, want complete", page.HistoryState)
	}
	if !page.Complete {
		t.Error("complete = false for a run that retained every record")
	}
	if page.RetainedCount != "3" {
		t.Errorf("retained_count = %q, want 3", page.RetainedCount)
	}
	if page.NextAfter != "" {
		t.Errorf("next_after = %q on a page with nothing following", page.NextAfter)
	}
	if len(page.Observations) != 3 {
		t.Fatalf("returned %d observations, want 3", len(page.Observations))
	}

	for i, o := range page.Observations {
		want := fmt.Sprintf("%d", i+1)
		if o.Sequence != want {
			t.Errorf("observation %d has sequence %q, want %q", i, o.Sequence, want)
		}
		if o.EventID != fmt.Sprintf("e%d", i+1) {
			t.Errorf("observation %d has event_id %q", i, o.EventID)
		}
		if o.ParentSpanID == "" || o.SpanLineage != "child" {
			t.Errorf("observation %d lost its lineage: parent=%q lineage=%q",
				i, o.ParentSpanID, o.SpanLineage)
		}
		if o.DurationNanos != "1500" || o.SpanStatus != "ok" {
			t.Errorf("observation %d lost its operational evidence: duration=%q status=%q",
				i, o.DurationNanos, o.SpanStatus)
		}
		if o.SessionID != "session-1" || o.TraceID == "" {
			t.Errorf("observation %d lost its correlation: session=%q trace=%q",
				i, o.SessionID, o.TraceID)
		}
	}
}

// next_after is present exactly when another row follows, and paging with it
// visits every observation once.
func TestObservationRoutePagesWithoutDuplicatesOrOmissions(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")

	const total = 7
	for i := 1; i <= total; i++ {
		record := observationRecord(
			fmt.Sprintf("e%d", i), fmt.Sprintf("fp-%d", i), fmt.Sprintf("tool_%d", i))
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
			envelope(uint64(i), record)), 200, "ingest")
	}

	var seen []string
	path := "/v1/evaluation-runs/run-1/observations?limit=2"
	for {
		response := a.do("GET", path, nil)
		a.mustStatus(response, 200, "observations page")
		page := decodeObservationPage(t, response)

		if len(page.Observations) > 2 {
			t.Fatalf("page holds %d observations, above the requested limit",
				len(page.Observations))
		}
		for _, o := range page.Observations {
			seen = append(seen, o.Sequence)
		}
		if page.NextAfter == "" {
			break
		}
		path = "/v1/evaluation-runs/run-1/observations?limit=2&after=" + page.NextAfter
	}

	if len(seen) != total {
		t.Fatalf("paged over %v, want %d observations", seen, total)
	}
	for i, sequence := range seen {
		if sequence != fmt.Sprintf("%d", i+1) {
			t.Fatalf("page order is %v, want ascending sequences", seen)
		}
	}
}

// A run that retained nothing is an empty page with an honest state; a run
// that does not exist is a 404.
func TestObservationRouteDistinguishesEmptyFromMissing(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")

	empty := a.do("GET", "/v1/evaluation-runs/run-1/observations", nil)
	a.mustStatus(empty, 200, "observations for a run with no records")
	page := decodeObservationPage(t, empty)
	if len(page.Observations) != 0 {
		t.Errorf("returned %d observations for a run that ingested nothing",
			len(page.Observations))
	}
	if page.HistoryState != "complete" {
		t.Errorf("history_state = %q, want complete for a run that ingested nothing",
			page.HistoryState)
	}

	missing := a.do("GET", "/v1/evaluation-runs/no-such-run/observations", nil)
	a.mustStatus(missing, 404, "observations for an unknown run")
}

// The page bound and the cursor are enforced at the edge.
func TestObservationRouteRefusesUnusablePageRequests(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")

	for _, query := range []string{
		"?limit=0",
		"?limit=65",
		"?limit=-1",
		"?limit=abc",
		"?after=abc",
		"?after=01",
		"?after=0",
	} {
		t.Run(query, func(t *testing.T) {
			response := a.do("GET", "/v1/evaluation-runs/run-1/observations"+query, nil)
			if response.Code != 400 {
				t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
			}
			if code := errorCode(t, response); code == "" {
				t.Error("error envelope carries no code")
			}
		})
	}
}

// A measured zero and an unavailable duration stay distinguishable on the
// wire: "0" is present, absent is absent.
func TestObservationRouteDistinguishesMeasuredZeroFromUnavailable(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")

	zero := observationRecord("e1", "fp-1", "tool_1")
	zero.DurationNanos = "0"
	unavailable := observationRecord("e2", "fp-2", "tool_2")
	unavailable.DurationNanos = ""
	unavailable.SpanStatus = event.StatusUnavailable

	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelope(1, zero)), 200, "ingest measured zero")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelope(2, unavailable)), 200, "ingest unavailable")

	response := a.do("GET", "/v1/evaluation-runs/run-1/observations", nil)
	a.mustStatus(response, 200, "observations")

	// Checked on the raw body as well as the decoded one: omitempty is what
	// carries the distinction, and a decoded struct renders both as "".
	raw := response.Body.String()
	if !strings.Contains(raw, `"duration_nanos":"0"`) {
		t.Errorf("a measured zero is not published as \"0\":\n%s", raw)
	}

	page := decodeObservationPage(t, response)
	if len(page.Observations) != 2 {
		t.Fatalf("returned %d observations, want 2", len(page.Observations))
	}
	if page.Observations[0].DurationNanos != "0" {
		t.Errorf("measured zero published as %q, want \"0\"",
			page.Observations[0].DurationNanos)
	}
	if page.Observations[1].DurationNanos != "" {
		t.Errorf("unavailable duration published as %q, want absent",
			page.Observations[1].DurationNanos)
	}
	if page.Observations[1].SpanStatus != "" {
		t.Errorf("unavailable status published as %q, want absent",
			page.Observations[1].SpanStatus)
	}
}
