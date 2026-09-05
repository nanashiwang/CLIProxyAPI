package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

var contextPricePattern = regexp.MustCompile(`_above_(\d+)k_tokens`)

var explicitModelAliases = map[string]string{
	"claude-3-5-sonnet-latest": "claude-3-5-sonnet-20241022",
	"claude-3-5-haiku-latest":  "claude-3-5-haiku-20241022",
	"gpt-5.2-codex-max":        "gpt-5.2-codex",
}

type resolvedModel struct {
	name        string
	provider    string
	entry       modelEntry
	override    config.PricingOverride
	hasOverride bool
}

type selectedPrice struct {
	value     float64
	found     bool
	estimated bool
}

// CalculateUsageCost implements usage.BillingCalculator.
func (s *Service) CalculateUsageCost(record coreusage.Record) coreusage.Billing {
	calculatedAt := time.Now().UTC()
	tier := effectiveServiceTier(record)
	var version string
	var source string
	unpriced := func(reason string) coreusage.Billing {
		return coreusage.Billing{
			Currency: "USD",
			Priced:   false,
			Reason:   reason,
			Pricing: coreusage.PricingSnapshot{
				Version:      version,
				Source:       source,
				ServiceTier:  tier,
				CalculatedAt: calculatedAt,
			},
		}
	}
	if s == nil {
		return unpriced("pricing_service_unavailable")
	}

	s.mu.RLock()
	enabled := s.options.Enabled
	catalog := s.catalog
	overrides := cloneOverrides(s.options.Overrides)
	version = s.version
	source = s.activeSource
	s.mu.RUnlock()
	if !enabled {
		return unpriced("pricing_disabled")
	}

	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	breakdown := detail.TokenBreakdown
	if !breakdown.Valid() || breakdown.Quality != coreusage.TokenAccountingQualityComplete {
		return unpriced("incomplete_token_breakdown")
	}
	if breakdown.TotalTokens == 0 {
		return unpriced("no_billable_tokens")
	}

	resolved, okResolved := resolveModel(catalog, overrides, record.Provider, record.Model)
	if !okResolved {
		return unpriced("model_price_not_found")
	}
	unpricedResolved := func(reason string) coreusage.Billing {
		billing := unpriced(reason)
		billing.Pricing.MatchedModel = resolved.name
		billing.Pricing.MatchedProvider = resolved.provider
		return billing
	}

	inputTotal := breakdown.Input.TotalTokens
	threshold := resolveContextThreshold(resolved.entry, inputTotal)
	inputPrice := selectBucketPrice(resolved.entry, "input_cost_per_token", tier, threshold)
	outputPrice := selectBucketPrice(resolved.entry, "output_cost_per_token", tier, threshold)
	cacheReadPrice := selectBucketPrice(resolved.entry, "cache_read_input_token_cost", tier, threshold)
	cacheWritePrice := selectBucketPrice(resolved.entry, "cache_creation_input_token_cost", tier, threshold)

	if resolved.hasOverride {
		inputPrice = applyPriceOverride(inputPrice, resolved.override.Input)
		outputPrice = applyPriceOverride(outputPrice, resolved.override.Output)
		cacheReadPrice = applyPriceOverride(cacheReadPrice, resolved.override.CacheRead)
		cacheWritePrice = applyPriceOverride(cacheWritePrice, resolved.override.CacheWrite)
	}

	providerFamily := normalizeProviderFamily(record.Provider, resolved.provider, resolved.name)
	if breakdown.Input.CacheReadTokens > 0 && !cacheReadPrice.found && providerFamily == "openai" && inputPrice.found {
		cacheReadPrice = selectedPrice{value: inputPrice.value * 0.1, found: true, estimated: true}
	}
	if breakdown.Input.CacheWriteTokens > 0 && !cacheWritePrice.found && providerFamily == "openai" && inputPrice.found {
		cacheWritePrice = selectedPrice{value: inputPrice.value * 1.25, found: true, estimated: true}
	}

	if breakdown.Input.UncachedTokens > 0 && !inputPrice.found {
		return unpricedResolved("input_price_not_found")
	}
	if breakdown.Output.TotalTokens > 0 && !outputPrice.found {
		return unpricedResolved("output_price_not_found")
	}
	if breakdown.Input.CacheReadTokens > 0 && !cacheReadPrice.found {
		return unpricedResolved("cache_read_price_not_found")
	}
	if breakdown.Input.CacheWriteTokens > 0 && !cacheWritePrice.found {
		return unpricedResolved("cache_write_price_not_found")
	}

	costBreakdown := coreusage.CostBreakdown{
		InputUSD:      float64(breakdown.Input.UncachedTokens) * inputPrice.value,
		OutputUSD:     float64(breakdown.Output.TotalTokens) * outputPrice.value,
		CacheReadUSD:  float64(breakdown.Input.CacheReadTokens) * cacheReadPrice.value,
		CacheWriteUSD: float64(breakdown.Input.CacheWriteTokens) * cacheWritePrice.value,
	}
	total := costBreakdown.InputUSD + costBreakdown.OutputUSD + costBreakdown.CacheReadUSD + costBreakdown.CacheWriteUSD
	estimated := source == "embedded" || inputPrice.estimated || outputPrice.estimated || cacheReadPrice.estimated || cacheWritePrice.estimated
	reason := ""
	if providerFamily == "anthropic" && breakdown.Input.CacheWriteTokens > 0 && hasOneHourCachePrice(resolved.entry) {
		// The canonical usage schema currently has one cache-write bucket and cannot distinguish 5m from 1h writes.
		estimated = true
	}
	if codexOAuthCacheWriteUnreported(record, resolved.name, breakdown) {
		// The ChatGPT Codex OAuth backend currently reports zero cache-write tokens even
		// when a later identical request confirms that the cache was populated.
		estimated = true
		reason = "cache_write_tokens_unreported"
	}
	if resolved.hasOverride {
		source = "custom+" + source
		version = versionWithOverride(version, resolved.name, resolved.override)
	}

	return coreusage.Billing{
		Currency:  "USD",
		Priced:    true,
		Reason:    reason,
		TotalUSD:  total,
		Breakdown: costBreakdown,
		Pricing: coreusage.PricingSnapshot{
			Version:                version,
			Source:                 source,
			MatchedModel:           resolved.name,
			MatchedProvider:        resolved.provider,
			ServiceTier:            tier,
			ContextThresholdTokens: threshold,
			UnitPricesUSDPerMillion: coreusage.UnitPrices{
				Input:      inputPrice.value * 1_000_000,
				Output:     outputPrice.value * 1_000_000,
				CacheRead:  cacheReadPrice.value * 1_000_000,
				CacheWrite: cacheWritePrice.value * 1_000_000,
			},
			Estimated:    estimated,
			CalculatedAt: calculatedAt,
		},
	}
}

