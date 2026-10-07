package main

import (
	"github.com/trustvian/trustvian/config"

	platform "trustvian-platform"
)

// loadPricing parses the table through config, the one package that reads
// YAML, and binds it with its digest.
func loadPricing(path string) (platform.Pricing, error) {
	cfg, err := config.LoadPricingFile(path)
	if err != nil {
		return platform.Pricing{}, err
	}
	models := make(map[string]platform.ModelPrice, len(cfg.Models))
	for _, m := range cfg.Models {
		models[m.Model] = platform.ModelPrice{
			InputMicrosPerMillion:  *m.InputMicrosPerMillionTokens,
			OutputMicrosPerMillion: *m.OutputMicrosPerMillionTokens,
		}
	}
	return platform.NewPricing(cfg.PricingVersion, cfg.Source, cfg.Currency, cfg.Digest(), models)
}
