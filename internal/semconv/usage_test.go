package semconv_test

import (
	"math"
	"slices"
	"testing"

	"github.com/trustvian/trustvian/internal/semconv"
)

func TestReadUsage(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]any
		want  semconv.Usage
	}{
		{name: "nothing stated", attrs: nil, want: semconv.Usage{}},
		{name: "GenAI input and output",
			attrs: map[string]any{"gen_ai.usage.input_tokens": int64(120), "gen_ai.usage.output_tokens": int64(30)},
			want:  semconv.Usage{Input: 120, HasInput: true, Output: 30, HasOutput: true}},
		{name: "legacy GenAI names",
			attrs: map[string]any{"gen_ai.usage.prompt_tokens": int64(7), "gen_ai.usage.completion_tokens": int64(8)},
			want:  semconv.Usage{Input: 7, HasInput: true, Output: 8, HasOutput: true}},
		{name: "OpenInference counts",
			attrs: map[string]any{"llm.token_count.prompt": int64(5), "llm.token_count.completion": int64(6),
				"llm.token_count.total": int64(11)},
			want: semconv.Usage{Input: 5, HasInput: true, Output: 6, HasOutput: true}},
		{name: "the stable key outranks the legacy and OpenInference keys",
			attrs: map[string]any{"gen_ai.usage.input_tokens": int64(1), "gen_ai.usage.prompt_tokens": int64(2),
				"llm.token_count.prompt": int64(3)},
			want: semconv.Usage{Input: 1, HasInput: true}},
		{name: "zero is a measurement",
			attrs: map[string]any{"gen_ai.usage.output_tokens": int64(0)},
			want:  semconv.Usage{Output: 0, HasOutput: true}},
		{name: "a total alone is unsplit, never divided",
			attrs: map[string]any{"llm.token_count.total": int64(40)},
			want:  semconv.Usage{Unsplit: 40, HasUnsplit: true}},
		{name: "a total beside one part is not read",
			attrs: map[string]any{"llm.token_count.prompt": int64(10), "llm.token_count.total": int64(40)},
			want:  semconv.Usage{Input: 10, HasInput: true}},
		{name: "a negative value is absent, and the next key is not consulted",
			attrs: map[string]any{"gen_ai.usage.input_tokens": int64(-1), "llm.token_count.prompt": int64(9)},
			want:  semconv.Usage{}},
		{name: "a string is absent, never parsed",
			attrs: map[string]any{"gen_ai.usage.input_tokens": "12"},
			want:  semconv.Usage{}},
		{name: "an integral float is read",
			attrs: map[string]any{"gen_ai.usage.input_tokens": float64(12)},
			want:  semconv.Usage{Input: 12, HasInput: true}},
		{name: "a fractional float is absent",
			attrs: map[string]any{"gen_ai.usage.input_tokens": 12.5},
			want:  semconv.Usage{}},
		{name: "the largest plausible count is read",
			attrs: map[string]any{"gen_ai.usage.input_tokens": int64(1 << 32)},
			want:  semconv.Usage{Input: 1 << 32, HasInput: true}},
		{name: "a count past MaxTokenCount is absent, never clamped",
			attrs: map[string]any{"gen_ai.usage.input_tokens": int64(1<<32 + 1)},
			want:  semconv.Usage{}},
		{name: "a float past exact range is absent",
			attrs: map[string]any{"gen_ai.usage.input_tokens": float64(1 << 54)},
			want:  semconv.Usage{}},
		{name: "NaN and infinity are absent",
			attrs: map[string]any{"gen_ai.usage.input_tokens": math.NaN(), "gen_ai.usage.output_tokens": math.Inf(1)},
			want:  semconv.Usage{}},
		{name: "a malformed part is absent, so the total is read",
			attrs: map[string]any{"llm.token_count.prompt": true, "llm.token_count.total": int64(3)},
			want:  semconv.Usage{Unsplit: 3, HasUnsplit: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := semconv.ReadUsage(tt.attrs); got != tt.want {
				t.Fatalf("ReadUsage() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadHTTPStatusCode(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]any
		want  uint16
		ok    bool
	}{
		{"absent", nil, 0, false},
		{"stable key", map[string]any{"http.response.status_code": int64(429)}, 429, true},
		{"legacy key", map[string]any{"http.status_code": int64(200)}, 200, true},
		{"stable outranks legacy", map[string]any{"http.response.status_code": int64(503), "http.status_code": int64(200)}, 503, true},
		{"lowest", map[string]any{"http.response.status_code": int64(100)}, 100, true},
		{"highest", map[string]any{"http.response.status_code": int64(599)}, 599, true},
		{"below range", map[string]any{"http.response.status_code": int64(99)}, 0, false},
		{"above range", map[string]any{"http.response.status_code": int64(600)}, 0, false},
		{"zero", map[string]any{"http.response.status_code": int64(0)}, 0, false},
		{"string", map[string]any{"http.response.status_code": "200"}, 0, false},
		{"malformed stable key is absent, legacy not consulted",
			map[string]any{"http.response.status_code": "x", "http.status_code": int64(200)}, 0, false},
		{"integral float", map[string]any{"http.status_code": float64(404)}, 404, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := semconv.ReadHTTPStatusCode(tt.attrs)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ReadHTTPStatusCode() = %d, %v; want %d, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestUsageAndContentKeysAreDisjoint: a usage key is metadata about a call,
// and a content key is what the call said. No key may be both, or reading
// usage would be reading content.
func TestUsageAndContentKeysAreDisjoint(t *testing.T) {
	content := semconv.ContentAttributes()
	for _, key := range semconv.UsageAttributes() {
		if slices.Contains(content, key) {
			t.Errorf("%q is both a usage key and a content key", key)
		}
	}
}

// TestUsageIsNotIdentity: the usage and status keys establish nothing in the
// convention table — no category, name, target, actor or session.
func TestUsageIsNotIdentity(t *testing.T) {
	attrs := map[string]any{}
	for _, key := range semconv.UsageAttributes() {
		attrs[key] = int64(42)
	}
	if got := semconv.Normalize(semconv.Span{Kind: semconv.KindClient, Attributes: attrs}); got != (semconv.Normalized{}) {
		t.Fatalf("usage keys established %+v", got)
	}
}

func TestReadUsageDoesNotModifyItsInput(t *testing.T) {
	attrs := map[string]any{"llm.token_count.total": int64(3)}
	semconv.ReadUsage(attrs)
	semconv.ReadHTTPStatusCode(attrs)
	if len(attrs) != 1 || attrs["llm.token_count.total"] != int64(3) {
		t.Fatalf("attrs changed: %v", attrs)
	}
}

func BenchmarkReadUsage(b *testing.B) {
	attrs := map[string]any{
		"gen_ai.operation.name":      "chat",
		"gen_ai.request.model":       "llama3.2",
		"gen_ai.usage.input_tokens":  int64(120),
		"gen_ai.usage.output_tokens": int64(30),
		"http.response.status_code":  int64(200),
	}
	b.ReportAllocs()
	for b.Loop() {
		semconv.ReadUsage(attrs)
		semconv.ReadHTTPStatusCode(attrs)
	}
}
