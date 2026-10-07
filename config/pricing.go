package config

// The pricing table (task 087): an operator-supplied, versioned list of model
// prices, so a comparison can state what its token counts cost.
//
//	version: 1
//	pricing_version: "team-2026-10"
//	source: "copied from provider pricing page, 2026-10-01"
//	currency: USD
//	models:
//	  - model: llama3.2
//	    input_micros_per_million_tokens: 0
//	    output_micros_per_million_tokens: 0
//
// Prices are integer micro-units of the currency per million tokens, so cost is
// integer arithmetic and the same records always cost the same amount; a
// floating-point price would make a cost depend on the order it was summed in.
// Every field is required and nothing has a default: a model priced at zero is
// a statement, so zero cannot also mean "unset". Unknown fields are refused.
//
// Trustvian bundles no prices. A cost figure is only as good as this file, which
// is why every figure carries the file's pricing_version, source and digest.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"

	yaml "go.yaml.in/yaml/v3"
)

// PricingSchemaVersionV1 is the only pricing schema version this build reads.
const PricingSchemaVersionV1 = 1

// Pricing bounds.
const (
	MaxPricedModels        = 256
	maxPricingModelLen     = 255
	maxPricingVersionLen   = 128
	maxPricingSourceLen    = 512
	pricingDigestAlgorithm = "sha256"
)

// ErrInvalidPricing reports a pricing file that cannot be used as written. The
// message names the field.
var ErrInvalidPricing = errors.New("config: invalid pricing")

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// PricingConfig is one parsed, validated pricing file.
type PricingConfig struct {
	Version        *int           `yaml:"version"`
	PricingVersion string         `yaml:"pricing_version"`
	Source         string         `yaml:"source"`
	Currency       string         `yaml:"currency"`
	Models         []ModelPricing `yaml:"models"`
}

// ModelPricing is one model's price. Pointers, so an omitted price is told
// apart from an explicit zero.
type ModelPricing struct {
	Model                        string  `yaml:"model"`
	InputMicrosPerMillionTokens  *uint64 `yaml:"input_micros_per_million_tokens"`
	OutputMicrosPerMillionTokens *uint64 `yaml:"output_micros_per_million_tokens"`
}

func invalidPricing(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidPricing}, args...)...)
}

// Validate refuses a table no comparison can use.
func (p PricingConfig) Validate() error {
	if p.Version == nil {
		return invalidPricing("version is required")
	}
	if *p.Version != PricingSchemaVersionV1 {
		return invalidPricing("version %d is not supported; this build reads %d", *p.Version, PricingSchemaVersionV1)
	}
	for _, f := range []struct {
		name, value string
		max         int
	}{
		{"pricing_version", p.PricingVersion, maxPricingVersionLen},
		{"source", p.Source, maxPricingSourceLen},
	} {
		if f.value == "" {
			return invalidPricing("%s is required", f.name)
		}
		if len(f.value) > f.max || !printable(f.value) {
			return invalidPricing("%s must be printable text of at most %d bytes", f.name, f.max)
		}
	}
	if !currencyPattern.MatchString(p.Currency) {
		return invalidPricing("currency %q must be a three-letter upper-case code", p.Currency)
	}
	if len(p.Models) == 0 || len(p.Models) > MaxPricedModels {
		return invalidPricing("models must list 1..%d models, got %d", MaxPricedModels, len(p.Models))
	}
	seen := make(map[string]struct{}, len(p.Models))
	for i, m := range p.Models {
		if m.Model == "" || len(m.Model) > maxPricingModelLen || !printable(m.Model) {
			return invalidPricing("models[%d].model must be printable text of 1..%d bytes", i, maxPricingModelLen)
		}
		if _, dup := seen[m.Model]; dup {
			return invalidPricing("model %q is priced twice", m.Model)
		}
		seen[m.Model] = struct{}{}
		if m.InputMicrosPerMillionTokens == nil || m.OutputMicrosPerMillionTokens == nil {
			return invalidPricing("model %q needs both input_ and output_micros_per_million_tokens", m.Model)
		}
	}
	return nil
}

