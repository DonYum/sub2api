//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Hydrogen thinking tiers must follow the base model's catalog price, so a
// price change on gemini-4-hydrogen also reaches -low/-medium/-high/-xhigh
// instead of those falling through to the hardcoded fallback rate.
func TestPricingService_Gemini4HydrogenThinkingTiersUseBasePricing(t *testing.T) {
	basePricing := &LiteLLMModelPricing{InputCostPerToken: 3e-6, OutputCostPerToken: 15e-6, CacheReadInputTokenCost: 0.15e-6}
	xhighPricing := &LiteLLMModelPricing{InputCostPerToken: 5e-6, OutputCostPerToken: 25e-6}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gemini-4-hydrogen": basePricing,
	}}
	for _, model := range []string{
		"gemini-4-hydrogen",
		"gemini-4-hydrogen-low",
		"gemini-4-hydrogen-medium",
		"gemini-4-hydrogen-high",
		"gemini-4-hydrogen-xhigh",
		"models/gemini-4-hydrogen-xhigh",
	} {
		t.Run(model, func(t *testing.T) {
			require.Same(t, basePricing, svc.GetModelPricing(model))
		})
	}

	// A tier priced on its own keeps its own entry.
	svc.pricingData["gemini-4-hydrogen-xhigh"] = xhighPricing
	require.Same(t, xhighPricing, svc.GetModelPricing("gemini-4-hydrogen-xhigh"))
	require.Same(t, basePricing, svc.GetModelPricing("gemini-4-hydrogen-high"))

	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gemini-4-hydrogen": basePricing,
	}})
	tokens := UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	for _, model := range []string{"gemini-4-hydrogen", "gemini-4-hydrogen-low", "gemini-4-hydrogen-medium", "gemini-4-hydrogen-high", "gemini-4-hydrogen-xhigh"} {
		t.Run("billed/"+model, func(t *testing.T) {
			cost, err := billing.CalculateCostWithServiceTier(model, tokens, 1, "")
			require.NoError(t, err)
			require.InDelta(t, 3.0, cost.InputCost, 1e-9)
			require.InDelta(t, 15.0, cost.OutputCost, 1e-9)
		})
	}
}
