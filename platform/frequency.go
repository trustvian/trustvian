package platform

// Frequency evidence on the repeated comparison (task 106).
//
// 078 answers *was a behavior present*, as k of N. After a model change a
// developer also asks *how often*: the candidate still calls a tool in 10/10
// runs, but nine times a run instead of three. Every figure here is computed by
// the control plane from what 078 and 087 already persist — each completed
// repetition's per-behavior observation counts — and every figure is an
// integer.
//
// Two of them are derived by one floor division each, calls_per_run_mean_milli
// and call_ratio_permille. Both are integer, order-independent and reproducible,
// and both are reporting only: no gate reads either (task 106 § Conflict:
// ratios and means). The gates read counts and per-run maxima.

import (
	"cmp"
	"errors"
	"fmt"
	"math/bits"
	"slices"
)

// ErrFrequencyOverflow reports a frequency figure past uint64. Refused, never
// wrapped: a wrapped total would report fewer calls for a busier run.
var ErrFrequencyOverflow = errors.New("platform: frequency evidence overflow")

// LostTransitionsNotRecorded is what the comparison says about lost
// transitions: nothing records a transition per execution, and 067's retained
// observations are not a complete sequence, so none is reconstructed.
const LostTransitionsNotRecorded = "not_recorded"

// FrequencyStats is how often one behavior, or one target, was called on one
// side, over that side's completed repetitions.
//
// Runs is how many repetitions the figures cover. With no completed
// repetition there is nothing to state: Available is false, and min, max and
// mean are not figures — never zeros.
type FrequencyStats struct {
	Runs           uint64
	CallsTotal     uint64
	CallsPerRunMin uint64
	CallsPerRunMax uint64

	// CallsPerRunMeanMilli is ⌊CallsTotal × 1000 / Runs⌋, in thousandths.
	CallsPerRunMeanMilli uint64
}

// Available reports whether any completed repetition backs the figures.
func (s FrequencyStats) Available() bool { return s.Runs > 0 }

// frequencyAccumulator folds one side's per-run call counts for one key. A
// run in which the key did not appear is a run of zero calls, which is what
// makes the minimum honest: absent in one run means a minimum of zero.
type frequencyAccumulator struct {
	total       uint64
	max         uint64
	minPresent  uint64
	presentRuns uint64
}

func (a *frequencyAccumulator) addRun(calls uint64) error {
	if a.total > ^uint64(0)-calls {
		return fmt.Errorf("%w: a calls total would exceed %d", ErrFrequencyOverflow, ^uint64(0))
	}
	a.total += calls
	a.max = max(a.max, calls)
	if a.presentRuns == 0 || calls < a.minPresent {
		a.minPresent = calls
	}
	a.presentRuns++
	return nil
}

// stats closes the accumulator over the side's completed runs.
func (a frequencyAccumulator) stats(runs uint64) (FrequencyStats, error) {
	if runs == 0 {
		return FrequencyStats{}, nil
	}
	mean, err := floorDivTimes1000(a.total, runs)
	if err != nil {
		return FrequencyStats{}, err
	}
	low := a.minPresent
	if a.presentRuns < runs {
		low = 0
	}
	return FrequencyStats{
		Runs: runs, CallsTotal: a.total, CallsPerRunMin: low, CallsPerRunMax: a.max,
		CallsPerRunMeanMilli: mean,
	}, nil
}

// floorDivTimes1000 is ⌊n × 1000 / d⌋ in 128 bits; a quotient past uint64 is
// an error. d must not be zero.
func floorDivTimes1000(n, d uint64) (uint64, error) {
	hi, lo := bits.Mul64(n, 1000)
	if hi >= d {
		return 0, fmt.Errorf("%w: %d × 1000 / %d exceeds %d", ErrFrequencyOverflow, n, d, ^uint64(0))
	}
	q, _ := bits.Div64(hi, lo, d)
	return q, nil
}

// RepeatedTargetFrequency is one target's frequency on both sides: the
// behaviors that share its name and category, summed per run.
type RepeatedTargetFrequency struct {
	Target               OperationalTarget
	Reference, Candidate FrequencyStats

	// CallRatioPermille is ⌊candidate total × 1000 / reference total⌋. With a
	// reference total of zero there is no ratio — not infinite, not zero —
	// and RatioAvailable is false.
	CallRatioPermille uint64
	RatioAvailable    bool

	// CandidateHTTP429 and CandidateHTTPStated are 087's evidence for this
	// target on the candidate side, summed over its runs, for the comparison
	// rules. Stated is how many observations reported any status code.
	CandidateHTTP429    uint64
	CandidateHTTPStated uint64
}

// frequencySide accumulates one side: per behavior and per target call
// counts over its completed repetitions, and 087's 429 and stated-status
// counts per target for the comparison rules.
type frequencySide struct {
	runs      uint64
	behaviors map[string]*frequencyAccumulator
	targets   map[OperationalTarget]*frequencyAccumulator

	// byName is calls per run to a target name, across categories: what
	// max_calls_per_run names, since a scenario author knows a host, not the
	// category a convention filed it under.
	byName   map[string]*frequencyAccumulator
	http429  map[OperationalTarget]uint64
	httpSaid map[OperationalTarget]uint64
}

