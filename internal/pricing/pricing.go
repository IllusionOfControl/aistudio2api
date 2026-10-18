// Package pricing estimates request costs based on current Gemini API and Vertex AI published text pricing.
package pricing

// longContext is the input token threshold for long context pricing tiers; requests exceeding this threshold are billed at long context rates.
const longContext = 200_000

// price is the USD unit price per million tokens for a model; cached is the cached input unit price, long prefix indicates long context tier price, 0 means no tiering.
type price struct {
	input, cached, output             float64
	longInput, longCached, longOutput float64
}

// prices maps API model IDs to text generation prices; output unit price includes reasoning/thinking tokens.
var prices = map[string]price{
	"gemini-4-argon":                         {input: 2, cached: 0.10, output: 10},
	"gemini-3.8-flash":                       {input: 0.75, cached: 0.075, output: 3.75},
	"gemini-3.7-flash":                       {input: 0.75, cached: 0.075, output: 3.75},
	"gemini-3.6-flash":                       {input: 0.75, cached: 0.075, output: 3.75},
	"gemini-3.5-flash":                       {input: 1.50, cached: 0.15, output: 9},
	"gemini-3.5-flash-lite":                  {input: 0.30, cached: 0.03, output: 2.50},
	"gemini-3.1-pro-preview":                 {input: 2, cached: 0.20, output: 12, longInput: 4, longCached: 0.40, longOutput: 18},
	"gemini-3.1-pro-preview-customtools":     {input: 2, cached: 0.20, output: 12, longInput: 4, longCached: 0.40, longOutput: 18},
	"gemini-3.1-flash-lite":                  {input: 0.25, cached: 0.025, output: 1.50},
	"gemini-3-flash-preview":                 {input: 0.50, cached: 0.05, output: 3},
	"gemini-2.5-pro":                         {input: 1.25, cached: 0.125, output: 10, longInput: 2.50, longCached: 0.25, longOutput: 15},
	"gemini-2.5-flash":                       {input: 0.30, cached: 0.03, output: 2.50},
	"gemini-2.5-flash-lite":                  {input: 0.10, cached: 0.01, output: 0.40},
	"gemini-robotics-er-2-preview":           {input: 1, cached: 0.10, output: 5},
	"gemini-robotics-er-2-streaming-preview": {input: 1, cached: 0.10, output: 5},
}

// Priced returns whether a model has pricing.
func Priced(model string) bool {
	_, ok := prices[model]
	return ok
}

// Cost returns estimated USD cost for a request based on model pricing; cached is the cached portion of input tokens, output is the sum of thinking and reply tokens; returns 0 if model has no pricing.
func Cost(model string, input, cached, output int64) float64 {
	value := prices[model]
	if value.longInput > 0 && input > longContext {
		value.input, value.cached, value.output = value.longInput, value.longCached, value.longOutput
	}
	return (float64(input-cached)*value.input + float64(cached)*value.cached + float64(output)*value.output) / 1e6
}
