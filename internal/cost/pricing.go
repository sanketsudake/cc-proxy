// Package cost estimates request cost in USD from token usage and a
// per-model pricing table. Prices drift — the table is overridable via
// config, and every consumer should label the number as an estimate.
package cost

import (
	"maps"
	"sort"
	"strings"

	"github.com/sanketsudake/cc-proxy/internal/config"
	"github.com/sanketsudake/cc-proxy/internal/sse"
)

// pricing is USD per million tokens.
type pricing struct {
	input, output, cacheRead, cacheWrite float64
}

// defaults are matched by model-ID prefix, longest prefix first.
// Cache read defaults to 0.1x input; cache write (5m TTL) to 1.25x input.
// Verified against Anthropic pricing as of 2026-07.
var defaults = map[string]pricing{
	"claude-fable-5":  {input: 10, output: 50},
	"claude-mythos-5": {input: 10, output: 50},
	"claude-opus-4":   {input: 5, output: 25},
	"claude-opus-3":   {input: 15, output: 75},
	"claude-sonnet-5": {input: 3, output: 15},
	"claude-sonnet-4": {input: 3, output: 15},
	"claude-haiku-4":  {input: 1, output: 5},
	"claude-3-haiku":  {input: 0.25, output: 1.25},
}

// Estimator resolves model prices with optional config overrides.
type Estimator struct {
	table    map[string]pricing
	prefixes []string // sorted longest-first for prefix matching
}

// NewEstimator merges config overrides (same prefix-key scheme) over defaults.
func NewEstimator(overrides map[string]config.ModelPricing) *Estimator {
	table := make(map[string]pricing, len(defaults)+len(overrides))
	maps.Copy(table, defaults)
	for k, v := range overrides {
		table[k] = pricing{input: v.Input, output: v.Output, cacheRead: v.CacheRead, cacheWrite: v.CacheWrite}
	}
	prefixes := make([]string, 0, len(table))
	for k := range table {
		prefixes = append(prefixes, k)
	}
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })
	return &Estimator{table: table, prefixes: prefixes}
}

// Estimate returns the estimated USD cost and whether the model was known.
func (e *Estimator) Estimate(model string, u sse.Usage) (float64, bool) {
	var p pricing
	found := false
	for _, prefix := range e.prefixes {
		if strings.HasPrefix(model, prefix) {
			p = e.table[prefix]
			found = true
			break
		}
	}
	if !found {
		return 0, false
	}
	cacheRead := p.cacheRead
	if cacheRead == 0 {
		cacheRead = p.input * 0.1
	}
	cacheWrite := p.cacheWrite
	if cacheWrite == 0 {
		cacheWrite = p.input * 1.25
	}
	const mtok = 1_000_000
	cost := float64(u.InputTokens)*p.input/mtok +
		float64(u.OutputTokens)*p.output/mtok +
		float64(u.CacheReadTokens)*cacheRead/mtok +
		float64(u.CacheCreationTokens)*cacheWrite/mtok
	return cost, true
}
