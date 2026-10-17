package httpapi_test

// Task 109: the actor on an observation frame.
//
// The frame names who performed the observation so a live view can tell the
// actors of one run apart. It is display metadata copied from the ingested
// record, held to the frame's bounds, and never part of behavioral identity.

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

// actorFields decodes the observation's actor fields, recording which keys
// were present: "always present" is part of the contract.
type actorFields struct {
	present       map[string]bool
	ActorID       string
	ActorIDState  string
	ActorType     string
	FingerprintID string
	Behavior      json.RawMessage
}

func decodeActorFields(t *testing.T, frame sseFrame) actorFields {
	t.Helper()
	var payload struct {
		Observation map[string]json.RawMessage `json:"observation"`
	}
	if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
		t.Fatalf("decode %s: %v", frame.Data, err)
	}
	if payload.Observation == nil {
		t.Fatalf("frame %q carries no observation: %s", frame.Event, frame.Data)
	}
	out := actorFields{present: map[string]bool{}, Behavior: payload.Observation["behavior"]}
	for key, target := range map[string]*string{
		"actor_id": &out.ActorID, "actor_id_state": &out.ActorIDState,
		"actor_type": &out.ActorType, "fingerprint_id": &out.FingerprintID,
	} {
		raw, ok := payload.Observation[key]
		out.present[key] = ok
		if ok {
			if err := json.Unmarshal(raw, target); err != nil {
				t.Fatalf("decode %s: %v", key, err)
			}
		}
	}
	return out
}

// retainedActorIDs reads the run's retained observations, oldest first.
func (a *realtimeAPI) retainedActorIDs(runID string) []string {
	a.t.Helper()
	response, err := a.client.Get(a.server.URL + "/v1/evaluation-runs/" + runID + "/observations")
	if err != nil {
		a.t.Fatalf("GET observations: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 {
		a.t.Fatalf("GET observations status = %d: %s", response.StatusCode, body)
	}
	var page struct {
		Observations []struct {
			Sequence string `json:"sequence"`
			ActorID  string `json:"actor_id"`
		} `json:"observations"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		a.t.Fatalf("decode observations: %v (%s)", err, body)
	}
	bySequence := map[string]string{}
	for _, o := range page.Observations {
		bySequence[o.Sequence] = o.ActorID
	}
	out := make([]string, 0, len(page.Observations))
	for i := 1; i <= len(page.Observations); i++ {
		out = append(out, bySequence[platform.FormatSequence(uint64(i))])
	}
	return out
}

func TestSSEObservationCarriesTheRecordsActor(t *testing.T) {
	atBound := strings.Repeat("a", 256)
	overBound := strings.Repeat("b", 257)

	tests := []struct {
		name      string
		actorID   string
		actorType event.ActorType
		wantID    string
		wantState string
		wantType  string
	}{
		{"an ordinary id", "billing-agent", event.ActorTypeService, "billing-agent", "carried", "service"},
		{"exactly 256 bytes is carried", atBound, event.ActorTypeAIAgent, atBound, "carried", "ai_agent"},
		{"257 bytes is not carried, never truncated", overBound, event.ActorTypeAIAgent, "", "exceeds_frame_bound", "ai_agent"},
		// Ingest does not check the record's own actor type, so a value that
		// is not a recognized type is dropped rather than copied at any length.
		{"an unrecognized actor type is not carried", "billing-agent", event.ActorType(strings.Repeat("x", 5000)), "billing-agent", "carried", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newRealtimeAPI(t)
			a.seedRunning("run-1", "cand-1")
			s := a.connect("?run_id=run-1", nil)
			defer s.close()
			decodeReady(t, s.next())

			record := apiRecord("evt-0", "fp-0", "read")
			record.ActorID = tt.actorID
			record.ActorType = tt.actorType
			a.ingest("run-1", 1, record)

			frame := s.next()
			got := decodeActorFields(t, frame)
			for _, key := range []string{"actor_id", "actor_id_state", "actor_type"} {
				if !got.present[key] {
					t.Errorf("%s is absent; it is always present on an observation frame", key)
				}
			}
			if got.ActorID != tt.wantID || got.ActorIDState != tt.wantState || got.ActorType != tt.wantType {
				t.Errorf("actor = (%d bytes, %q, %d bytes), want (%d bytes, %q, %q)",
					len(got.ActorID), got.ActorIDState, len(got.ActorType),
					len(tt.wantID), tt.wantState, tt.wantType)
			}
			// The bound is on the frame, not on what the run retains.
			if retained := a.retainedActorIDs("run-1"); len(retained) != 1 || retained[0] != tt.actorID {
				t.Errorf("retained actor id is %d bytes, want the record's %d",
					len(strings.Join(retained, "")), len(tt.actorID))
			}
		})
	}
}

func TestSSELifecycleAndStatusFramesCarryNoActor(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")
	s := a.connect("", nil)
	defer s.close()
	decodeReady(t, s.next())

	a.bus.Publish(platform.RealtimeEvent{
		Kind:   platform.RealtimeStatusChanged,
		Status: platform.RealtimeStatusChange{CollectorID: "dev"},
	})
	a.mustPost("/v1/evaluation-runs/run-1/complete", nil, 200)

	for _, want := range []string{"status_changed", "evaluation_completed"} {
		frame := s.next()
		if frame.Event != want {
			t.Fatalf("event = %q, want %q", frame.Event, want)
		}
		if strings.Contains(frame.Data, `"actor_`) {
			t.Errorf("%s frame carries an actor field: %s", want, frame.Data)
		}
	}
}

