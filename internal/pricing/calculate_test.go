package pricing

import (
	"math"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestCalculateUsageCostGPT56Buckets(t *testing.T) {
	service := NewService()
	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-5.6",
		Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
			100_000,
			20_000,
			10_000,
			5_000,
			0,
			105_000,
		)},
	}

	billing := service.CalculateUsageCost(record)
	if !billing.Priced {
		t.Fatalf("billing = %+v, want priced", billing)
	}
	assertFloatClose(t, billing.Breakdown.InputUSD, 0.35)
	assertFloatClose(t, billing.Breakdown.OutputUSD, 0.15)
	assertFloatClose(t, billing.Breakdown.CacheReadUSD, 0.01)
	assertFloatClose(t, billing.Breakdown.CacheWriteUSD, 0.0625)
	assertFloatClose(t, billing.TotalUSD, 0.5725)
	if billing.Pricing.MatchedModel != "gpt-5.6" || billing.Pricing.ServiceTier != "standard" {
		t.Fatalf("pricing = %+v", billing.Pricing)
	}
}

func TestCalculateUsageCostMarksCodexOAuthCacheWriteAsUnreported(t *testing.T) {
	service := NewService()
	billing := service.CalculateUsageCost(coreusage.Record{
		Provider: "codex",
		AuthType: "oauth",
		Model:    "gpt-5.6-luna",
		Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
			5_347, 0, 0, 5, 0, 5_352,
		)},
	})
	if !billing.Priced || !billing.Pricing.Estimated {
		t.Fatalf("billing = %+v, want priced estimate", billing)
	}
	if billing.Reason != "cache_write_tokens_unreported" {
		t.Fatalf("reason = %q, want cache_write_tokens_unreported", billing.Reason)
	}
	if billing.Breakdown.CacheWriteUSD != 0 {
		t.Fatalf("cache write cost = %f, want zero without fabricated tokens", billing.Breakdown.CacheWriteUSD)
	}
}

func TestCalculateUsageCostDoesNotMarkOpenAIAPIUsageAsUnreported(t *testing.T) {
	service := NewService()
	billing := service.CalculateUsageCost(coreusage.Record{
		Provider: "openai",
		AuthType: "apikey",
		Model:    "gpt-5.6",
		Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
			1_000, 0, 0, 100, 0, 1_100,
		)},
	})
	if billing.Reason == "cache_write_tokens_unreported" {
		t.Fatalf("unexpected reason for OpenAI API usage: %+v", billing)
	}
}

func TestCalculateUsageCostGPT56ServiceTiers(t *testing.T) {
	service := NewService()
	tests := []struct {
		tier       string
		wantInput  float64
		wantOutput float64
		wantTotal  float64
	}{
		{tier: "standard", wantInput: 5, wantOutput: 30, wantTotal: 0.008},
		{tier: "flex", wantInput: 2.5, wantOutput: 15, wantTotal: 0.004},
		{tier: "priority", wantInput: 10, wantOutput: 60, wantTotal: 0.016},
		{tier: "batch", wantInput: 2.5, wantOutput: 15, wantTotal: 0.004},
	}
	for _, tt := range tests {
		t.Run(tt.tier, func(t *testing.T) {
			billing := service.CalculateUsageCost(coreusage.Record{
				Provider:    "openai",
				Model:       "gpt-5.6",
				ServiceTier: tt.tier,
				Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
					1_000, 0, 0, 100, 0, 1_100,
				)},
			})
			if !billing.Priced {
				t.Fatalf("billing = %+v, want priced", billing)
			}
			assertFloatClose(t, billing.Pricing.UnitPricesUSDPerMillion.Input, tt.wantInput)
			assertFloatClose(t, billing.Pricing.UnitPricesUSDPerMillion.Output, tt.wantOutput)
			assertFloatClose(t, billing.TotalUSD, tt.wantTotal)
		})
	}
}

func TestCalculateUsageCostGPT56LongContext(t *testing.T) {
	service := NewService()
	billing := service.CalculateUsageCost(coreusage.Record{
		Provider:    "openai",
		Model:       "gpt-5.6",
		ServiceTier: "flex",
		Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
			300_000, 0, 0, 10_000, 0, 310_000,
		)},
	})
	if !billing.Priced {
		t.Fatalf("billing = %+v, want priced", billing)
	}
	if billing.Pricing.ContextThresholdTokens != 272_000 {
		t.Fatalf("context threshold = %d, want 272000", billing.Pricing.ContextThresholdTokens)
	}
	assertFloatClose(t, billing.Pricing.UnitPricesUSDPerMillion.Input, 5)
	assertFloatClose(t, billing.Pricing.UnitPricesUSDPerMillion.Output, 22.5)
	assertFloatClose(t, billing.TotalUSD, 1.725)
}

