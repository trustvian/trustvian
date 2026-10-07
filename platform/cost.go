package platform

// Cost, from an operator-supplied pricing table (task 087).
//
// Optional by construction: a control plane built without pricing reports no
// cost section at all, and nothing else in any response changes. With pricing,
// a comparison side's cost is
//
//	Σ over priced models  floor((input × input price + output × output price) / 1 000 000)
//
// in integer micro-units of the table's currency. Prices are micro-units per
// million tokens. Each model's product is taken in 128 bits and rounded down
// once, at that model; the side's total is the sum of those, and any step past
// uint64 is an error, never a wrapped figure.
//
// A behavior is priced when its operation name is a model the table prices —
// the model-layer convention names a model call after its model (task 075).
// Every other token count is reported as unpriced, never given a guessed
// price: tokens of a behavior the table does not price, and every unsplit total
// even for a priced model, because no input or output rate applies to a total.
//
// Every figure travels with the table's version, source and digest. A renderer
// that has a cost but not its provenance renders neither.

import (
	"errors"
	"fmt"
	"math/bits"
)

// ErrCostOverflow reports a cost past uint64 micro-units. The comparison
// cannot be priced under this table; it is refused rather than wrapped.
var ErrCostOverflow = errors.New("platform: cost overflow")

// ModelPrice is one model's price, in micro-units per million tokens.
type ModelPrice struct {
	InputMicrosPerMillion  uint64
	OutputMicrosPerMillion uint64
}

// Pricing is a validated pricing table with its provenance. The zero value is
// "no pricing configured".
type Pricing struct {
	version  string
	source   string
	currency string
	digest   string
	models   map[string]ModelPrice
}

// NewPricing binds a table parsed and validated by the config package.
// Provenance is required: a price without it is a figure nobody can check.
func NewPricing(pricingVersion, source, currency, digest string, models map[string]ModelPrice) (Pricing, error) {
	if pricingVersion == "" || source == "" || currency == "" || digest == "" || len(models) == 0 {
		return Pricing{}, errors.New("platform: pricing needs a version, a source, a currency, a digest and at least one model")
	}
	copied := make(map[string]ModelPrice, len(models))
	for name, price := range models {
		copied[name] = price
	}
	return Pricing{version: pricingVersion, source: source, currency: currency, digest: digest, models: copied}, nil
}

// Version and Digest identify the table, for display beside any figure.
func (p Pricing) Version() string { return p.version }
func (p Pricing) Digest() string  { return p.digest }

// Configured reports whether a table was supplied.
func (p Pricing) Configured() bool { return p.models != nil }

// WithPricing attaches a pricing table. Without it no comparison carries cost.
func WithPricing(p Pricing) ControlPlaneOption {
	return func(c *ControlPlane) { c.pricing = p }
}

// CostEvidence is one side's cost.
type CostEvidence struct {
	// RunsWithEvidence is how many of the side's runs reported any token
	// count; a side with none has no cost to state.
	RunsWithEvidence uint64

	CostMicros     uint64
	PricedTokens   uint64
	UnpricedTokens uint64

	// Coverage: how many observations on the side reported tokens and how
	// many did not. A cost over 1 of 1,000 model calls is a figure, not a
	// total, and a reader must be able to tell without consulting another
	// section.
	TokenObservations         uint64
	ObservationsWithoutTokens uint64
}

// Available reports whether any run on the side reported tokens.
func (e CostEvidence) Available() bool { return e.RunsWithEvidence > 0 }

// CostComparison is the cost section: provenance, both sides, and whether
// they compare.
type CostComparison struct {
	PricingVersion string
	PricingDigest  string
	Source         string
	Currency       string

	Reference, Candidate CostEvidence
	Unavailable          OperationalUnavailability
}

// Comparable reports whether both sides reported tokens.
func (c CostComparison) Comparable() bool { return c.Unavailable == "" }

// costSide accumulates one side's per-model token sums over its runs.
type costSide struct {
	runs                             uint64
	tokenObservations, withoutTokens uint64
	input                            map[string]uint64
	output                           map[string]uint64
	unpaid                           uint64
	unsplit                          uint64
}

