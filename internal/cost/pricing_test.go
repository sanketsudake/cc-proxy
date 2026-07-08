package cost

import (
	"math"
	"testing"

	"github.com/sanketsudake/claude-agent-proxy/internal/config"
	"github.com/sanketsudake/claude-agent-proxy/internal/sse"
)

func TestEstimate(t *testing.T) {
	e := NewEstimator(nil)
	// Sonnet 5: $3 in, $15 out, cache read 0.3, cache write 3.75 per MTok.
	got, ok := e.Estimate("claude-sonnet-5", sse.Usage{
		InputTokens: 1_000_000, OutputTokens: 1_000_000,
		CacheReadTokens: 1_000_000, CacheCreationTokens: 1_000_000,
	})
	if !ok {
		t.Fatal("model should be known")
	}
	want := 3.0 + 15.0 + 0.3 + 3.75
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("cost = %f, want %f", got, want)
	}
}

func TestEstimateLongestPrefixWins(t *testing.T) {
	e := NewEstimator(nil)
	// claude-sonnet-5 must match "claude-sonnet-5", not "claude-sonnet-4".
	five, _ := e.Estimate("claude-sonnet-5", sse.Usage{InputTokens: 1_000_000})
	four, _ := e.Estimate("claude-sonnet-4-6", sse.Usage{InputTokens: 1_000_000})
	if five != 3.0 || four != 3.0 {
		t.Errorf("sonnet pricing: 5=%f 4.6=%f", five, four)
	}
	fable, _ := e.Estimate("claude-fable-5", sse.Usage{InputTokens: 1_000_000})
	if fable != 10.0 {
		t.Errorf("fable input = %f", fable)
	}
}

func TestEstimateUnknownModel(t *testing.T) {
	e := NewEstimator(nil)
	if _, ok := e.Estimate("gpt-6", sse.Usage{InputTokens: 100}); ok {
		t.Error("unknown model should return ok=false")
	}
}

func TestEstimateOverride(t *testing.T) {
	e := NewEstimator(map[string]config.ModelPricing{
		"claude-opus-4": {Input: 100, Output: 200},
	})
	got, ok := e.Estimate("claude-opus-4-8", sse.Usage{InputTokens: 1_000_000})
	if !ok || got != 100 {
		t.Errorf("override: got=%f ok=%v", got, ok)
	}
}
