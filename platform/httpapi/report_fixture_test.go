package httpapi_test

// Report fixtures (task 107-B): whole scenario executions recorded through the
// real routes, each repetition an isolated run carrying the operational,
// fidelity and layer facts its envelope states. The golden version 2
// documents are these executions' reports.

import (
	"fmt"
	"maps"

	"github.com/trustvian/trustvian/event"
)

// reportStep is one record of a repetition. Unset fields keep apiRecord's
// tool operation on build-host.
type reportStep struct {
	op             string
	category       event.OperationCategory
	target         string
	targetCategory event.TargetCategory
	duration       string
	status         event.SpanStatus
	// envelope holds the envelope's optional facts: http_status_code,
	// tokens_*, fidelity, behavior_layer.
	envelope map[string]string
}

// reportFixture is one execution: its begin body's extra members, each
// side's repetitions, and the limits it completes under.
type reportFixture struct {
	id        string
	begin     map[string]any
	reference [][]reportStep
	candidate [][]reportStep
	limits    map[string]any
}

// fingerprintOf names a step's behavior: one fingerprint per (category,
// operation, target), as the engine's would be.
func fingerprintOf(s reportStep) string {
	return fmt.Sprintf("fp-%s-%s@%s", s.category, s.op, s.target)
}

func (s reportStep) withDefaults() reportStep {
	if s.category == "" {
		s.category = event.OperationCategoryTool
	}
	if s.target == "" {
		s.target = "build-host"
	}
	if s.targetCategory == "" {
		s.targetCategory = event.TargetCategoryExternal
	}
	return s
}

// recordRepetition creates, fills and completes one isolated run.
func (a *api) recordRepetition(runID string, steps []reportStep) {
	a.t.Helper()
	profile := runID + "-profile"
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": profile,
	}), 201, "create run "+runID)
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start "+runID)
	for i, raw := range steps {
		s := raw.withDefaults()
		record := apiRecord(fmt.Sprintf("%s-e%d", runID, i), fingerprintOf(s), s.op)
		record.Behavior.OperationCategory = s.category
		record.Behavior.TargetName = s.target
		record.Behavior.TargetCategory = s.targetCategory
		record.DurationNanos, record.SpanStatus = s.duration, s.status
		body := envelope(uint64(i+1), record)
		body["behavioral_profile"] = profile
		for k, v := range s.envelope {
			body[k] = v
		}
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/records", body), 200, "ingest "+runID)
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/complete", nil), 200, "complete "+runID)
}

// recordExecution begins, runs and completes the fixture's execution, and
// returns each side's run ids.
func (a *api) recordExecution(f reportFixture) (reference, candidate []string) {
	a.t.Helper()
	a.seedHierarchy()
	begin := beginBody(f.id, len(f.candidate), nil)
	maps.Copy(begin, f.begin)
	a.mustStatus(a.do("POST", "/v1/scenario-executions", begin), 201, "begin "+f.id)
	body := map[string]any{"gate_limits": f.limits}
	for _, side := range []struct {
		name  string
		steps [][]reportStep
		ids   *[]string
	}{{"reference", f.reference, &reference}, {"candidate", f.candidate, &candidate}} {
		for i, steps := range side.steps {
			run := fmt.Sprintf("%s-%s-%d", f.id, side.name, i+1)
			a.recordRepetition(run, steps)
			*side.ids = append(*side.ids, run)
		}
		body[side.name+"_run_ids"] = *side.ids
	}
	a.mustStatus(a.do("POST", "/v1/scenario-executions/"+f.id+"/complete", body), 200, "complete "+f.id)
	return reference, candidate
}
