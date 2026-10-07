package httpapi

// Task 087's latency, errors and tokens sections on the wire.
//
// Every number is canonical decimal text, and every delta is candidate minus
// reference of the same counter, as signed canonical decimal text ("-12").
// A section that is not comparable carries `"comparable": false`, a `reason`
// from a closed set, and no `delta` at all: the field is absent, so nothing can
// render a change against a side that measured nothing. The duration minimum
// and maximum are absent on a side that measured no duration, for the same
// reason — "0" there would read as an instantaneous call.

import (
	"strconv"

	platform "trustvian-platform"
)

// durationBucketLabels name the eleven buckets, in order: "le_1ms" through
// "le_10000ms", then "gt_10000ms". A closed vocabulary a renderer can rely on.
var durationBucketLabels = func() [platform.DurationBucketCount]string {
	var labels [platform.DurationBucketCount]string
	bounds := platform.DurationBucketUpperMillis()
	for i, upper := range bounds {
		labels[i] = "le_" + strconv.FormatUint(upper, 10) + "ms"
	}
	labels[platform.DurationBucketCount-1] = "gt_" + strconv.FormatUint(bounds[len(bounds)-1], 10) + "ms"
	return labels
}()

type durationBucketDTO struct {
	Bound string `json:"bound"`
	Count string `json:"count"`
}

type latencySideDTO struct {
	RunsWithEvidence string              `json:"runs_with_evidence"`
	Buckets          []durationBucketDTO `json:"buckets"`
	Observed         string              `json:"observed"`
	Unobserved       string              `json:"unobserved"`
	SumNanos         string              `json:"sum_nanos"`
	MinNanos         *string             `json:"min_nanos,omitempty"`
	MaxNanos         *string             `json:"max_nanos,omitempty"`
}

type latencyDeltaDTO struct {
	Buckets    []durationBucketDTO `json:"buckets"`
	Observed   string              `json:"observed"`
	Unobserved string              `json:"unobserved"`
	SumNanos   string              `json:"sum_nanos"`
	MinNanos   string              `json:"min_nanos"`
	MaxNanos   string              `json:"max_nanos"`
}

type latencySectionDTO struct {
	Comparable bool             `json:"comparable"`
	Reason     string           `json:"reason,omitempty"`
	Reference  latencySideDTO   `json:"reference"`
	Candidate  latencySideDTO   `json:"candidate"`
	Delta      *latencyDeltaDTO `json:"delta,omitempty"`
}

type spanStatusCountsDTO struct {
	Unavailable string `json:"unavailable"`
	Unset       string `json:"unset"`
	OK          string `json:"ok"`
	Error       string `json:"error"`
}

type httpStatusClassesDTO struct {
	Class1xx    string `json:"1xx"`
	Class2xx    string `json:"2xx"`
	Class3xx    string `json:"3xx"`
	Class4xx    string `json:"4xx"`
	Class5xx    string `json:"5xx"`
	Unavailable string `json:"unavailable"`
}

type errorsSideDTO struct {
	RunsWithEvidence string               `json:"runs_with_evidence"`
	SpanStatus       spanStatusCountsDTO  `json:"span_status"`
	HTTPStatus       httpStatusClassesDTO `json:"http_status"`
	HTTP429          string               `json:"http_429"`
}

type errorsDeltaDTO struct {
	SpanStatus spanStatusCountsDTO  `json:"span_status"`
	HTTPStatus httpStatusClassesDTO `json:"http_status"`
	HTTP429    string               `json:"http_429"`
}

type errorsSectionDTO struct {
	Comparable bool            `json:"comparable"`
	Reason     string          `json:"reason,omitempty"`
	Reference  errorsSideDTO   `json:"reference"`
	Candidate  errorsSideDTO   `json:"candidate"`
	Delta      *errorsDeltaDTO `json:"delta,omitempty"`
}

type tokensSideDTO struct {
	RunsWithEvidence string `json:"runs_with_evidence"`
	Input            string `json:"input"`
	Output           string `json:"output"`
	Unsplit          string `json:"unsplit"`
	Observed         string `json:"observed"`
	Unobserved       string `json:"unobserved"`
}

type tokensDeltaDTO struct {
	Input      string `json:"input"`
	Output     string `json:"output"`
	Unsplit    string `json:"unsplit"`
	Observed   string `json:"observed"`
	Unobserved string `json:"unobserved"`
}

type tokensSectionDTO struct {
	Comparable bool            `json:"comparable"`
	Reason     string          `json:"reason,omitempty"`
	Reference  tokensSideDTO   `json:"reference"`
	Candidate  tokensSideDTO   `json:"candidate"`
	Delta      *tokensDeltaDTO `json:"delta,omitempty"`
}

