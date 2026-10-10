package platform

// Persisted per-behavior fidelity and layer (task 081, ADR 0068).
//
// Task 075 put fidelity everywhere that needed no storage, and task 083 did the
// same for the behavior layer. Both ride on the ingest envelope beside the
// record. A comparison is built from persisted evidence, so neither reached
// one. This keeps them per behavior as counts.
//
// Counts rather than one value, because a fingerprint is computed from
// StableFeatures and neither fidelity nor layer is one of its dimensions: two
// observations of one behavior can disagree. Each observation is counted at the
// level its envelope stated, and Reported applies the disagreement rule — the
// lowest level seen — to those counts, here and only here.
//
// Neither is identity. Nothing here reaches StableFeatures, a fingerprint or a
// baseline key, and a test asserts it.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/trustvian/trustvian/event"
)

// BehaviorFidelity counts one behavior's observations by the fidelity and the
// layer their envelopes stated.
//
// Each group partitions the observations. Unrecorded is an observation whose
// envelope did not state the pair: the in-process SDK path, `trustvian eval
// ingest`, a client stating only one of the two fields, and every observation
// recorded before schema 13. A layer is claimed exactly
// when fidelity is semantic, so the two groups are tied:
//
//	LayerModel + LayerTool + LayerRetrieval + LayerUnclassified == Semantic
//	LayerTransport  == Transport
//	LayerUnrecorded == Unrecorded
//
// LayerUnclassified is a semantic observation whose envelope named no layer.
type BehaviorFidelity struct {
	Semantic   uint64
	Transport  uint64
	Unrecorded uint64

	LayerModel        uint64
	LayerTool         uint64
	LayerRetrieval    uint64
	LayerTransport    uint64
	LayerUnclassified uint64
	LayerUnrecorded   uint64
}

// FidelityLevel is a behavior's reported fidelity.
type FidelityLevel string

const (
	FidelityLevelSemantic   FidelityLevel = "semantic"
	FidelityLevelTransport  FidelityLevel = "transport"
	FidelityLevelUnrecorded FidelityLevel = "unrecorded"
)

// ErrInvalidFidelityPair reports an envelope whose fidelity and layer
// contradict each other: transport fidelity with a semantic layer, or semantic
// fidelity with the transport layer. No Trustvian producer sends one — the
// Collector states both or neither — and both cannot be true.
var ErrInvalidFidelityPair = errors.New("platform: fidelity and behavior layer disagree")

// validateFidelityPair refuses a contradictory pair, rather than counting it as
// unrecorded: that would let a submitter erase its own evidence by corrupting
// it (task 084's rule).
//
// A partial pair — fidelity without a layer, or a layer without a fidelity — is
// not refused. The envelope contract makes both fields independently optional,
// so it is valid input; it simply does not say enough to place the observation
// in both groups, and observe counts it unrecorded in each. Only the pairs the
// layer rule produces are counted as stated: transport with transport, and
// semantic with model, tool, retrieval or no layer.
func validateFidelityPair(f event.Fidelity, l event.Layer) error {
	contradictory := (f == event.FidelityTransport && (l == event.LayerModel || l == event.LayerTool ||
		l == event.LayerRetrieval)) || (f == event.FidelitySemantic && l == event.LayerTransport)
	if !contradictory {
		return nil
	}
	return fmt.Errorf("%w: fidelity %q with behavior_layer %q; transport fidelity goes with the transport "+
		"layer, and a semantic layer goes with semantic fidelity", ErrInvalidFidelityPair, f, l)
}

// observe counts one observation, or reports why it cannot, leaving the
// receiver unchanged. The pair must already be valid.
func (b BehaviorFidelity) observe(f event.Fidelity, l event.Layer) (BehaviorFidelity, error) {
	next := b
	// Unrecorded in both groups unless the pair is one the layer rule
	// produces: neither field, or only one of them, states the whole pair.
	fidelity, layer := &next.Unrecorded, &next.LayerUnrecorded
	switch {
	case f == event.FidelitySemantic:
		fidelity = &next.Semantic
		switch l {
		case event.LayerModel:
			layer = &next.LayerModel
		case event.LayerTool:
			layer = &next.LayerTool
		case event.LayerRetrieval:
			layer = &next.LayerRetrieval
		default:
			layer = &next.LayerUnclassified
		}
	case f == event.FidelityTransport && l == event.LayerTransport:
		fidelity, layer = &next.Transport, &next.LayerTransport
	}
	if err := addCount(fidelity, 1, "fidelity count"); err != nil {
		return b, err
	}
	if err := addCount(layer, 1, "layer count"); err != nil {
		return b, err
	}
	return next, nil
}

