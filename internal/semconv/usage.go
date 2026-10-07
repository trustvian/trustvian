package semconv

// Usage and HTTP status code: operational facts about a span, read for the
// evidence path only (task 087).
//
// Neither is behavioral identity, and neither reaches an Event's StableFeatures.
// A token count describes how much a model call cost, not what the actor did;
// a status code describes how a request ended, not which request it was.
// Folding either into a fingerprint would make the same behavior a new one every
// time a response grew or a server returned a different code.
//
// They are read here, beside the convention table, so the keys are listed once
// for both adapters and a test can assert they share nothing with the content
// deny-list. Like the GenAI identity keys, they are string literals: the semconv
// package both adapters pin does not carry the GenAI usage keys (see this
// package's documentation and ADR 0045).
//
// Token counts come from the producer's usage attributes and nothing else.
// Counting tokens in prompt or completion text would mean reading content, which
// this package refuses (content.go).

import "math"

// Usage attribute keys, in precedence order within each part.
const (
	AttrGenAIUsageInputTokens      = "gen_ai.usage.input_tokens"
	AttrGenAIUsageOutputTokens     = "gen_ai.usage.output_tokens"
	AttrGenAIUsagePromptTokens     = "gen_ai.usage.prompt_tokens"     // legacy GenAI name for input
	AttrGenAIUsageCompletionTokens = "gen_ai.usage.completion_tokens" // legacy GenAI name for output
	AttrLLMTokenCountPrompt        = "llm.token_count.prompt"         // OpenInference
	AttrLLMTokenCountCompletion    = "llm.token_count.completion"     // OpenInference
	AttrLLMTokenCountTotal         = "llm.token_count.total"          // OpenInference
)

// MaxTokenCount is the largest token count one observation may report: 2^32,
// about 4.3 billion, orders of magnitude past any model's context window.
//
// A plausibility bound, like event.MaxDurationNanos for a duration. Without
// one, a single producer reporting near-2^64 tokens could make every later sum
// over its behavior overflow — and an overflowing sum refuses the comparison
// it sits beside. With it, overflow needs billions of records on one side. A
// larger value is malformed: absent here, refused at the control plane.
const MaxTokenCount uint64 = 1 << 32

// HTTP status code keys, in precedence order.
const (
	AttrHTTPResponseStatusCode = "http.response.status_code"
	AttrHTTPStatusCode         = "http.status_code" // legacy, before the stable HTTP conventions
)

var (
	inputTokenKeys  = []string{AttrGenAIUsageInputTokens, AttrGenAIUsagePromptTokens, AttrLLMTokenCountPrompt}
	outputTokenKeys = []string{AttrGenAIUsageOutputTokens, AttrGenAIUsageCompletionTokens, AttrLLMTokenCountCompletion}
	statusCodeKeys  = []string{AttrHTTPResponseStatusCode, AttrHTTPStatusCode}
)

// UsageAttributes is every key ReadUsage and ReadHTTPStatusCode read.
//
// Returned as a fresh slice so a caller cannot mutate the package's own lists.
func UsageAttributes() []string {
	all := make([]string, 0, len(inputTokenKeys)+len(outputTokenKeys)+1+len(statusCodeKeys))
	all = append(all, inputTokenKeys...)
	all = append(all, outputTokenKeys...)
	all = append(all, AttrLLMTokenCountTotal)
	all = append(all, statusCodeKeys...)
	return all
}

// Usage is what one span's usage attributes said about its tokens.
//
// Each part carries its own Has flag, because "not stated" and "zero tokens"
// are different facts: a span that reports zero output tokens measured
// something, and a span with no usage attribute measured nothing.
type Usage struct {
	Input    uint64
	HasInput bool

	Output    uint64
	HasOutput bool

	// Unsplit is a total read only when neither part was stated. A total is
	// never split into input and output: the producer did not say how.
	Unsplit    uint64
	HasUnsplit bool
}

// Observed reports whether the span stated any token count at all.
func (u Usage) Observed() bool { return u.HasInput || u.HasOutput || u.HasUnsplit }

// ReadUsage reads the token counts one span's attributes carry.
//
// Within each part the first key present decides, in the order the constants
// above list: the stable GenAI key, its legacy name, then OpenInference. A
// present value that is not an integer from 0 to MaxTokenCount makes that part
// absent; the
// next key is not consulted, because the producer did state this part and
// stated it wrongly, and reading a second key would report a value from a
// different source than the one it chose. llm.token_count.total is read only
// when both parts are absent.
//
// Pure: attrs is read, never written, and nil means no attributes.
func ReadUsage(attrs map[string]any) Usage {
	var u Usage
	u.Input, u.HasInput = firstCount(attrs, inputTokenKeys, MaxTokenCount)
	u.Output, u.HasOutput = firstCount(attrs, outputTokenKeys, MaxTokenCount)
	if !u.HasInput && !u.HasOutput {
		u.Unsplit, u.HasUnsplit = firstCount(attrs, []string{AttrLLMTokenCountTotal}, MaxTokenCount)
	}
	return u
}

// ReadHTTPStatusCode reads the HTTP response status code: the stable key, else
// the legacy one, as an integer from 100 to 599. Anything else — a string, a
// fraction, a code outside the range — is absent, never coerced. It is never
// inferred from span status: an unset span status says nothing about HTTP.
func ReadHTTPStatusCode(attrs map[string]any) (uint16, bool) {
	code, ok := firstCount(attrs, statusCodeKeys, 599)
	if !ok || code < 100 {
		return 0, false
	}
	return uint16(code), true
}

// firstCount returns the value of the first key present in attrs, as a
// non-negative integer no greater than limit. A present key whose value is not
// one is absent, and the keys after it are not read.
func firstCount(attrs map[string]any, keys []string, limit uint64) (uint64, bool) {
	for _, key := range keys {
		raw, present := attrs[key]
		if !present {
			continue
		}
		n, ok := nonNegativeInteger(raw)
		if !ok || n > limit {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// maxExactFloat is 2^53: above it a float64 no longer holds every integer, so
// a larger value cannot be trusted to be the count the producer meant.
const maxExactFloat = 1 << 53

// nonNegativeInteger accepts the integer shapes an attribute map can carry:
// OTLP's int64, Go's int, and an integral float64 within exact range (a
// producer whose language has no integer type sends one). Strings and
// booleans are refused rather than parsed.
func nonNegativeInteger(v any) (uint64, bool) {
	switch n := v.(type) {
	case int64:
		if n >= 0 {
			return uint64(n), true
		}
	case int:
		if n >= 0 {
			return uint64(n), true
		}
	case float64:
		if n >= 0 && n <= maxExactFloat && n == math.Trunc(n) {
			return uint64(n), true
		}
	}
	return 0, false
}
