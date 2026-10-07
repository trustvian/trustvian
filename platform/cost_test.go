package platform

import (
	"errors"
	"math"
	"testing"
)

func TestModelCostRoundsDownOnceAndRefusesOverflow(t *testing.T) {
	tests := []struct {
		name                       string
		input, inPrice, out, outPr uint64
		want                       uint64
		overflow                   bool
	}{
		{"exact", 1_000_000, 2_500_000, 0, 0, 2_500_000, false},
		// 1×1 + 1×1 = 2 micro-units per million: rounded down to 0, once, at
		// the model — not 0 + 0 per part.
		{"rounded down once", 999_999, 1, 1, 1, 1, false},
		{"zero price", math.MaxUint64, 0, math.MaxUint64, 0, 0, false},
		{"product past 64 bits, quotient within", math.MaxUint64, 1_000_000, 0, 0, math.MaxUint64, false},
		{"quotient past 64 bits", math.MaxUint64, 1_000_001, 0, 0, 0, true},
		{"sum past 128 bits", math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := modelCost(tt.input, tt.inPrice, tt.out, tt.outPr)
			if tt.overflow {
				if !errors.Is(err, ErrCostOverflow) {
					t.Fatalf("error = %v, want ErrCostOverflow", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("modelCost() = %d, %v; want %d", got, err, tt.want)
			}
		})
	}
}

// TestCostIsIndependentOfOrder: the same token counts give the same figure
// however the runs and behaviors are ordered — tokens are summed per model
// first, and the rounding happens once per model per side.
func TestCostIsIndependentOfOrder(t *testing.T) {
	pricing, err := NewPricing("v", "s", "USD", "sha256:x", map[string]ModelPrice{"op-a": {InputMicrosPerMillion: 3, OutputMicrosPerMillion: 7}})
	if err != nil {
		t.Fatal(err)
	}
	tok := func(in, out uint64) OperationalSummary {
		return OperationalSummary{Tokens: TokenCounts{Input: in, Output: out, Observed: 1}}
	}
	runs := [][]BehaviorEntry{
		{entryWith("a", 1, tok(333_333, 1)), entryWith("b", 1, tok(5, 5))},
		{entryWith("a", 1, tok(333_333, 2))},
		{entryWith("a", 1, tok(333_334, 3))},
	}
	orders := [][3]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}}
	var want CostEvidence
	for i, o := range orders {
		got, ok, err := compareCost(pricing, [][]BehaviorEntry{runs[o[0]], runs[o[1]], runs[o[2]]}, nil)
		if err != nil || !ok {
			t.Fatal(err)
		}
		if i == 0 {
			want = got.Reference
			// (1 000 000 × 3 + 6 × 7) / 1e6 = 3.000042 → 3 micro-units.
			if want.CostMicros != 3 || want.PricedTokens != 1_000_006 || want.UnpricedTokens != 10 ||
				want.RunsWithEvidence != 3 {
				t.Fatalf("cost = %+v", want)
			}
		} else if got.Reference != want {
			t.Fatalf("order %v gave %+v, want %+v", o, got.Reference, want)
		}
	}
}

func TestNoPricingIsNoCostSection(t *testing.T) {
	if _, ok, err := compareCost(Pricing{}, nil, nil); ok || err != nil {
		t.Fatalf("compareCost without pricing = %v, %v; want no section", ok, err)
	}
	if _, err := NewPricing("", "s", "USD", "d", map[string]ModelPrice{"m": {}}); err == nil {
		t.Fatal("a pricing table without a version was accepted")
	}
}