// addRun folds one run's entries in. Tokens are summed per model name first,
// so the rounding happens once per model per side, whatever the order of the
// runs or behaviors.
func (s *costSide) addRun(p Pricing, entries []BehaviorEntry) error {
	if s.input == nil {
		s.input, s.output = map[string]uint64{}, map[string]uint64{}
	}
	observed := false
	for _, e := range entries {
		t := e.Operational.Tokens
		if err := addCost(&s.tokenObservations, t.Observed); err != nil {
			return err
		}
		if err := addCost(&s.withoutTokens, t.Unobserved); err != nil {
			return err
		}
		if t.Observed == 0 {
			continue
		}
		observed = true
		if err := addCost(&s.unsplit, t.Unsplit); err != nil {
			return err
		}
		model := e.Behavior.OperationName
		if _, priced := p.models[model]; !priced {
			if err := addCost(&s.unpaid, t.Input); err != nil {
				return err
			}
			if err := addCost(&s.unpaid, t.Output); err != nil {
				return err
			}
			continue
		}
		in, out := s.input[model], s.output[model]
		if err := addCost(&in, t.Input); err != nil {
			return err
		}
		if err := addCost(&out, t.Output); err != nil {
			return err
		}
		s.input[model], s.output[model] = in, out
	}
	if observed {
		s.runs++
	}
	return nil
}

func addCost(field *uint64, n uint64) error {
	if *field > ^uint64(0)-n {
		return fmt.Errorf("%w: a token sum exceeds %d", ErrCostOverflow, ^uint64(0))
	}
	*field += n
	return nil
}

// evidence prices the accumulated side.
func (s costSide) evidence(p Pricing) (CostEvidence, error) {
	out := CostEvidence{RunsWithEvidence: s.runs,
		TokenObservations: s.tokenObservations, ObservationsWithoutTokens: s.withoutTokens}
	unpriced := s.unpaid
	if err := addCost(&unpriced, s.unsplit); err != nil {
		return CostEvidence{}, err
	}
	out.UnpricedTokens = unpriced
	for model, in := range s.input {
		price := p.models[model]
		cost, err := modelCost(in, price.InputMicrosPerMillion, s.output[model], price.OutputMicrosPerMillion)
		if err != nil {
			return CostEvidence{}, fmt.Errorf("model %s: %w", preview(model), err)
		}
		if err := addCost(&out.CostMicros, cost); err != nil {
			return CostEvidence{}, err
		}
		if err := addCost(&out.PricedTokens, in); err != nil {
			return CostEvidence{}, err
		}
		if err := addCost(&out.PricedTokens, s.output[model]); err != nil {
			return CostEvidence{}, err
		}
	}
	return out, nil
}

// modelCost is floor((input×inPrice + output×outPrice) / 1e6), taken in 128
// bits. Overflow at any step is an error.
func modelCost(input, inPrice, output, outPrice uint64) (uint64, error) {
	hiIn, loIn := bits.Mul64(input, inPrice)
	hiOut, loOut := bits.Mul64(output, outPrice)
	lo, carry := bits.Add64(loIn, loOut, 0)
	hi, overflow := bits.Add64(hiIn, hiOut, carry)
	if overflow != 0 {
		return 0, fmt.Errorf("%w: the token cost exceeds 128 bits", ErrCostOverflow)
	}
	const perMillion = 1_000_000
	if hi >= perMillion {
		return 0, fmt.Errorf("%w: the cost exceeds %d micro-units", ErrCostOverflow, ^uint64(0))
	}
	quotient, _ := bits.Div64(hi, lo, perMillion)
	return quotient, nil
}

// compareCost builds the cost section from two sides' runs. ok is false when
// no pricing is configured, and then there is no section at all.
func compareCost(p Pricing, reference, candidate [][]BehaviorEntry) (CostComparison, bool, error) {
	if !p.Configured() {
		return CostComparison{}, false, nil
	}
	var ref, cand costSide
	for _, run := range reference {
		if err := ref.addRun(p, run); err != nil {
			return CostComparison{}, false, err
		}
	}
	for _, run := range candidate {
		if err := cand.addRun(p, run); err != nil {
			return CostComparison{}, false, err
		}
	}
	refEvidence, err := ref.evidence(p)
	if err != nil {
		return CostComparison{}, false, err
	}
	candEvidence, err := cand.evidence(p)
	if err != nil {
		return CostComparison{}, false, err
	}
	return CostComparison{
		PricingVersion: p.version, PricingDigest: p.digest, Source: p.source, Currency: p.currency,
		Reference: refEvidence, Candidate: candEvidence,
		Unavailable: unavailability(refEvidence.Available(), candEvidence.Available()),
	}, true, nil
}
