package httpapi_test

// The /v1 run session collection, task 103.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSessionRouteListsRetainedSessions(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	for i, session := range []string{"sess-b", "sess-a", "", "sess-b"} {
		record := observationRecord(fmt.Sprintf("e%d", i+1), fmt.Sprintf("fp-%d", i+1), fmt.Sprintf("tool_%d", i+1))
		record.SessionID = session
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records", envelope(uint64(i+1), record)), 200, "ingest")
	}

	response := a.do("GET", "/v1/evaluation-runs/run-1/sessions?limit=1", nil)
	a.mustStatus(response, 200, "sessions")
	var page struct {
		HistoryState string `json:"history_state"`
		NextAfter    string `json:"next_after"`
		Sessions     []struct {
			SessionID     string `json:"session_id"`
			Observations  string `json:"observations"`
			FirstSequence string `json:"first_sequence"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.HistoryState != "complete" || len(page.Sessions) != 1 || page.Sessions[0].SessionID != "sess-b" ||
		page.Sessions[0].Observations != "2" || page.NextAfter != "1" {
		t.Errorf("first page = %+v", page)
	}
	if strings.Contains(response.Body.String(), `"trace_id"`) {
		t.Error("the session list names its identifier trace_id")
	}

	rest := a.do("GET", "/v1/evaluation-runs/run-1/sessions?after=1", nil)
	if !strings.Contains(rest.Body.String(), `"session_id":"sess-a"`) || strings.Contains(rest.Body.String(), `"next_after"`) {
		t.Errorf("continuation = %s", rest.Body.String())
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/v1/evaluation-runs/missing/sessions", 404},
		{"/v1/evaluation-runs/run-1/sessions?after=01", 400},
		{"/v1/evaluation-runs/run-1/sessions?limit=0", 400},
	} {
		a.mustStatus(a.do("GET", tc.path, nil), tc.want, tc.path)
	}
}