func delta(candidate, reference uint64) string {
	return platform.Difference(candidate, reference).String()
}

func newLatencySideDTO(e platform.LatencyEvidence) latencySideDTO {
	buckets := make([]durationBucketDTO, 0, platform.DurationBucketCount)
	for i, n := range e.Buckets {
		buckets = append(buckets, durationBucketDTO{Bound: durationBucketLabels[i], Count: u64(n)})
	}
	dto := latencySideDTO{
		RunsWithEvidence: u64(e.RunsWithEvidence), Buckets: buckets,
		Observed: u64(e.Durations.Count), Unobserved: u64(e.Durations.Unobserved),
		SumNanos: u64(e.Durations.Sum),
	}
	if e.Available() {
		lo, hi := u64(e.Durations.Min), u64(e.Durations.Max)
		dto.MinNanos, dto.MaxNanos = &lo, &hi
	}
	return dto
}

func newLatencySectionDTO(c platform.LatencyComparison) latencySectionDTO {
	dto := latencySectionDTO{
		Comparable: c.Comparable(), Reason: string(c.Unavailable),
		Reference: newLatencySideDTO(c.Reference), Candidate: newLatencySideDTO(c.Candidate),
	}
	if !c.Comparable() {
		return dto
	}
	r, k := c.Reference, c.Candidate
	buckets := make([]durationBucketDTO, 0, platform.DurationBucketCount)
	for i := range r.Buckets {
		buckets = append(buckets, durationBucketDTO{Bound: durationBucketLabels[i], Count: delta(k.Buckets[i], r.Buckets[i])})
	}
	dto.Delta = &latencyDeltaDTO{
		Buckets:    buckets,
		Observed:   delta(k.Durations.Count, r.Durations.Count),
		Unobserved: delta(k.Durations.Unobserved, r.Durations.Unobserved),
		SumNanos:   delta(k.Durations.Sum, r.Durations.Sum),
		MinNanos:   delta(k.Durations.Min, r.Durations.Min),
		MaxNanos:   delta(k.Durations.Max, r.Durations.Max),
	}
	return dto
}

func newSpanStatusCountsDTO(c platform.SpanStatusCounts) spanStatusCountsDTO {
	return spanStatusCountsDTO{Unavailable: u64(c.Unavailable), Unset: u64(c.Unset), OK: u64(c.OK), Error: u64(c.Error)}
}

func newHTTPStatusClassesDTO(c platform.HTTPStatusClassCounts) httpStatusClassesDTO {
	return httpStatusClassesDTO{
		Class1xx: u64(c.Informational), Class2xx: u64(c.Success), Class3xx: u64(c.Redirection),
		Class4xx: u64(c.ClientError), Class5xx: u64(c.ServerError), Unavailable: u64(c.Unavailable),
	}
}

func newErrorsSideDTO(e platform.ErrorEvidence) errorsSideDTO {
	return errorsSideDTO{
		RunsWithEvidence: u64(e.RunsWithEvidence),
		SpanStatus:       newSpanStatusCountsDTO(e.SpanStatus),
		HTTPStatus:       newHTTPStatusClassesDTO(e.HTTPStatus),
		HTTP429:          u64(e.HTTP429),
	}
}

func newErrorsSectionDTO(c platform.ErrorComparison) errorsSectionDTO {
	dto := errorsSectionDTO{
		Comparable: c.Comparable(), Reason: string(c.Unavailable),
		Reference: newErrorsSideDTO(c.Reference), Candidate: newErrorsSideDTO(c.Candidate),
	}
	if !c.Comparable() {
		return dto
	}
	rs, ks := c.Reference.SpanStatus, c.Candidate.SpanStatus
	rh, kh := c.Reference.HTTPStatus, c.Candidate.HTTPStatus
	dto.Delta = &errorsDeltaDTO{
		SpanStatus: spanStatusCountsDTO{
			Unavailable: delta(ks.Unavailable, rs.Unavailable), Unset: delta(ks.Unset, rs.Unset),
			OK: delta(ks.OK, rs.OK), Error: delta(ks.Error, rs.Error),
		},
		HTTPStatus: httpStatusClassesDTO{
			Class1xx: delta(kh.Informational, rh.Informational), Class2xx: delta(kh.Success, rh.Success),
			Class3xx: delta(kh.Redirection, rh.Redirection), Class4xx: delta(kh.ClientError, rh.ClientError),
			Class5xx: delta(kh.ServerError, rh.ServerError), Unavailable: delta(kh.Unavailable, rh.Unavailable),
		},
		HTTP429: delta(c.Candidate.HTTP429, c.Reference.HTTP429),
	}
	return dto
}

