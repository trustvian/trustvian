package httpapi_test

// Correlation-aware counting over the wire (task 083, ADR 0052).
//
// The fold is authoritative in the control plane and adapters only render it,
// so these assert that /v1/evaluations/compare carries the server's result
// for the two shapes #132's review found the fold getting wrong: an identity
// observed in mixed contexts, and a cycle somewhere in the added graph.

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	trustvian "github.com/trustvian/trustvian"
	platform "trustvian-platform"
)

type countingCompareBody struct {
	Diff struct {
		AddedCount            int    `json:"added_count"`
		AddedChangeCount      int    `json:"added_change_count"`
		CorrelationState      string `json:"correlation_state"`
		CountingPolicyVersion string `json:"counting_policy_version"`
		AddedChanges          []struct {
			RootFingerprintID          string   `json:"root_fingerprint_id"`
			ContributingFingerprintIDs []string `json:"contributing_fingerprint_ids"`
		} `json:"added_changes"`
	} `json:"behavior_diff"`
}

// spanRecord builds a record with span identity and a distinct descriptor.
func spanRecord(eventID, fingerprint, operation, trace, span, parent string) trustvian.DecisionRecord {
	record := apiRecord(eventID, fingerprint, operation)
	record.TraceID = trace
	record.SpanID = span
	record.ParentSpanID = parent
	if parent == "" {
		record.SpanLineage = "root"
	} else {
		record.SpanLineage = "child"
	}
	return record
}

func (a *api) completeSpanRun(runID, candidateID string, records []trustvian.DecisionRecord) {
	a.t.Helper()
	a.seedHierarchy()
	if candidateID != "cand-1" {
		a.mustStatus(a.do("POST", "/v1/candidates", map[string]any{
			"id": candidateID, "agent_id": "agent-1", "metadata": map[string]string{"label": candidateID},
		}), 201, "create candidate")
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": candidateID,
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start")
	for i, record := range records {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/records",
			envelope(uint64(i+1), record)), 200, fmt.Sprintf("ingest %s/%d", runID, i+1))
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/complete", nil), 200, "complete")
}

func (a *api) compareCounting(reference, candidate string) countingCompareBody {
	a.t.Helper()
	maxUint := platform.FormatSequence(math.MaxUint64)
	r := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": reference,
		"candidate_run_id": candidate,
		"gate_limits":      limitsBody(maxUint, maxUint, maxUint),
	})
	a.mustStatus(r, 200, "compare")
	var body countingCompareBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		a.t.Fatalf("decode compare: %v (%s)", err, r.Body.String())
	}
	return body
}

func (b countingCompareBody) shapes() []string {
	out := make([]string, 0, len(b.Diff.AddedChanges))
	for _, c := range b.Diff.AddedChanges {
		out = append(out, c.RootFingerprintID+"<-"+strings.Join(c.ContributingFingerprintIDs, ","))
	}
	return out
}

// A known tool reaching a new destination that a new tool also reaches is two
// changes on the wire, not one.
func TestCompareKeepsAKnownToolsNewDestinationCountedBesideANewTool(t *testing.T) {
	a := newAPI(t)
	a.completeSpanRun("run-ref", "cand-1", []trustvian.DecisionRecord{
		spanRecord("ref-1", "fp-known-tool", "export_customer", "trace-r", "span-known", ""),
		spanRecord("ref-2", "fp-http-old", "post_old", "trace-r", "span-known-http", "span-known"),
	})
	a.completeSpanRun("run-can", "cand-2", []trustvian.DecisionRecord{
		spanRecord("can-1", "fp-known-tool", "export_customer", "trace-known", "span-known", ""),
		spanRecord("can-2", "fp-http-new", "post_new", "trace-known", "span-known-http", "span-known"),
		spanRecord("can-3", "fp-new-tool", "import_invoices", "trace-new", "span-new", ""),
		spanRecord("can-4", "fp-http-new", "post_new", "trace-new", "span-new-http", "span-new"),
	})

	body := a.compareCounting("run-ref", "run-can")
	if body.Diff.AddedCount != 2 {
		t.Fatalf("added_count = %d, want 2", body.Diff.AddedCount)
	}
	if body.Diff.AddedChangeCount != 2 {
		t.Errorf("added_change_count = %d, want 2; the known tool's new destination "+
			"must not be absorbed into the new tool's change", body.Diff.AddedChangeCount)
	}
	if body.Diff.CorrelationState != "complete" {
		t.Errorf("correlation_state = %q, want complete", body.Diff.CorrelationState)
	}
	if want := []string{
		"fp-http-new<-fp-http-new",
		"fp-new-tool<-fp-http-new,fp-new-tool",
	}; !slices.Equal(body.shapes(), want) {
		t.Errorf("added_changes = %v, want %v", body.shapes(), want)
	}
}

// A cycle beside a valid pair reports partial and the identity count, with
// every change contributing only itself.
func TestCompareReportsTheIdentityCountWhenTheAddedGraphHasACycle(t *testing.T) {
	a := newAPI(t)
	a.completeSpanRun("run-ref", "cand-1", []trustvian.DecisionRecord{
		spanRecord("ref-1", "fp-read", "read", "trace-r", "span-r", ""),
	})
	a.completeSpanRun("run-can", "cand-2", []trustvian.DecisionRecord{
		spanRecord("can-1", "fp-read", "read", "trace-0", "span-0", ""),
		spanRecord("can-2", "fp-a", "tool_a", "trace-1", "span-a", ""),
		spanRecord("can-3", "fp-b", "post_b", "trace-1", "span-b", "span-a"),
		spanRecord("can-4", "fp-c", "step_c", "trace-2", "span-c", "span-d"),
		spanRecord("can-5", "fp-d", "step_d", "trace-2", "span-d", "span-c"),
	})

	body := a.compareCounting("run-ref", "run-can")
	if body.Diff.CorrelationState != "partial" {
		t.Errorf("correlation_state = %q, want partial", body.Diff.CorrelationState)
	}
	if body.Diff.AddedCount != 4 || body.Diff.AddedChangeCount != 4 {
		t.Errorf("added_count=%d added_change_count=%d, want 4 and 4; partial "+
			"means the unfolded identity count",
			body.Diff.AddedCount, body.Diff.AddedChangeCount)
	}
	if want := []string{
		"fp-a<-fp-a", "fp-b<-fp-b", "fp-c<-fp-c", "fp-d<-fp-d",
	}; !slices.Equal(body.shapes(), want) {
		t.Errorf("added_changes = %v, want %v", body.shapes(), want)
	}
}
