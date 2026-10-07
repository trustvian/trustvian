package trustvianprocessor

import (
	"testing"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// BenchmarkAnnotationsOf splits the common case, a span that states no usage
// and no status code, from one that states all of them (task 087). Both read
// a few map keys and must not allocate.
func BenchmarkAnnotationsOf(b *testing.B) {
	cases := []struct {
		name  string
		attrs map[string]any
	}{
		{"no operational facts", map[string]any{
			event.AttrFidelity: "semantic", event.AttrLayer: "model",
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "llama3.2",
		}},
		{"usage and status", map[string]any{
			event.AttrFidelity: "semantic", event.AttrLayer: "model",
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "llama3.2",
			"gen_ai.usage.input_tokens": int64(120), "gen_ai.usage.output_tokens": int64(30),
			"http.response.status_code": int64(200),
		}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			result := trustvian.Result{Event: event.Event{Attributes: c.attrs}}
			b.ReportAllocs()
			for b.Loop() {
				annotationsOf(result)
			}
		})
	}
}