func newTokensSideDTO(e platform.TokenEvidence) tokensSideDTO {
	t := e.Tokens
	return tokensSideDTO{
		RunsWithEvidence: u64(e.RunsWithEvidence),
		Input:            u64(t.Input), Output: u64(t.Output), Unsplit: u64(t.Unsplit),
		Observed: u64(t.Observed), Unobserved: u64(t.Unobserved),
	}
}

func newTokensSectionDTO(c platform.TokenComparison) tokensSectionDTO {
	dto := tokensSectionDTO{
		Comparable: c.Comparable(), Reason: string(c.Unavailable),
		Reference: newTokensSideDTO(c.Reference), Candidate: newTokensSideDTO(c.Candidate),
	}
	if !c.Comparable() {
		return dto
	}
	r, k := c.Reference.Tokens, c.Candidate.Tokens
	dto.Delta = &tokensDeltaDTO{
		Input: delta(k.Input, r.Input), Output: delta(k.Output, r.Output), Unsplit: delta(k.Unsplit, r.Unsplit),
		Observed: delta(k.Observed, r.Observed), Unobserved: delta(k.Unobserved, r.Unobserved),
	}
	return dto
}

// operationalSectionsDTO is the three sections together, as a repeated
// comparison carries them.
type operationalSectionsDTO struct {
	Latency latencySectionDTO `json:"latency"`
	Errors  errorsSectionDTO  `json:"errors"`
	Tokens  tokensSectionDTO  `json:"tokens"`
	Cost    *costSectionDTO   `json:"cost,omitempty"`
}

func newOperationalSectionsDTO(c platform.OperationalComparison, cost *platform.CostComparison) operationalSectionsDTO {
	return operationalSectionsDTO{
		Latency: newLatencySectionDTO(c.Latency),
		Errors:  newErrorsSectionDTO(c.Errors),
		Tokens:  newTokensSectionDTO(c.Tokens),
		Cost:    newCostSectionDTO(cost),
	}
}

// costSideDTO is one side's cost. cost_micros is absent on a side that
// reported no tokens: its cost is unknown, not zero.
type costSideDTO struct {
	RunsWithEvidence string  `json:"runs_with_evidence"`
	CostMicros       *string `json:"cost_micros,omitempty"`
	PricedTokens     string  `json:"priced_tokens"`
	UnpricedTokens   string  `json:"unpriced_tokens"`

	// Coverage of the figure: observations that reported tokens, and those
	// that did not.
	TokenObservations         string `json:"token_observations"`
	ObservationsWithoutTokens string `json:"observations_without_tokens"`
}

type costDeltaDTO struct {
	CostMicros string `json:"cost_micros"`
}

// costSectionDTO carries its provenance on every rendering: a cost is never
// published without the pricing version, digest and source it came from.
type costSectionDTO struct {
	PricingVersion string        `json:"pricing_version"`
	PricingDigest  string        `json:"pricing_digest"`
	Source         string        `json:"source"`
	Currency       string        `json:"currency"`
	Comparable     bool          `json:"comparable"`
	Reason         string        `json:"reason,omitempty"`
	Reference      costSideDTO   `json:"reference"`
	Candidate      costSideDTO   `json:"candidate"`
	Delta          *costDeltaDTO `json:"delta,omitempty"`
}

func newCostSideDTO(e platform.CostEvidence) costSideDTO {
	dto := costSideDTO{
		RunsWithEvidence: u64(e.RunsWithEvidence),
		PricedTokens:     u64(e.PricedTokens), UnpricedTokens: u64(e.UnpricedTokens),
		TokenObservations:         u64(e.TokenObservations),
		ObservationsWithoutTokens: u64(e.ObservationsWithoutTokens),
	}
	if e.Available() {
		cost := u64(e.CostMicros)
		dto.CostMicros = &cost
	}
	return dto
}

// newCostSectionDTO renders the section, or nil when no pricing is configured
// — and then the response has no cost field at all.
func newCostSectionDTO(c *platform.CostComparison) *costSectionDTO {
	if c == nil {
		return nil
	}
	dto := &costSectionDTO{
		PricingVersion: c.PricingVersion, PricingDigest: c.PricingDigest,
		Source: c.Source, Currency: c.Currency,
		Comparable: c.Comparable(), Reason: string(c.Unavailable),
		Reference: newCostSideDTO(c.Reference), Candidate: newCostSideDTO(c.Candidate),
	}
	if c.Comparable() {
		dto.Delta = &costDeltaDTO{CostMicros: delta(c.Candidate.CostMicros, c.Reference.CostMicros)}
	}
	return dto
}