func newFrequencySide() *frequencySide {
	return &frequencySide{
		behaviors: map[string]*frequencyAccumulator{},
		targets:   map[OperationalTarget]*frequencyAccumulator{},
		byName:    map[string]*frequencyAccumulator{},
		http429:   map[OperationalTarget]uint64{},
		httpSaid:  map[OperationalTarget]uint64{},
	}
}

// addRun folds one completed repetition's entries in.
func (s *frequencySide) addRun(entries []BehaviorEntry) error {
	s.runs++
	perTarget := map[OperationalTarget]uint64{}
	for _, e := range entries {
		if e.Observations == 0 {
			continue
		}
		acc := s.behaviors[e.FingerprintID]
		if acc == nil {
			acc = &frequencyAccumulator{}
			s.behaviors[e.FingerprintID] = acc
		}
		if err := acc.addRun(e.Observations); err != nil {
			return err
		}
		t := OperationalTarget{Name: e.Behavior.TargetName, Category: string(e.Behavior.TargetCategory)}
		sum := perTarget[t]
		if sum > ^uint64(0)-e.Observations {
			return fmt.Errorf("%w: calls to one target in one run", ErrFrequencyOverflow)
		}
		perTarget[t] = sum + e.Observations
		for _, f := range []struct {
			into map[OperationalTarget]uint64
			add  uint64
		}{{s.http429, e.Operational.HTTP429}, {s.httpSaid, e.Operational.HTTPStatus.Stated()}} {
			if f.into[t] > ^uint64(0)-f.add {
				return fmt.Errorf("%w: a status count for one target", ErrFrequencyOverflow)
			}
			f.into[t] += f.add
		}
	}
	perName := map[string]uint64{}
	for t, calls := range perTarget {
		acc := s.targets[t]
		if acc == nil {
			acc = &frequencyAccumulator{}
			s.targets[t] = acc
		}
		if err := acc.addRun(calls); err != nil {
			return err
		}
		if perName[t.Name] > ^uint64(0)-calls {
			return fmt.Errorf("%w: calls to one target name in one run", ErrFrequencyOverflow)
		}
		perName[t.Name] += calls
	}
	for name, calls := range perName {
		acc := s.byName[name]
		if acc == nil {
			acc = &frequencyAccumulator{}
			s.byName[name] = acc
		}
		if err := acc.addRun(calls); err != nil {
			return err
		}
	}
	return nil
}

// behaviorStats is one behavior's figures on this side; a behavior the side
// never saw has zero calls over its runs.
func (s *frequencySide) behaviorStats(fingerprint string) (FrequencyStats, error) {
	acc := s.behaviors[fingerprint]
	if acc == nil {
		acc = &frequencyAccumulator{}
	}
	return acc.stats(s.runs)
}

func (s *frequencySide) targetStats(t OperationalTarget) (FrequencyStats, error) {
	acc := s.targets[t]
	if acc == nil {
		acc = &frequencyAccumulator{}
	}
	return acc.stats(s.runs)
}

// targetFrequencies builds every target either side called, ordered by
// category then name — the per-target read's own order.
func targetFrequencies(reference, candidate *frequencySide) ([]RepeatedTargetFrequency, error) {
	keys := make([]OperationalTarget, 0, len(reference.targets)+len(candidate.targets))
	for t := range reference.targets {
		keys = append(keys, t)
	}
	for t := range candidate.targets {
		if _, seen := reference.targets[t]; !seen {
			keys = append(keys, t)
		}
	}
	slices.SortFunc(keys, func(a, b OperationalTarget) int {
		return cmp.Or(cmp.Compare(a.Category, b.Category), cmp.Compare(a.Name, b.Name))
	})
	out := make([]RepeatedTargetFrequency, 0, len(keys))
	for _, t := range keys {
		ref, err := reference.targetStats(t)
		if err != nil {
			return nil, err
		}
		cand, err := candidate.targetStats(t)
		if err != nil {
			return nil, err
		}
		row := RepeatedTargetFrequency{
			Target: t, Reference: ref, Candidate: cand,
			CandidateHTTP429: candidate.http429[t], CandidateHTTPStated: candidate.httpSaid[t],
		}
		if ref.CallsTotal > 0 {
			ratio, err := floorDivTimes1000(cand.CallsTotal, ref.CallsTotal)
			if err != nil {
				return nil, err
			}
			row.CallRatioPermille, row.RatioAvailable = ratio, true
		}
		out = append(out, row)
	}
	return out, nil
}

// isLost is task 106's classification: present in every reference repetition
// and missing from at least one candidate repetition. Reported beside 078's
// classification rather than as a value of it — see RepeatedBehaviorPresence.
func isLost(referencePresent, candidatePresent, runs uint64) bool {
	return runs > 0 && referencePresent == runs && candidatePresent < runs
}
