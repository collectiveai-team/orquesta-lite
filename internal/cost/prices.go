package cost

import "strings"

type price struct {
	InputPerMillion  float64
	OutputPerMillion float64
}

var embeddedPrices = map[string]price{
	"claude-sonnet-4":   {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"claude-sonnet-4-6": {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"claude-opus-4":     {InputPerMillion: 15.00, OutputPerMillion: 75.00},
	"claude-opus-4-8":   {InputPerMillion: 15.00, OutputPerMillion: 75.00},
	"gemini-2.5-pro":    {InputPerMillion: 1.25, OutputPerMillion: 10.00},
	"gemini-2.5-flash":  {InputPerMillion: 0.30, OutputPerMillion: 2.50},
	"gpt-5":             {InputPerMillion: 1.25, OutputPerMillion: 10.00},
}

var embeddedPricePrefixes = []string{
	"claude-sonnet-4-6",
	"claude-sonnet-4",
	"claude-opus-4-8",
	"claude-opus-4",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
}

func estimateUSD(model string, inputTokens, outputTokens int) (float64, bool) {
	if inputTokens == 0 && outputTokens == 0 {
		return 0, false
	}
	p, ok := priceForModel(model)
	if !ok {
		return 0, false
	}
	input := float64(inputTokens) / 1_000_000 * p.InputPerMillion
	output := float64(outputTokens) / 1_000_000 * p.OutputPerMillion
	return input + output, true
}

// EstimateUSD prices first-party token counts against the same embedded
// table the cost report uses. Shared with the eventdb query API so
// GET /api/cost and the query endpoints can never disagree on pricing.
// ok is false when the model is unknown or both counts are zero.
func EstimateUSD(model string, inputTokens, outputTokens int) (float64, bool) {
	return estimateUSD(model, inputTokens, outputTokens)
}

func priceForModel(model string) (price, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return price{}, false
	}
	if p, ok := embeddedPrices[model]; ok {
		return p, true
	}
	for _, prefix := range embeddedPricePrefixes {
		if strings.HasPrefix(model, prefix) {
			return embeddedPrices[prefix], true
		}
	}
	return price{}, false
}