func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Digest is "sha256:" and the hex SHA-256 of the table's canonical encoding:
// JSON of the validated values with models sorted by name. Formatting,
// comments and model order in the file do not change it; any value does. Call
// only on a validated table.
func (p PricingConfig) Digest() string {
	type canonicalModel struct {
		Model  string `json:"model"`
		Input  uint64 `json:"input_micros_per_million_tokens"`
		Output uint64 `json:"output_micros_per_million_tokens"`
	}
	models := make([]canonicalModel, 0, len(p.Models))
	for _, m := range p.Models {
		models = append(models, canonicalModel{m.Model, *m.InputMicrosPerMillionTokens, *m.OutputMicrosPerMillionTokens})
	}
	slices.SortFunc(models, func(a, b canonicalModel) int {
		switch {
		case a.Model < b.Model:
			return -1
		case a.Model > b.Model:
			return 1
		}
		return 0
	})
	canonical, err := json.Marshal(struct {
		Version        int              `json:"version"`
		PricingVersion string           `json:"pricing_version"`
		Source         string           `json:"source"`
		Currency       string           `json:"currency"`
		Models         []canonicalModel `json:"models"`
	}{*p.Version, p.PricingVersion, p.Source, p.Currency, models})
	if err != nil {
		// Unreachable: every field is a string or an integer.
		panic("config: encoding a validated pricing table: " + err.Error())
	}
	sum := sha256.Sum256(canonical)
	return pricingDigestAlgorithm + ":" + hex.EncodeToString(sum[:])
}

// LoadPricing decodes and validates one pricing document. Strict: unknown
// fields and duplicate keys are refused, and every price must be written as a
// YAML integer.
func LoadPricing(data []byte) (PricingConfig, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return PricingConfig{}, ErrEmptyInput
	}
	var cfg PricingConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return PricingConfig{}, ErrEmptyInput
		}
		return PricingConfig{}, fmt.Errorf("%w: decode: %w", ErrInvalidPricing, err)
	}
	if err := requireIntegerPrices(data); err != nil {
		return PricingConfig{}, err
	}
	if err := cfg.Validate(); err != nil {
		return PricingConfig{}, err
	}
	return cfg, nil
}

// LoadPricingFile reads the file at path, bounded like every other config file,
// and calls LoadPricing.
func LoadPricingFile(path string) (PricingConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return PricingConfig{}, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFileSize+1))
	if err != nil {
		return PricingConfig{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if len(data) > maxConfigFileSize {
		return PricingConfig{}, fmt.Errorf("%w: %s (max %d bytes)", ErrFileTooLarge, path, maxConfigFileSize)
	}
	cfg, err := LoadPricing(data)
	if err != nil {
		return PricingConfig{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// requireIntegerPrices refuses a version or price written as anything but a
// YAML integer, for the reason requireIntegerCounts gives: the decoder turns
// `12.5` into 12 and a too-large integer into a saturated value, and each is a
// price nobody wrote.
func requireIntegerPrices(data []byte) error {
	var raw struct {
		Version any `yaml:"version"`
		Models  []struct {
			Input  any `yaml:"input_micros_per_million_tokens"`
			Output any `yaml:"output_micros_per_million_tokens"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidPricing, err)
	}
	check := func(name string, v any) error {
		negative := false
		switch n := v.(type) {
		case nil, uint64:
		case int:
			negative = n < 0
		case int64:
			negative = n < 0
		default:
			return invalidPricing("%s must be written as an integer", name)
		}
		if negative {
			return invalidPricing("%s must not be negative", name)
		}
		return nil
	}
	if err := check("version", raw.Version); err != nil {
		return err
	}
	for i, m := range raw.Models {
		if err := check(fmt.Sprintf("models[%d].input_micros_per_million_tokens", i), m.Input); err != nil {
			return err
		}
		if err := check(fmt.Sprintf("models[%d].output_micros_per_million_tokens", i), m.Output); err != nil {
			return err
		}
	}
	return nil
}