// unrecordedFidelity is what an entry recorded before schema 13 holds, and
// what the migration writes for it: every observation unrecorded in both
// groups.
func unrecordedFidelity(observations uint64) BehaviorFidelity {
	return BehaviorFidelity{Unrecorded: observations, LayerUnrecorded: observations}
}

// Reported applies the disagreement rule: transport if any observation was
// transport, else semantic if any was semantic, else unrecorded. Mixed is true
// when two or more fidelity counters are non-zero.
//
// The lowest level, because fidelity is a claim about evidence quality that
// must hold for every observation it covers: one transport observation means
// some of what this identity counted was inferred, and reporting semantic would
// overstate the evidence (TestFidelityNeverExceedsTheEvidence). A majority has
// no threshold anyone chose, and the latest observation depends on ingest
// order. The counts keep everything else.
func (b BehaviorFidelity) Reported() (FidelityLevel, bool) {
	nonZero := 0
	for _, n := range []uint64{b.Semantic, b.Transport, b.Unrecorded} {
		if n > 0 {
			nonZero++
		}
	}
	mixed := nonZero > 1
	switch {
	case b.Transport > 0:
		return FidelityLevelTransport, mixed
	case b.Semantic > 0:
		return FidelityLevelSemantic, mixed
	}
	return FidelityLevelUnrecorded, mixed
}

// Add is the counter-wise sum, for a side's total over its runs. The rule is
// applied to the sum, never to per-run results. Overflow is an error.
func (b BehaviorFidelity) Add(o BehaviorFidelity) (BehaviorFidelity, error) {
	out := b
	pairs := []struct {
		into *uint64
		add  uint64
	}{
		{&out.Semantic, o.Semantic}, {&out.Transport, o.Transport}, {&out.Unrecorded, o.Unrecorded},
		{&out.LayerModel, o.LayerModel}, {&out.LayerTool, o.LayerTool},
		{&out.LayerRetrieval, o.LayerRetrieval}, {&out.LayerTransport, o.LayerTransport},
		{&out.LayerUnclassified, o.LayerUnclassified}, {&out.LayerUnrecorded, o.LayerUnrecorded},
	}
	for _, p := range pairs {
		if err := addCount(p.into, p.add, "fidelity summary"); err != nil {
			return b, err
		}
	}
	return out, nil
}

// validateBehaviorFidelity refuses counts no sequence of valid observations
// could produce for an entry of `observations`: the restore-side half of what
// observe keeps on write. Nothing is clamped or repaired.
func validateBehaviorFidelity(b BehaviorFidelity, observations uint64) error {
	fidelityTotal, err := sumNoOverflow([]uint64{b.Semantic, b.Transport, b.Unrecorded})
	if err != nil {
		return errors.New("fidelity counts overflow")
	}
	if fidelityTotal != observations {
		return fmt.Errorf("fidelity counts total %d, observations are %d", fidelityTotal, observations)
	}
	semanticLayers, err := sumNoOverflow([]uint64{b.LayerModel, b.LayerTool, b.LayerRetrieval, b.LayerUnclassified})
	if err != nil {
		return errors.New("layer counts overflow")
	}
	if semanticLayers != b.Semantic {
		return fmt.Errorf("model, tool, retrieval and unclassified layers total %d, semantic observations are %d",
			semanticLayers, b.Semantic)
	}
	if b.LayerTransport != b.Transport {
		return fmt.Errorf("transport layer counts %d, transport observations are %d", b.LayerTransport, b.Transport)
	}
	if b.LayerUnrecorded != b.Unrecorded {
		return fmt.Errorf("unrecorded layer counts %d, unrecorded observations are %d", b.LayerUnrecorded, b.Unrecorded)
	}
	return nil
}

// columnFidelityCounts is schema 13's one column on the behavior entry table:
// the entry's nine fidelity and layer counters as canonical decimal text,
// comma-separated, in fidelityCounterNames order.
//
// One column for both groups rather than two (or nine). The two groups are one
// fact — the pair an envelope stated — and three of their invariants span both,
// so one decode validates them together. And every ingest rewrites a run's
// entries and reads them back: task 087 measured each extra column the SQLite
// driver decodes there, and nothing reads these counters in SQL.
const columnFidelityCounts = "fidelity_counts"

