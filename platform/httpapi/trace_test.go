package httpapi_test

// The /v1 run trace collection, task 100.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trustvian/trustvian/event"
)

type tracePageBody struct {
	Version       string `json:"version"`
	RunID         string `json:"run_id"`
	HistoryState  string `json:"history_state"`
	RetainedCount string `json:"retained_count"`
	Complete      bool   `json:"complete"`
	NextAfter     string `json:"next_after"`
	Traces        []struct {
		TraceID       string `json:"trace_id"`
		Observations  string `json:"observations"`
		FirstSequence string `json:"first_sequence"`
		LastSequence  string `json:"last_sequence"`
		ErrorSpans    string `json:"error_spans"`
	} `json:"traces"`
}

func decodeTracePage(t *testing.T, r *httptest.ResponseRecorder) tracePageBody {
	t.Helper()
	var body tracePageBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode trace page: %v (%s)", err, r.Body.String())
	}
	return body
}

// seedTraces ingests five observations across three traces and one untraced
// action: t-1 (seq 1, 4), t-2 (seq 2, error), none (seq 3), t-3 (seq 5).
func seedTraces(a *api) {
	plan := []struct {
		trace  string
		status event.SpanStatus
	}{
		{"t-1", event.StatusOK}, {"t-2", event.StatusError}, {"", event.StatusOK},
		{"t-1", event.StatusError}, {"t-3", event.StatusUnset},
	}
	for i, step := range plan {
		record := observationRecord(fmt.Sprintf("e%d", i+1), fmt.Sprintf("fp-%d", i+1), fmt.Sprintf("tool_%d", i+1))
		record.TraceID = step.trace
		record.SpanStatus = step.status
		if step.trace == "" {
			record.SpanID = ""
			record.ParentSpanID = ""
			record.SpanLineage = ""
		}
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
			envelope(uint64(i+1), record)), 200, "ingest")
	}
}

func TestTraceRouteListsRetainedTracesInFirstAppearanceOrder(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	seedTraces(a)

	response := a.do("GET", "/v1/evaluation-runs/run-1/traces", nil)
	a.mustStatus(response, 200, "traces")
	page := decodeTracePage(t, response)

	if page.Version != "1" || page.RunID != "run-1" {
		t.Errorf("envelope = %q/%q", page.Version, page.RunID)
	}
	if page.HistoryState != "complete" || !page.Complete || page.RetainedCount != "5" {
		t.Errorf("history = %q complete=%v retained=%q", page.HistoryState, page.Complete, page.RetainedCount)
	}
	if page.NextAfter != "" {
		t.Errorf("next_after = %q with nothing following", page.NextAfter)
	}
	type row struct{ id, n, first, last, errs string }
	want := []row{{"t-1", "2", "1", "4", "1"}, {"t-2", "1", "2", "2", "1"}, {"t-3", "1", "5", "5", "0"}}
	if len(page.Traces) != len(want) {
		t.Fatalf("traces = %+v, want %d", page.Traces, len(want))
	}
	for i, w := range want {
		got := page.Traces[i]
		if got.TraceID != w.id || got.Observations != w.n || got.FirstSequence != w.first ||
			got.LastSequence != w.last || got.ErrorSpans != w.errs {
			t.Errorf("trace %d = %+v, want %+v", i, got, w)
		}
	}
	// Counters are decimal strings, like every uint64 on the wire.
	if !strings.Contains(response.Body.String(), `"observations":"2"`) {
		t.Errorf("counters are not encoded as strings: %s", response.Body.String())
	}
}

func TestTraceRoutePagesExactly(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	seedTraces(a)

	var seen []string
	path := "/v1/evaluation-runs/run-1/traces?limit=2"
	for range 5 {
		response := a.do("GET", path, nil)
		a.mustStatus(response, 200, "traces page")
		page := decodeTracePage(t, response)
		for _, trace := range page.Traces {
			seen = append(seen, trace.TraceID)
		}
		if page.NextAfter == "" {
			break
		}
		path = "/v1/evaluation-runs/run-1/traces?limit=2&after=" + page.NextAfter
	}
	if strings.Join(seen, ",") != "t-1,t-2,t-3" {
		t.Errorf("paged traces = %v, want [t-1 t-2 t-3] once each", seen)
	}

	// A full last page publishes no cursor when nothing follows it.
	response := a.do("GET", "/v1/evaluation-runs/run-1/traces?limit=3", nil)
	if page := decodeTracePage(t, response); page.NextAfter != "" || len(page.Traces) != 3 {
		t.Errorf("exactly-full page: %d traces, next_after %q", len(page.Traces), page.NextAfter)
	}
}

func TestTraceRouteRefusesBadInputAndUnknownRuns(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/v1/evaluation-runs/run-1/traces?limit=0", 400},
		{"/v1/evaluation-runs/run-1/traces?limit=65", 400},
		{"/v1/evaluation-runs/run-1/traces?limit=x", 400},
		{"/v1/evaluation-runs/run-1/traces?after=01", 400},
		{"/v1/evaluation-runs/run-1/traces?after=0", 400},
		{"/v1/evaluation-runs/missing/traces", 404},
	} {
		a.mustStatus(a.do("GET", tc.path, nil), tc.want, tc.path)
	}

	response := a.do("GET", "/v1/evaluation-runs/run-1/traces", nil)
	a.mustStatus(response, 200, "empty run")
	page := decodeTracePage(t, response)
	if len(page.Traces) != 0 || !strings.Contains(response.Body.String(), `"traces":[]`) {
		t.Errorf("an empty run must publish an empty list, not null: %s", response.Body.String())
	}
}
