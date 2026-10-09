package httpapi

// Task 081's per-side fidelity on comparison behaviors.

import platform "trustvian-platform"

// behaviorFidelityDTO is one side's fidelity for one behavior: the level the
// control plane's disagreement rule reports, whether the observations
// disagreed, the three fidelity counts that decided it, and the layer counts
// beside them. Counts are canonical decimal strings, like every counter here.
type behaviorFidelityDTO struct {
	Level      string        `json:"level"`
	Mixed      bool          `json:"mixed"`
	Semantic   string        `json:"semantic"`
	Transport  string        `json:"transport"`
	Unrecorded string        `json:"unrecorded"`
	Layer      layerCountDTO `json:"layer"`
}

type layerCountDTO struct {
	Model        string `json:"model"`
	Tool         string `json:"tool"`
	Retrieval    string `json:"retrieval"`
	Transport    string `json:"transport"`
	Unclassified string `json:"unclassified"`
	Unrecorded   string `json:"unrecorded"`
}

// newBehaviorFidelityDTO renders one side, or nothing for a side with no
// observations of the behavior: a side that never saw it has no fidelity to
// report, and "unrecorded" would claim an observation that did not happen.
func newBehaviorFidelityDTO(b platform.BehaviorFidelity, observations uint64) *behaviorFidelityDTO {
	if observations == 0 {
		return nil
	}
	level, mixed := b.Reported()
	return &behaviorFidelityDTO{
		Level: string(level), Mixed: mixed,
		Semantic: u64(b.Semantic), Transport: u64(b.Transport), Unrecorded: u64(b.Unrecorded),
		Layer: layerCountDTO{
			Model: u64(b.LayerModel), Tool: u64(b.LayerTool), Retrieval: u64(b.LayerRetrieval),
			Transport: u64(b.LayerTransport), Unclassified: u64(b.LayerUnclassified),
			Unrecorded: u64(b.LayerUnrecorded),
		},
	}
}
