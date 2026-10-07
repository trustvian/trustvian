package evaluation

import "github.com/trustvian/trustvian/event"

// Annotations are the adapter facts that ride beside a record on the ingest
// envelope. None is engine evidence and none is on DecisionRecord: each says
// how the record was produced, or what its span reported, and the engine has
// no opinion about any of them.
//
// Every zero value means "not stated", and is sent as an absent field:
//
//	Fidelity        zero is read by the control plane as transport (task 075)
//	Layer           zero is "not classified", never transport (task 083)
//	HTTPStatusCode  0 means no valid status code; a valid one is 100-599
//	Usage           each part's Has flag says whether it was stated (task 087)
type Annotations struct {
	Fidelity       event.Fidelity
	Layer          event.Layer
	HTTPStatusCode uint16
	Usage          event.Usage
}
