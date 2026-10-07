package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const validPricing = `version: 1
pricing_version: "team-2026-10"
source: "copied from provider pricing page, 2026-10-01"
currency: USD
models:
  - model: llama3.2
    input_micros_per_million_tokens: 0
    output_micros_per_million_tokens: 0
  - model: gpt-x
    input_micros_per_million_tokens: 2500000
    output_micros_per_million_tokens: 10000000
`

func TestLoadPricing(t *testing.T) {
	cfg, err := LoadPricing([]byte(validPricing))
	if err != nil {
		t.Fatalf("LoadPricing() error = %v", err)
	}
	if cfg.PricingVersion != "team-2026-10" || len(cfg.Models) != 2 ||
		*cfg.Models[1].OutputMicrosPerMillionTokens != 10_000_000 {
		t.Fatalf("parsed %+v", cfg)
	}
	if d := cfg.Digest(); !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		t.Fatalf("digest %q", d)
	}
}

// TestPricingDigestIsCanonical: formatting, comments and model order do not
// move the digest; any value does.
func TestPricingDigestIsCanonical(t *testing.T) {
	base, err := LoadPricing([]byte(validPricing))
	if err != nil {
		t.Fatal(err)
	}
	reordered := `# a comment
version:   1
currency: USD
source: "copied from provider pricing page, 2026-10-01"
pricing_version: team-2026-10
models:
  - {model: gpt-x, output_micros_per_million_tokens: 10000000, input_micros_per_million_tokens: 2500000}
  - {model: llama3.2, input_micros_per_million_tokens: 0, output_micros_per_million_tokens: 0}
`
	same, err := LoadPricing([]byte(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if same.Digest() != base.Digest() {
		t.Fatalf("an equivalent table changed the digest: %s vs %s", same.Digest(), base.Digest())
	}
	changed, err := LoadPricing([]byte(strings.Replace(validPricing, "2500000", "2500001", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest() == base.Digest() {
		t.Fatal("a changed price kept the digest")
	}
}

func TestLoadPricingRefusals(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{"missing version", strings.Replace(validPricing, "version: 1\n", "", 1)},
		{"unsupported version", strings.Replace(validPricing, "version: 1", "version: 2", 1)},
		{"version as a float", strings.Replace(validPricing, "version: 1", "version: 1.0", 1)},
		{"a fractional price", strings.Replace(validPricing, "2500000", "2.5", 1)},
		{"a negative price", strings.Replace(validPricing, "2500000", "-1", 1)},
		{"a price as a string", strings.Replace(validPricing, "2500000", `"2500000"`, 1)},
		{"a missing price", strings.Replace(validPricing, "    output_micros_per_million_tokens: 10000000\n", "", 1)},
		{"a duplicate model", strings.Replace(validPricing, "model: gpt-x", "model: llama3.2", 1)},
		{"an empty model name", strings.Replace(validPricing, "model: gpt-x", `model: ""`, 1)},
		{"a lower-case currency", strings.Replace(validPricing, "currency: USD", "currency: usd", 1)},
		{"no source", strings.Replace(validPricing, `source: "copied from provider pricing page, 2026-10-01"`, `source: ""`, 1)},
		{"an unknown field", validPricing + "discount: 10\n"},
		{"no models", strings.Split(validPricing, "models:")[0] + "models: []\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadPricing([]byte(tt.doc)); !errors.Is(err, ErrInvalidPricing) {
				t.Fatalf("LoadPricing() error = %v, want ErrInvalidPricing", err)
			}
		})
	}
	if _, err := LoadPricing([]byte("  \n")); !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("empty input error = %v", err)
	}
	var many strings.Builder
	many.WriteString(strings.Split(validPricing, "models:")[0] + "models:\n")
	for i := range MaxPricedModels + 1 {
		fmt.Fprintf(&many, "  - {model: m%d, input_micros_per_million_tokens: 1, output_micros_per_million_tokens: 1}\n", i)
	}
	if _, err := LoadPricing([]byte(many.String())); !errors.Is(err, ErrInvalidPricing) {
		t.Fatalf("%d models error = %v, want ErrInvalidPricing", MaxPricedModels+1, err)
	}
}
