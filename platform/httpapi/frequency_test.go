package httpapi_test

// Task 106 over /v1: the frequency fields on compare-repeated, and the
// byte-for-byte guarantee for everything that existed before them.

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

// withoutMember removes every `,"name":<value>` member from a compact JSON
// document, whatever the value's type, measuring the value with the decoder.
func withoutMember(t *testing.T, doc []byte, name string) []byte {
	t.Helper()
	marker := []byte(`,"` + name + `":`)
	for {
		start := bytes.Index(doc, marker)
		if start < 0 {
			return doc
		}
		valueAt := start + len(marker)
		dec := json.NewDecoder(bytes.NewReader(doc[valueAt:]))
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			t.Fatalf("member %s: %v", name, err)
		}
		doc = append(append([]byte{}, doc[:start]...), doc[valueAt+int(dec.InputOffset()):]...)
	}
}

// task106Members are the members 106 adds to compare-repeated.
var task106Members = []string{
	"reference_frequency", "candidate_frequency", "lost", "targets", "lost_transitions",
}

// TestCompareRepeatedAtNEqualsOneIsUnchanged is task 106's technical
// requirement 3: at N = 1 with none of the new limits, the response is the one
// the control plane returned before 106 (captured from main at 946a4fc), byte
// for byte, once the added reporting members are removed.
func TestCompareRepeatedAtNEqualsOneIsUnchanged(t *testing.T) {
	before, err := os.ReadFile(filepath.Join("testdata", "compare-repeated-n1-before-106.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(t)
	a.completeIsolatedRun("ref-1", []string{"read", "list", "list"})
	a.completeIsolatedRun("can-1", []string{"read", "send"})
	r := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
		"reference_run_ids": []string{"ref-1"}, "candidate_run_ids": []string{"can-1"},
		"gate_limits": repeatedLimitsBody("1", "0"),
	})
	a.mustStatus(r, 200, "compare-repeated")
	got := r.Body.Bytes()
	for _, member := range task106Members {
		if !bytes.Contains(got, []byte(`"`+member+`":`)) {
			t.Errorf("the response does not carry %s", member)
		}
		got = withoutMember(t, got, member)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(before)) {
		t.Fatalf("the pre-106 response changed:\n got %s\nwant %s", got, before)
	}
}

// TestFrequencyOnTheWire: per-behavior frequency and lost, and per-target
// totals with their ratio. The unavailable ratio is TestFrequencyFigures'.
func TestFrequencyOnTheWire(t *testing.T) {
	a := newAPI(t)
	a.completeIsolatedRun("ref-1", []string{"read", "send"})
	a.completeIsolatedRun("ref-2", []string{"read", "send"})
	a.completeIsolatedRun("can-1", []string{"read", "read", "read"})
	a.completeIsolatedRun("can-2", []string{"read", "send"})
	r := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
		"reference_run_ids": []string{"ref-1", "ref-2"}, "candidate_run_ids": []string{"can-1", "can-2"},
		"gate_limits": repeatedLimitsBody("2", "0"),
	})
	a.mustStatus(r, 200, "compare-repeated")
	var body struct {
		Behaviors []struct {
			FingerprintID      string            `json:"fingerprint_id"`
			ReferenceFrequency map[string]string `json:"reference_frequency"`
			CandidateFrequency map[string]string `json:"candidate_frequency"`
			Lost               bool              `json:"lost"`
		} `json:"behaviors"`
		Targets []struct {
			TargetName         string            `json:"target_name"`
			CandidateFrequency map[string]string `json:"candidate_frequency"`
			CallRatioAvailable bool              `json:"call_ratio_available"`
			CallRatioPermille  *string           `json:"call_ratio_permille"`
		} `json:"targets"`
		LostTransitions string `json:"lost_transitions"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		cand map[string]string
		lost bool
	}{
		"fp-read": {map[string]string{"runs": "2", "calls_total": "4", "calls_per_run_min": "1",
			"calls_per_run_max": "3", "calls_per_run_mean_milli": "2000"}, false},
		"fp-send": {map[string]string{"runs": "2", "calls_total": "1", "calls_per_run_min": "0",
			"calls_per_run_max": "1", "calls_per_run_mean_milli": "500"}, true},
	}
	for _, b := range body.Behaviors {
		w := want[b.FingerprintID]
		if !maps.Equal(b.CandidateFrequency, w.cand) || b.Lost != w.lost {
			t.Errorf("%s: candidate %v lost %v; want %v %v", b.FingerprintID, b.CandidateFrequency, b.Lost, w.cand, w.lost)
		}
	}
	// Every behavior targets build-host: 5 candidate calls over 4 reference.
	if len(body.Targets) != 1 || body.Targets[0].CallRatioPermille == nil ||
		*body.Targets[0].CallRatioPermille != "1250" || !body.Targets[0].CallRatioAvailable {
		t.Fatalf("targets = %+v", body.Targets)
	}
	if body.LostTransitions != "not_recorded" {
		t.Errorf("lost_transitions = %q", body.LostTransitions)
	}
}
