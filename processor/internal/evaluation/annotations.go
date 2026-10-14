package evaluation

import "github.com/trustvian/trustvian/event"

// Annotations are the adapter facts that ride beside a record on the ingest
// envelope. None is engine evidence and none is on DecisionRecord: each says
// how the record was produced, or what its span reported, and the engine has
// no opinion about any of them.
//
// Every zero value means "not stated", and is sent as an absent field:
//
//	Fidelity        zero is not stated; see below (tasks 075, 081)
//	Layer           zero is "not classified", never transport (task 083)
//	HTTPStatusCode  0 means no valid status code; a valid one is 100-599
//	Usage           each part's Has flag says whether it was stated (task 087)
//
// An unstated fidelity is read two ways by the control plane: the record's
// realtime observation shows it as transport (OrTransport), and the behavior's
// persisted fidelity counts it unrecorded (platform/behavior_fidelity.go,
// pinned by TestUnstatedFidelityIsUnrecordedInEvidence). The two never
// disagree about a record this Collector sends, because none it applies has a
// zero Fidelity: annotationsOf fills it through OrTransport, so an unmapped or
// unrecognized value is sent as transport (pinned by
// TestAnnotationsNeverLeaveFidelityUnstated). The one request sent with zero
// Annotations is Sink's recovery re-presentation, which the control plane only
// replays or refuses by record digest and never counts. Change either reading
// and both pins, and this paragraph, need revisiting.
type Annotations struct {
	Fidelity       event.Fidelity
	Layer          event.Layer
	HTTPStatusCode uint16
	Usage          event.Usage
}
