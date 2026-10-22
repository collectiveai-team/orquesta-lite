package cost

import "strings"

// price is a model's per-million-token rate for each way a provider bills
// tokens. Cache reads cost about a tenth of fresh input, and they are almost
// all of an agent's input: every turn re-sends the whole conversation, which
// the provider serves from its prompt cache. Pricing them as fresh input made
// the workflow cost budget read ~5x what a run actually spent.
//
// Claude's cache-write rate is the 1-hour TTL one (2x input), the TTL Claude
// Code writes with; OpenAI and Gemini do not bill writes separately, so theirs
// is the input rate. Long-context tiers are ignored. Rates are from LiteLLM's
// pricing table.
type price struct {
	InputPerMillion      float64
	OutputPerMillion     float64
	CacheReadPerMillion  float64
	CacheWritePerMillion float64
}

var embeddedPrices = map[string]price{
	"claude-sonnet-4":   {InputPerMillion: 3.00, OutputPerMillion: 15.00, CacheReadPerMillion: 0.30, CacheWritePerMillion: 6.00},
	"claude-sonnet-4-6": {InputPerMillion: 3.00, OutputPerMillion: 15.00, CacheReadPerMillion: 0.30, CacheWritePerMillion: 6.00},
	"claude-sonnet-5":   {InputPerMillion: 2.00, OutputPerMillion: 10.00, CacheReadPerMillion: 0.20, CacheWritePerMillion: 4.00},
	"claude-opus-4":     {InputPerMillion: 15.00, OutputPerMillion: 75.00, CacheReadPerMillion: 1.50, CacheWritePerMillion: 30.00},
	"claude-opus-4-8":   {InputPerMillion: 5.00, OutputPerMillion: 25.00, CacheReadPerMillion: 0.50, CacheWritePerMillion: 10.00},
	"claude-opus-5":     {InputPerMillion: 5.00, OutputPerMillion: 25.00, CacheReadPerMillion: 0.50, CacheWritePerMillion: 10.00},
	"claude-opus-5-5":   {InputPerMillion: 4.00, OutputPerMillion: 20.00, CacheReadPerMillion: 0.20, CacheWritePerMillion: 8.00},
	"gemini-2.5-pro":    {InputPerMillion: 1.25, OutputPerMillion: 10.00, CacheReadPerMillion: 0.125, CacheWritePerMillion: 1.25},
	"gemini-2.5-flash":  {InputPerMillion: 0.30, OutputPerMillion: 2.50, CacheReadPerMillion: 0.03, CacheWritePerMillion: 0.30},
	"gpt-5":             {InputPerMillion: 1.25, OutputPerMillion: 10.00, CacheReadPerMillion: 0.125, CacheWritePerMillion: 1.25},
	"gpt-5.6-sol":       {InputPerMillion: 4.00, OutputPerMillion: 20.00, CacheReadPerMillion: 0.40, CacheWritePerMillion: 5.00},
	"gpt-5.6-terra":     {InputPerMillion: 2.00, OutputPerMillion: 12.00, CacheReadPerMillion: 0.20, CacheWritePerMillion: 2.50},
}

// Longest prefix first: claude-sonnet-5 must win over claude-sonnet-4 for a
// dated snapshot id, claude-opus-4-8 over claude-opus-4, and claude-opus-5-5
// over claude-opus-5. Only models with a known rate are listed: a bare "gpt-5"
// prefix would silently price every future gpt-5.x at another model's rate,
// and a guessed number is worse than none because the result feeds the
// workflow cost budget.
var embeddedPricePrefixes = []string{
	"claude-sonnet-4-6",
	"claude-sonnet-5",
	"claude-sonnet-4",
	"claude-opus-4-8",
	"claude-opus-5-5",
	"claude-opus-5",
	"claude-opus-4",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
}

// Usage is one invocation's tokens split by how they are billed. Input is
// fresh input only: every provider adapter reports cache reads and cache
// writes outside input_tokens.
type Usage struct {
	Input      int
	CacheRead  int
	CacheWrite int
	Output     int
}

func (u Usage) empty() bool {
	return u.Input == 0 && u.CacheRead == 0 && u.CacheWrite == 0 && u.Output == 0
}

// EstimateUsageUSD prices a token breakdown against the embedded table. It is
// what the workflow cost budget is checked against, and the cost report's
// fallback when agtop cannot price a session. ok is false when the model is
// unknown or there are no tokens.
func EstimateUsageUSD(model string, u Usage) (float64, bool) {
	if u.empty() {
		return 0, false
	}
	p, ok := priceForModel(model)
	if !ok {
		return 0, false
	}
	perMillion := func(tokens int, rate float64) float64 {
		return float64(tokens) / 1_000_000 * rate
	}
	return perMillion(u.Input, p.InputPerMillion) +
		perMillion(u.CacheRead, p.CacheReadPerMillion) +
		perMillion(u.CacheWrite, p.CacheWritePerMillion) +
		perMillion(u.Output, p.OutputPerMillion), true
}

// EstimateUSD prices fresh input and output tokens only. Callers that know the
// cache split must use EstimateUsageUSD: folding cached tokens into
// inputTokens prices them at the full input rate.
func EstimateUSD(model string, inputTokens, outputTokens int) (float64, bool) {
	return EstimateUsageUSD(model, Usage{Input: inputTokens, Output: outputTokens})
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
