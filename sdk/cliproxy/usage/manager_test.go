package usage

import (
	"context"
	"testing"
	"time"
)

func TestGenerateEnabledDefaultsNilToTrue(t *testing.T) {
	if !GenerateEnabled(nil) {
		t.Fatalf("GenerateEnabled(nil) = false, want true")
	}
}

func TestGenerateEnabledHonorsExplicitFalse(t *testing.T) {
	if GenerateEnabled(GenerateFlag(false)) {
		t.Fatalf("GenerateEnabled(false) = true, want false")
	}
}

func TestGenerateEnabledHonorsExplicitTrue(t *testing.T) {
	if !GenerateEnabled(GenerateFlag(true)) {
		t.Fatalf("GenerateEnabled(true) = false, want true")
	}
}

func TestGenerateFromContextDefaultsMissingToTrue(t *testing.T) {
	if !GenerateFromContext(context.Background()) {
		t.Fatalf("GenerateFromContext(background) = false, want true")
	}
}

func TestGenerateFromContextHonorsExplicitFalse(t *testing.T) {
	ctx := WithGenerate(context.Background(), false)
	if GenerateFromContext(ctx) {
		t.Fatalf("GenerateFromContext(false) = true, want false")
	}
}

func TestRecordOmittedGenerateIsEnabled(t *testing.T) {
	// Existing callers construct Record without setting Generate.
	// Omission must remain distinguishable from explicit false and default to true.
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	}
	if record.Generate != nil {
		t.Fatalf("Record.Generate = %v, want nil for omitted field", record.Generate)
	}
	if !GenerateEnabled(record.Generate) {
		t.Fatalf("GenerateEnabled(omitted) = false, want true")
	}
}

func TestManagerPublishCalculatesBillingFromNormalizedBreakdown(t *testing.T) {
	withBillingCalculator(t, billingCalculatorFunc(func(record Record) Billing {
		if !record.Detail.TokenBreakdown.Valid() || record.Detail.TokenBreakdown.Quality != TokenAccountingQualityComplete {
			t.Fatalf("calculator detail = %+v", record.Detail)
		}
		return Billing{
			Currency: "USD",
			Priced:   true,
			TotalUSD: 0.125,
			Pricing: PricingSnapshot{
				Version:      "test-version",
				CalculatedAt: time.Now(),
			},
		}
	}))

	manager := NewManager(4)
	defer manager.Stop()
	records := make(chan Record, 1)
	manager.Register(pluginFuncForManagerTest(func(_ context.Context, record Record) {
		records <- record
	}))
	manager.Publish(context.Background(), Record{
		Provider: "openai",
		Model:    "test-model",
		Detail: Detail{
			InputTokens:  10,
			OutputTokens: 2,
			TotalTokens:  12,
		},
	})

	select {
	case record := <-records:
		if !record.Billing.Priced || record.Billing.TotalUSD != 0.125 || record.Billing.Pricing.Version != "test-version" {
			t.Fatalf("billing = %+v", record.Billing)
		}
		if !record.Detail.TokenBreakdown.Valid() {
			t.Fatalf("detail = %+v", record.Detail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

func TestCalculateBillingWithoutCalculatorReturnsUnpricedReason(t *testing.T) {
	withBillingCalculator(t, nil)
	billing := calculateBilling(Record{})
	if billing.Priced || billing.Currency != "USD" || billing.Reason != "pricing_calculator_unavailable" {
		t.Fatalf("billing = %+v", billing)
	}
}

type billingCalculatorFunc func(Record) Billing

func (fn billingCalculatorFunc) CalculateUsageCost(record Record) Billing {
	return fn(record)
}

type pluginFuncForManagerTest func(context.Context, Record)

func (fn pluginFuncForManagerTest) HandleUsage(ctx context.Context, record Record) {
	fn(ctx, record)
}

func withBillingCalculator(t *testing.T, calculator BillingCalculator) {
	t.Helper()
	billingCalculatorState.RLock()
	previous := billingCalculatorState.calculator
	billingCalculatorState.RUnlock()
	SetBillingCalculator(calculator)
	t.Cleanup(func() { SetBillingCalculator(previous) })
}