func codexOAuthCacheWriteUnreported(record coreusage.Record, resolvedModelName string, breakdown coreusage.TokenBreakdown) bool {
	if !strings.EqualFold(strings.TrimSpace(record.Provider), "codex") ||
		!strings.EqualFold(strings.TrimSpace(record.AuthType), "oauth") ||
		breakdown.Input.TotalTokens <= 0 ||
		breakdown.Input.CacheWriteTokens != 0 {
		return false
	}
	for _, model := range []string{resolvedModelName, record.Model, record.Alias} {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-5.6") {
			return true
		}
	}
	return false
}

func effectiveServiceTier(record coreusage.Record) string {
	tier := strings.ToLower(strings.TrimSpace(record.ResponseServiceTier))
	if tier == "" {
		tier = strings.ToLower(strings.TrimSpace(record.Detail.ResponseServiceTier))
	}
	if tier == "" {
		tier = strings.ToLower(strings.TrimSpace(record.ServiceTier))
	}
	if tier == "" {
		tier = strings.ToLower(strings.TrimSpace(record.RequestServiceTier))
	}
	switch tier {
	case "", "auto", "default", "standard":
		return "standard"
	case "batch", "batches":
		return "batch"
	default:
		return tier
	}
}

func versionWithOverride(version, model string, override config.PricingOverride) string {
	payload, errMarshal := json.Marshal(struct {
		CatalogVersion string                 `json:"catalog_version"`
		Model          string                 `json:"model"`
		Override       config.PricingOverride `json:"override"`
	}{
		CatalogVersion: version,
		Model:          model,
		Override:       override,
	})
	if errMarshal != nil {
		return version
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func resolveModel(catalog map[string]modelEntry, overrides map[string]config.PricingOverride, provider, model string) (resolvedModel, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return resolvedModel{}, false
	}
	candidates := exactModelCandidates(provider, model)
	for _, candidate := range candidates {
		entry, hasEntry := catalog[candidate]
		override, hasOverride := overrides[candidate]
		if !hasEntry && !hasOverride {
			continue
		}
		providerName := entryString(entry, "litellm_provider")
		if override.Provider != "" {
			providerName = override.Provider
		}
		return resolvedModel{
			name:        candidate,
			provider:    providerName,
			entry:       entry,
			override:    override,
			hasOverride: hasOverride,
		}, true
	}
	return resolvedModel{}, false
}

func exactModelCandidates(provider, model string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	seen := make(map[string]struct{})
	out := make([]string, 0, 6)
	appendCandidate := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	appendCandidate(model)
	appendCandidate(strings.TrimPrefix(model, "models/"))
	if provider != "" {
		appendCandidate(provider + "/" + model)
	}
	if slash := strings.IndexByte(model, '/'); slash > 0 {
		prefix := strings.ToLower(model[:slash])
		if knownProviderPrefix(prefix) {
			appendCandidate(model[slash+1:])
		}
	}
	for _, candidate := range append([]string(nil), out...) {
		if alias := explicitModelAliases[candidate]; alias != "" {
			appendCandidate(alias)
		}
	}
	return out
}

func knownProviderPrefix(value string) bool {
	switch value {
	case "openai", "codex", "azure", "azure_ai", "anthropic", "claude", "gemini", "vertex", "aistudio", "bedrock":
		return true
	default:
		return false
	}
}

func normalizeProviderFamily(values ...string) string {
	joined := strings.ToLower(strings.Join(values, " "))
	switch {
	case strings.Contains(joined, "openai"), strings.Contains(joined, "codex"), strings.Contains(joined, "gpt-"), strings.Contains(joined, "o3"), strings.Contains(joined, "o4"):
		return "openai"
	case strings.Contains(joined, "anthropic"), strings.Contains(joined, "claude"):
		return "anthropic"
	case strings.Contains(joined, "gemini"), strings.Contains(joined, "vertex"), strings.Contains(joined, "aistudio"):
		return "google"
	default:
		return ""
	}
}

func resolveContextThreshold(entry modelEntry, inputTokens int64) int64 {
	if inputTokens <= 0 || entry == nil {
		return 0
	}
	thresholds := make(map[int64]struct{})
	for field := range entry {
		match := contextPricePattern.FindStringSubmatch(field)
		if len(match) != 2 {
			continue
		}
		value, errParse := strconv.ParseInt(match[1], 10, 64)
		if errParse != nil || value <= 0 {
			continue
		}
		thresholds[value*1000] = struct{}{}
	}
	ordered := make([]int64, 0, len(thresholds))
	for threshold := range thresholds {
		ordered = append(ordered, threshold)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	var selected int64
	for _, threshold := range ordered {
		if inputTokens > threshold {
			selected = threshold
		}
	}
	return selected
}

func selectBucketPrice(entry modelEntry, baseField, tier string, threshold int64) selectedPrice {
	if entry == nil {
		return selectedPrice{}
	}
	tierSuffix := serviceTierSuffix(tier)
	thresholdSuffix := ""
	if threshold > 0 {
		thresholdSuffix = fmt.Sprintf("_above_%dk_tokens", threshold/1000)
	}
	if thresholdSuffix != "" && tierSuffix != "" {
		for _, field := range []string{
			baseField + thresholdSuffix + tierSuffix,
			baseField + tierSuffix + thresholdSuffix,
		} {
			if value, okValue := entryNumber(entry, field); okValue {
				return selectedPrice{value: value, found: true}
			}
		}
		if combined, okCombined := deriveCombinedPrice(entry, baseField, thresholdSuffix, tierSuffix); okCombined {
			return selectedPrice{value: combined, found: true, estimated: true}
		}
	}

	if thresholdSuffix != "" {
		if value, okValue := entryNumber(entry, baseField+thresholdSuffix); okValue {
			return selectedPrice{value: value, found: true, estimated: tierSuffix != ""}
		}
	}
	if tierSuffix != "" {
		if value, okValue := entryNumber(entry, baseField+tierSuffix); okValue {
			return selectedPrice{value: value, found: true, estimated: thresholdSuffix != ""}
		}
	}
	if value, okValue := entryNumber(entry, baseField); okValue {
		return selectedPrice{value: value, found: true, estimated: thresholdSuffix != "" || tierSuffix != ""}
	}
	return selectedPrice{}
}

func deriveCombinedPrice(entry modelEntry, baseField, thresholdSuffix, tierSuffix string) (float64, bool) {
	base, okBase := entryNumber(entry, baseField)
	thresholdPrice, okThreshold := entryNumber(entry, baseField+thresholdSuffix)
	tierPrice, okTier := entryNumber(entry, baseField+tierSuffix)
	if !okBase || !okThreshold || !okTier || base <= 0 {
		return 0, false
	}
	return tierPrice * (thresholdPrice / base), true
}

func serviceTierSuffix(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "flex":
		return "_flex"
	case "priority":
		return "_priority"
	case "batch":
		return "_batches"
	default:
		return ""
	}
}

func applyPriceOverride(price selectedPrice, override *float64) selectedPrice {
	if override == nil || *override < 0 {
		return price
	}
	return selectedPrice{value: *override / 1_000_000, found: true}
}

func hasOneHourCachePrice(entry modelEntry) bool {
	if entry == nil {
		return false
	}
	for field := range entry {
		if strings.HasPrefix(field, "cache_creation_input_token_cost_above_1hr") {
			return true
		}
	}
	return false
}