// The same behavior from two actors is one behavior: one fingerprint and one
// StableFeatures value, from the real engine. The frames differ only in the
// actor, which is reported, not keyed on.
func TestTwoActorsOneBehaviorShareIdentityOnTheFrame(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")
	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())

	engine := trustvian.NewEngine(trustvian.WithLearningScope(testProfile))
	var records []trustvian.DecisionRecord
	for i, actor := range []string{"support-agent", "billing-agent"} {
		result, err := engine.Analyze(t.Context(), event.Event{
			ID:        "evt-" + actor,
			Timestamp: testEpoch.Add(time.Duration(i+1) * time.Minute),
			Actor:     event.Actor{ID: actor, Type: event.ActorTypeService, IdentityConfidence: 0.9},
			Operation: event.Operation{Category: event.OperationCategoryTool, Name: "knowledge_search"},
			Target:    event.Target{Name: "kb.localhost", Category: event.TargetCategoryExternal},
			Context:   event.Context{Environment: testEnvironment},
		})
		if err != nil {
			t.Fatalf("Analyze(%s) error = %v", actor, err)
		}
		records = append(records, result.DecisionRecord())
	}
	if records[0].FingerprintID != records[1].FingerprintID || records[0].Behavior != records[1].Behavior {
		t.Fatalf("the engine gave two actors' identical behavior two identities: %+v / %+v",
			records[0].Behavior, records[1].Behavior)
	}

	var frames []actorFields
	for i, record := range records {
		a.ingest("run-1", uint64(i+1), record)
		frames = append(frames, decodeActorFields(t, s.next()))
	}
	if frames[0].FingerprintID != frames[1].FingerprintID || string(frames[0].Behavior) != string(frames[1].Behavior) {
		t.Errorf("frames disagree on identity: %s %s / %s %s",
			frames[0].FingerprintID, frames[0].Behavior, frames[1].FingerprintID, frames[1].Behavior)
	}
	if frames[0].ActorID != "support-agent" || frames[1].ActorID != "billing-agent" {
		t.Errorf("actors = %q, %q", frames[0].ActorID, frames[1].ActorID)
	}
	// The actor is not in the behavior: StableFeatures has no actor id.
	for _, f := range frames {
		if strings.Contains(string(f.Behavior), f.ActorID) {
			t.Errorf("behavior carries the actor id: %s", f.Behavior)
		}
	}
}
