package httpapi_test

// The correlated history route, task 076.
//
// Three optional, mutually exclusive narrowings on the observation route the
// explorer's session, trace and behavior-detail views read. The assertions are
// the ones a narrowing applied in the wrong place would pass: that a bounded
// page holds that many matches, that the continuation cursor describes the
// narrowed stream, and that the payload says which narrowing produced it.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"

	trustvian "github.com/trustvian/trustvian"
)

type correlatedPageBody struct {
	RunID string `json:"run_id"`
	Scope *struct {
		SessionID     string `json:"session_id"`
		TraceID       string `json:"trace_id"`
		FingerprintID string `json:"fingerprint_id"`
	} `json:"scope"`
	HistoryState string `json:"history_state"`
	NextAfter    string `json:"next_after"`
	Observations []struct {
		Sequence      string `json:"sequence"`
		SessionID     string `json:"session_id"`
		TraceID       string `json:"trace_id"`
		FingerprintID string `json:"fingerprint_id"`
	} `json:"observations"`
}

func decodeCorrelatedPage(t *testing.T, r *httptest.ResponseRecorder) correlatedPageBody {
	t.Helper()
	var body correlatedPageBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode correlated page: %v (%s)", err, r.Body.String())
	}
	return body
}

// correlatedRecordFor builds a record whose correlation and identity agree.
func correlatedRecordFor(
	eventID, session, trace, fingerprint string,
) trustvian.DecisionRecord {
	record := apiRecord(eventID, fingerprint, "tool_"+fingerprint)
	record.SessionID = session
	record.TraceID = trace
	record.SpanID = "span-" + eventID
	return record
}

// seedCorrelated ingests eight observations alternating between two sessions,
// two traces and two behaviors.
func seedCorrelated(a *api, runID string) {
	a.t.Helper()
	for i := 1; i <= 8; i++ {
		session, trace, fingerprint := "session-odd", "trace-odd", "odd"
		if i%2 == 0 {
			session, trace, fingerprint = "session-even", "trace-even", "even"
		}
		record := correlatedRecordFor(fmt.Sprintf("e%d", i), session, trace, fingerprint)
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/records",
			envelope(uint64(i), record)), 200, "ingest")
	}
}

// correlatedPath builds the route with one narrowing and a page bound.
func correlatedPath(runID, param, value string, limit int) string {
	query := url.Values{}
	if param != "" {
		query.Set(param, value)
	}
	query.Set("limit", fmt.Sprintf("%d", limit))
	return "/v1/evaluation-runs/" + runID + "/observations?" + query.Encode()
}

// Each narrowing returns only its own rows, in ingest order, and the payload
// echoes which narrowing produced them.
func TestCorrelatedRouteNarrowsAndSaysSo(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	seedCorrelated(a, "run-1")

	for _, tc := range []struct {
		param, value string
		wantSequence []string
	}{
		{"session_id", "session-odd", []string{"1", "3", "5", "7"}},
		{"trace_id", "trace-even", []string{"2", "4", "6", "8"}},
		{"fingerprint_id", "odd", []string{"1", "3", "5", "7"}},
	} {
		t.Run(tc.param, func(t *testing.T) {
			response := a.do("GET", correlatedPath("run-1", tc.param, tc.value, 64), nil)
			a.mustStatus(response, 200, "correlated observations")
			page := decodeCorrelatedPage(t, response)

			if page.Scope == nil {
				t.Fatal("scope is absent; a narrowed page must say what narrowed it")
			}
			if len(page.Observations) != len(tc.wantSequence) {
				t.Fatalf("returned %d observations, want %d",
					len(page.Observations), len(tc.wantSequence))
			}
			for i, want := range tc.wantSequence {
				if page.Observations[i].Sequence != want {
					t.Errorf("observation %d has sequence %q, want %q",
						i, page.Observations[i].Sequence, want)
				}
			}
			if page.NextAfter != "" {
				t.Errorf("next_after = %q on a page holding every match", page.NextAfter)
			}
		})
	}
}