// fidelityCounterCount is how many counters fidelity_counts holds.
const fidelityCounterCount = 9

// fidelityCounterNames names the counters in storage order.
var fidelityCounterNames = [fidelityCounterCount]string{
	"fidelity_semantic", "fidelity_transport", "fidelity_unrecorded",
	"layer_model", "layer_tool", "layer_retrieval", "layer_transport", "layer_unclassified", "layer_unrecorded",
}

func fidelityCounters(b BehaviorFidelity) [fidelityCounterCount]uint64 {
	return [fidelityCounterCount]uint64{
		b.Semantic, b.Transport, b.Unrecorded,
		b.LayerModel, b.LayerTool, b.LayerRetrieval, b.LayerTransport, b.LayerUnclassified, b.LayerUnrecorded,
	}
}

// encodeFidelityCounts renders counts as fidelity_counts stores them.
func encodeFidelityCounts(b BehaviorFidelity) string {
	buf := make([]byte, 0, 2*fidelityCounterCount)
	for i, n := range fidelityCounters(b) {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendUint(buf, n, 10)
	}
	return string(buf)
}

// decodeFidelityCounts parses fidelity_counts. Exactly nine canonical decimals;
// anything else is corruption. Whether they are consistent is
// validateBehaviorFidelity's. Allocation-free on valid input: it runs for every
// entry on every ingest.
func decodeFidelityCounts(text string) (BehaviorFidelity, error) {
	var v [fidelityCounterCount]uint64
	rest := text
	for i := range v {
		field, tail, more := strings.Cut(rest, ",")
		if more != (i < fidelityCounterCount-1) {
			return BehaviorFidelity{}, fmt.Errorf("%w: entry %s holds the wrong number of counters",
				ErrStoreCorrupt, columnFidelityCounts)
		}
		n, ok := parseCanonicalUint64(field)
		if !ok {
			return BehaviorFidelity{}, fmt.Errorf("%w: entry %s is not a canonical uint64: %q",
				ErrStoreCorrupt, fidelityCounterNames[i], preview(field))
		}
		v[i] = n
		rest = tail
	}
	return BehaviorFidelity{
		Semantic: v[0], Transport: v[1], Unrecorded: v[2],
		LayerModel: v[3], LayerTool: v[4], LayerRetrieval: v[5],
		LayerTransport: v[6], LayerUnclassified: v[7], LayerUnrecorded: v[8],
	}, nil
}

// schemaVersionV12 is task 086's schema, the last version without task 081's
// fidelity counts. v13 adds a column only, so v12 and v13 hold the same tables
// and are told apart by the stamp.
const schemaVersionV12 = 12

// behaviorFidelitySchemaStatements are schema 13's whole change, in either
// dialect, shipped on a fresh database too so a fresh and a migrated table are
// column-for-column identical. The empty default is never left in place: the
// migration backfills every existing row, every write states the column, and
// the read path refuses an empty value as corruption.
func behaviorFidelitySchemaStatements(textType string) []string {
	return []string{
		`ALTER TABLE ` + tableEntries + ` ADD COLUMN ` + columnFidelityCounts + ` ` + textType + ` NOT NULL DEFAULT ''`,
	}
}

// behaviorFidelityBackfillStatement makes a behavior recorded before schema 13
// say *unrecorded* in both groups — fidelity_unrecorded and layer_unrecorded
// are its observations, every other counter is 0 — which is the truth and
// keeps every invariant. Nothing is backfilled from retained observations:
// task 067 never retained fidelity. Spelled with `||`, which both dialects
// share. Applied by the migration only.
func behaviorFidelityBackfillStatement() string {
	var b strings.Builder
	b.WriteString(`UPDATE ` + tableEntries + ` SET ` + columnFidelityCounts + ` = '`)
	for i, name := range fidelityCounterNames {
		if i > 0 {
			b.WriteString(",")
		}
		if name == "fidelity_unrecorded" || name == "layer_unrecorded" {
			b.WriteString("' || observations || '")
		} else {
			b.WriteString("0")
		}
	}
	b.WriteString("'")
	return b.String()
}
