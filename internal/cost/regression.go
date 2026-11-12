package cost

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// PricingVersion covers both rates and ordered model-resolution rules.
func PricingVersion() string {
	return pricingVersion(embeddedPrices, embeddedPricePrefixes, "trim-lower-exact-then-first-prefix/v1")
}

func pricingVersion(rates map[string]price, prefixes []string, resolver string) string {
	raw, _ := json.Marshal(struct {
		Rates    map[string]price `json:"rates"`
		Prefixes []string         `json:"prefixes"`
		Resolver string           `json:"resolver"`
	}{rates, prefixes, resolver})
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

// CacheReadRegressionUSD estimates the incremental cost had cached tokens been
// fresh input. It is a counterfactual and must not be added to actual spend.
func CacheReadRegressionUSD(model string, tokens int) (float64, bool) {
	p, ok := priceForModel(model)
	if !ok || tokens < 0 {
		return 0, false
	}
	return float64(tokens) * (p.InputPerMillion - p.CacheReadPerMillion) / 1_000_000, true
}
