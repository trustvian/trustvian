package cirender

import (
	"os"
	"testing"
)

// BenchmarkRenderReport renders the golden model-change report: parse,
// validate and render, as eval report --format md and the comment do.
func BenchmarkRenderReport(b *testing.B) {
	raw, err := os.ReadFile("../../cmd/trustvian-ci-render/testdata/golden/v2-model-change.json")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		if out := RenderDocument(raw, DefaultLimits); out.State != StateVerdict {
			b.Fatal(out.Reason)
		}
	}
}
