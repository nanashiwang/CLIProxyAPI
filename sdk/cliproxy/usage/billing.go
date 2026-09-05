package usage

import (
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// CostBreakdown contains mutually exclusive USD cost buckets.
type CostBreakdown struct {
	InputUSD      float64 `json:"input_usd"`
	OutputUSD     float64 `json:"output_usd"`
	CacheReadUSD  float64 `json:"cache_read_usd"`
	CacheWriteUSD float64 `json:"cache_write_usd"`
}

// UnitPrices contains the USD price per million tokens used for each bucket.
type UnitPrices struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// PricingSnapshot records the exact pricing decision used for a request.
type PricingSnapshot struct {
	Version                 string     `json:"version"`
	Source                  string     `json:"source"`
	MatchedModel            string     `json:"matched_model"`
	MatchedProvider         string     `json:"matched_provider,omitempty"`
	ServiceTier             string     `json:"service_tier"`
	ContextThresholdTokens  int64      `json:"context_threshold_tokens,omitempty"`
	UnitPricesUSDPerMillion UnitPrices `json:"unit_prices_usd_per_million_tokens"`
	Estimated               bool       `json:"estimated"`
	CalculatedAt            time.Time  `json:"calculated_at"`
}

// Billing contains the USD cost calculated for one usage record.
type Billing struct {
	Currency  string          `json:"currency"`
	Priced    bool            `json:"priced"`
	Reason    string          `json:"reason,omitempty"`
	TotalUSD  float64         `json:"total_usd"`
	Breakdown CostBreakdown   `json:"breakdown"`
	Pricing   PricingSnapshot `json:"pricing"`
}

// BillingCalculator calculates a stable price snapshot for a usage record.
type BillingCalculator interface {
	CalculateUsageCost(record Record) Billing
}

var billingCalculatorState struct {
	sync.RWMutex
	calculator BillingCalculator
}

// SetBillingCalculator installs the process-wide usage billing calculator.
func SetBillingCalculator(calculator BillingCalculator) {
	billingCalculatorState.Lock()
	billingCalculatorState.calculator = calculator
	billingCalculatorState.Unlock()
}

func calculateBilling(record Record) (billing Billing) {
	billingCalculatorState.RLock()
	calculator := billingCalculatorState.calculator
	billingCalculatorState.RUnlock()
	if calculator == nil {
		return Billing{Currency: "USD", Priced: false, Reason: "pricing_calculator_unavailable"}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.WithField("panic", recovered).Error("usage: billing calculator panic recovered")
			billing = Billing{Currency: "USD", Priced: false, Reason: "calculator_panic"}
		}
	}()
	return calculator.CalculateUsageCost(record)
}
