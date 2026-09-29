package httpapi_test

// Operational-evidence validation at the ingest boundary (task 084).
//
// The control plane refuses a malformed or out-of-range duration, and the
// refusal leaves the run exactly as it was: no evidence recorded, and the cursor
// still expecting the same sequence, so the submitter can retry with a correct
// record.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// runningAPI seeds a hierarchy and one started run on the general fixture,
// which supports both GET and POST — newRealtimeAPI only posts.
func runningAPI(t *testing.T) *api {
	t.Helper()
	a := newAPI(t)
	a.seedHierarchy()
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-1", "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/start", nil), 200, "start")
	return a
}

func durationRecord(duration string) trustvian.DecisionRecord {
	record := fidelityRecord("export_customer")
	record.DurationNanos = duration
	return record
}

func TestIngestRefusesInvalidDuration(t *testing.T) {
	maxOne := strconv.FormatUint(event.MaxDurationNanos, 10)

	tests := []struct {
		name     string
		duration string
		accepted bool
	}{
		{"absent is unavailable", "", true},
		{"a measured zero", "0", true},
		{"an ordinary duration", "1500000", true},
		{"exactly the maximum", maxOne, true},
		{"the maximum plus one", strconv.FormatUint(event.MaxDurationNanos+1, 10), false},
		{"MaxUint64", "18446744073709551615", false},
		{"negative", "-1", false},
		{"a float", "1.5", false},
		{"a leading zero", "0100", false},
		{"not a number", "abc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := runningAPI(t)
			response := a.do("POST", "/v1/evaluation-runs/run-1/records",
				envelope(1, durationRecord(tt.duration)))
			want := 400
			if tt.accepted {
				want = 200
			}
			if response.Code != want {
				t.Fatalf("status = %d, want %d for duration %q\n%s",
					response.Code, want, tt.duration, response.Body.String())
			}
		})
	}
}

// TestRefusedDurationLeavesEvidenceAndCursorUnchanged is the property that makes
// the refusal safe: nothing advanced, so the submitter retries the same
// sequence with a corrected record and the run proceeds.
func TestRefusedDurationLeavesEvidenceAndCursorUnchanged(t *testing.T) {
	a := runningAPI(t)

	before := a.do("GET", "/v1/evaluation-runs/run-1/progress", nil).Body.String()
	if !strings.Contains(before, `"record_count":"0"`) {
		t.Fatalf("the fixture is not empty:\n%s", before)
	}
	if !strings.Contains(before, `"next_ingest_sequence":"1"`) {
		t.Fatalf("the fixture cursor is not at 1:\n%s", before)
	}

	// An out-of-range duration at the expected sequence.
	response := a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelope(1, durationRecord("18446744073709551615")))
	if response.Code != 400 {
		t.Fatalf("status = %d, want 400\n%s", response.Code, response.Body.String())
	}

	after := a.do("GET", "/v1/evaluation-runs/run-1/progress", nil).Body.String()
	if !strings.Contains(after, `"record_count":"0"`) {
		t.Errorf("the refused record was counted:\n%s", after)
	}
	if !strings.Contains(after, `"distinct_behavior_count":0`) {
		t.Errorf("the refused record reached the behavior snapshot:\n%s", after)
	}
	if !strings.Contains(after, `"next_ingest_sequence":"1"`) {
		t.Errorf("the cursor advanced past a refused record:\n%s", after)
	}

	// And the same sequence still works, which is the point of not advancing.
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelope(1, durationRecord("1500000"))), 200, "retry at sequence 1")

	recovered := a.do("GET", "/v1/evaluation-runs/run-1/progress", nil).Body.String()
	if !strings.Contains(recovered, `"record_count":"1"`) {
		t.Errorf("the retry at sequence 1 was not recorded:\n%s", recovered)
	}
	if !strings.Contains(recovered, `"next_ingest_sequence":"2"`) {
		t.Errorf("the cursor did not advance after the valid record:\n%s", recovered)
	}
}

// TestRefusalNamesTheField keeps the diagnostic useful: a submitter has to be
// able to tell which field it got wrong.
func TestRefusalNamesTheField(t *testing.T) {
	a := runningAPI(t)
	response := a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelope(1, durationRecord("18446744073709551615")))
	if response.Code != 400 {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if body.Error.Code == "" {
		t.Error("the refusal carries no error code")
	}
	if !strings.Contains(strings.ToLower(body.Error.Message), "duration") {
		t.Errorf("the refusal does not name the field: %q", body.Error.Message)
	}
}