// An unnarrowed read carries no scope at all, so a reader cannot mistake the
// run's whole history for a correlated view of it.
func TestUnnarrowedRouteCarriesNoScope(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	seedCorrelated(a, "run-1")

	response := a.do("GET", "/v1/evaluation-runs/run-1/observations", nil)
	a.mustStatus(response, 200, "observations")
	page := decodeCorrelatedPage(t, response)

	if page.Scope != nil {
		t.Errorf("scope = %+v on an unnarrowed read, want absent", page.Scope)
	}
	if len(page.Observations) != 8 {
		t.Errorf("returned %d observations, want the run's whole history of 8",
			len(page.Observations))
	}
}

// The narrowing is applied before the limit, and the continuation cursor
// describes the narrowed stream rather than the unfiltered one.
func TestCorrelatedRoutePagesTheNarrowedStream(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	seedCorrelated(a, "run-1")

	var seen []string
	after := ""
	for pages := 0; pages < 8; pages++ {
		path := correlatedPath("run-1", "session_id", "session-odd", 2)
		if after != "" {
			path += "&after=" + after
		}
		response := a.do("GET", path, nil)
		a.mustStatus(response, 200, "correlated page")
		page := decodeCorrelatedPage(t, response)

		// A full page of matches, not a page of rows some of which matched.
		if page.NextAfter != "" && len(page.Observations) != 2 {
			t.Fatalf("page holds %d observations beside a continuation cursor",
				len(page.Observations))
		}
		for _, o := range page.Observations {
			if o.SessionID != "session-odd" {
				t.Errorf("sequence %s carries session %q", o.Sequence, o.SessionID)
			}
			seen = append(seen, o.Sequence)
		}
		if page.NextAfter == "" {
			break
		}
		after = page.NextAfter
	}

	want := []string{"1", "3", "5", "7"}
	if len(seen) != len(want) {
		t.Fatalf("paging the narrowed stream saw %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("paging the narrowed stream saw %v, want %v", seen, want)
		}
	}
}

// More than one narrowing is refused with a diagnostic rather than answered.
func TestCorrelatedRouteRefusesMoreThanOneNarrowing(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	seedCorrelated(a, "run-1")

	response := a.do("GET",
		"/v1/evaluation-runs/run-1/observations?session_id=session-odd&trace_id=trace-odd", nil)
	a.mustStatus(response, 400, "two narrowings")
	if code := errorCode(t, response); code != "invalid_request" {
		t.Errorf("error code = %q, want invalid_request", code)
	}
}

// A correlation that retained nothing is an empty page whose history state says
// what is known — never an error, and never a fabricated absence.
func TestCorrelatedRouteReportsAnAbsentCorrelationHonestly(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	seedCorrelated(a, "run-1")

	response := a.do("GET", correlatedPath("run-1", "session_id", "session-absent", 64), nil)
	a.mustStatus(response, 200, "absent session")
	page := decodeCorrelatedPage(t, response)

	if len(page.Observations) != 0 {
		t.Errorf("returned %d observations for an absent session", len(page.Observations))
	}
	if page.HistoryState != "complete" {
		t.Errorf("history_state = %q, want complete", page.HistoryState)
	}
}

// One session identifier in two runs resolves separately in each.
func TestCorrelatedRouteIsScopedToOneRun(t *testing.T) {
	a := newAPI(t)
	a.seedRunning("run-1")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-2", "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run-2")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-2/start", nil), 200, "start run-2")

	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelope(1, correlatedRecordFor("a1", "session-shared", "trace-shared", "one"))),
		200, "ingest run-1")
	for i := 1; i <= 2; i++ {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-2/records",
			envelope(uint64(i),
				correlatedRecordFor(fmt.Sprintf("b%d", i), "session-shared", "trace-shared", "one"))),
			200, "ingest run-2")
	}

	for runID, want := range map[string]int{"run-1": 1, "run-2": 2} {
		response := a.do("GET", correlatedPath(runID, "session_id", "session-shared", 64), nil)
		a.mustStatus(response, 200, "correlated observations")
		page := decodeCorrelatedPage(t, response)
		if len(page.Observations) != want {
			t.Errorf("%s returned %d observations, want %d",
				runID, len(page.Observations), want)
		}
	}
}