func TestCalculateUsageCostUsesResponseTier(t *testing.T) {
	service := NewService()
	billing := service.CalculateUsageCost(coreusage.Record{
		Provider:            "openai",
		Model:               "gpt-5.6",
		ServiceTier:         "flex",
		ResponseServiceTier: "priority",
		Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
			1_000, 0, 0, 100, 0, 1_100,
		)},
	})
	if billing.Pricing.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want priority", billing.Pricing.ServiceTier)
	}
	assertFloatClose(t, billing.TotalUSD, 0.016)
}

func TestCalculateUsageCostCustomOverride(t *testing.T) {
	input, output, cacheRead, cacheWrite := 1.0, 2.0, 0.1, 1.25
	service := NewService()
	baseVersion := service.Status().Version
	service.Configure(Options{
		Enabled: true,
		Overrides: map[string]config.PricingOverride{
			"custom-model": {
				Provider:   "openai",
				Input:      &input,
				Output:     &output,
				CacheRead:  &cacheRead,
				CacheWrite: &cacheWrite,
			},
		},
	})
	billing := service.CalculateUsageCost(coreusage.Record{
		Provider: "openai",
		Model:    "custom-model",
		Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
			1_000, 200, 100, 500, 0, 1_500,
		)},
	})
	if !billing.Priced || billing.Pricing.Source != "custom+embedded" {
		t.Fatalf("billing = %+v", billing)
	}
	if billing.Pricing.Version == baseVersion || billing.Pricing.Version == "" {
		t.Fatalf("override version = %q, base = %q", billing.Pricing.Version, baseVersion)
	}
	assertFloatClose(t, billing.TotalUSD, 0.001845)
}

func TestCalculateUsageCostEstimatesMissingOpenAICachePrices(t *testing.T) {
	service := NewService()
	if errApply := service.applyCatalog([]byte(`{
		"test-openai": {
			"litellm_provider": "openai",
			"input_cost_per_token": 0.000002,
			"output_cost_per_token": 0.000004
		}
	}`), "test"); errApply != nil {
		t.Fatalf("applyCatalog() error = %v", errApply)
	}
	billing := service.CalculateUsageCost(coreusage.Record{
		Provider: "openai",
		Model:    "test-openai",
		Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
			1_000, 200, 100, 100, 0, 1_100,
		)},
	})
	if !billing.Priced || !billing.Pricing.Estimated {
		t.Fatalf("billing = %+v, want estimated price", billing)
	}
	assertFloatClose(t, billing.Pricing.UnitPricesUSDPerMillion.CacheRead, 0.2)
	assertFloatClose(t, billing.Pricing.UnitPricesUSDPerMillion.CacheWrite, 2.5)
	assertFloatClose(t, billing.TotalUSD, 0.00209)
}

func TestCalculateUsageCostDerivesCombinedTierAndContextPrice(t *testing.T) {
	service := NewService()
	if errApply := service.applyCatalog([]byte(`{
		"derived-model": {
			"litellm_provider": "openai",
			"input_cost_per_token": 0.000002,
			"input_cost_per_token_above_200k_tokens": 0.000004,
			"input_cost_per_token_flex": 0.000001,
			"output_cost_per_token": 0.000010,
			"output_cost_per_token_above_200k_tokens": 0.000015,
			"output_cost_per_token_flex": 0.000005
		}
	}`), "test"); errApply != nil {
		t.Fatalf("applyCatalog() error = %v", errApply)
	}
	billing := service.CalculateUsageCost(coreusage.Record{
		Provider:    "openai",
		Model:       "derived-model",
		ServiceTier: "flex",
		Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
			300_000, 0, 0, 10_000, 0, 310_000,
		)},
	})
	if !billing.Priced || !billing.Pricing.Estimated {
		t.Fatalf("billing = %+v, want derived estimate", billing)
	}
	assertFloatClose(t, billing.Pricing.UnitPricesUSDPerMillion.Input, 2)
	assertFloatClose(t, billing.Pricing.UnitPricesUSDPerMillion.Output, 7.5)
	assertFloatClose(t, billing.TotalUSD, 0.675)
}

func TestCalculateUsageCostRejectsUnknownOrIncompleteUsage(t *testing.T) {
	service := NewService()
	unknown := service.CalculateUsageCost(coreusage.Record{
		Provider: "openai",
		Model:    "missing-model",
		Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
			10, 0, 0, 2, 0, 12,
		)},
	})
	if unknown.Priced || unknown.Reason != "model_price_not_found" || unknown.TotalUSD != 0 {
		t.Fatalf("unknown billing = %+v", unknown)
	}

	incomplete := service.CalculateUsageCost(coreusage.Record{
		Provider: "plugin-provider",
		Model:    "gpt-5.6",
		Detail:   coreusage.Detail{TotalTokens: 12},
	})
	if incomplete.Priced || incomplete.Reason != "incomplete_token_breakdown" || incomplete.TotalUSD != 0 {
		t.Fatalf("incomplete billing = %+v", incomplete)
	}
}

func assertFloatClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("value = %.15f, want %.15f", got, want)
	}
}
