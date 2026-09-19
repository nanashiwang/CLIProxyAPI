package usage

import (
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// UsageQuery applies the same half-open time range and dimensions to all views.
type UsageQuery struct {
	From, To      time.Time
	Provider      string
	Model         string
	Account       string
	APIKey        string
	Pool          string
	Status        string
	StatusCode    int
	Search        string
	IncludeWarmup bool
}

// UsageRecord is a retained usage event, not an inferred upstream attempt.
type UsageRecord struct {
	RequestDetail
	ID         string `json:"id"`
	Model      string `json:"model"`
	API        string `json:"api"`
	Account    string `json:"account"`
	ModelMatch string `json:"model_match"`
}

// UnmarshalJSON must decode the outer fields explicitly because the embedded
// RequestDetail has a legacy-default decoder of its own.
func (r *UsageRecord) UnmarshalJSON(data []byte) error {
	var fields struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		API        string `json:"api"`
		Account    string `json:"account"`
		ModelMatch string `json:"model_match"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var detail RequestDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		return err
	}
	*r = UsageRecord{RequestDetail: detail, ID: fields.ID, Model: fields.Model, API: fields.API, Account: fields.Account, ModelMatch: fields.ModelMatch}
	return nil
}

type UsageRecordsPage struct {
	Items      []UsageRecord `json:"items"`
	Page       int           `json:"page"`
	PageSize   int           `json:"page_size"`
	Total      int           `json:"total"`
	TotalPages int           `json:"total_pages"`
}

type TokenQualityCounts struct {
	Complete     int64 `json:"complete"`
	Inconsistent int64 `json:"inconsistent"`
	Unclassified int64 `json:"unclassified"`
	Unavailable  int64 `json:"unavailable"`
}

type UsageSummary struct {
	DimensionSnapshot
	AverageLatencyMs     *float64           `json:"avg_latency_ms"`
	AverageTTFTMs        *float64           `json:"avg_ttft_ms"`
	Estimated            bool               `json:"estimated"`
	CacheWriteUnreported bool               `json:"cache_write_unreported"`
	TokenQuality         TokenQualityCounts `json:"token_quality"`
}

// UsagePerformance only includes successful generation events with reported
// positive timings. Coverage uses successful events as the denominator.
type UsagePerformance struct {
	LatencyP50Ms          *float64 `json:"latency_p50_ms"`
	LatencyP95Ms          *float64 `json:"latency_p95_ms"`
	LatencyP99Ms          *float64 `json:"latency_p99_ms"`
	TTFTP50Ms             *float64 `json:"ttft_p50_ms"`
	TTFTP95Ms             *float64 `json:"ttft_p95_ms"`
	TTFTP99Ms             *float64 `json:"ttft_p99_ms"`
	LatencySamples        int      `json:"latency_samples"`
	TTFTSamples           int      `json:"ttft_samples"`
	LatencyCoverage       float64  `json:"latency_coverage"`
	TTFTCoverage          float64  `json:"ttft_coverage"`
	OutputTokensPerSecond *float64 `json:"output_tokens_per_second"`
	ThroughputSamples     int      `json:"throughput_samples"`
}

type UsageHealth struct {
	SuccessRate      float64          `json:"success_rate"`
	StatusCodes      map[string]int64 `json:"status_codes"`
	ClientErrorCount int64            `json:"client_error_count"`
	ServerErrorCount int64            `json:"server_error_count"`
	RateLimitedCount int64            `json:"rate_limited_count"`
}

// UsageCost retains unknown costs as null rather than treating them as free.
type UsageCost struct {
	TotalCostUSD         *float64                `json:"total_cost_usd"`
	AverageCostUSD       *float64                `json:"avg_cost_usd"`
	PricingCoverage      float64                 `json:"pricing_coverage"`
	CacheReadRatio       float64                 `json:"cache_read_ratio"`
	CacheHitRequestRatio float64                 `json:"cache_hit_request_ratio"`
	PricedRequests       int64                   `json:"priced_requests"`
	UnpricedRequests     int64                   `json:"unpriced_requests"`
	Breakdown            coreusage.CostBreakdown `json:"breakdown"`
}

type UsageTrendPoint struct {
	DimensionSnapshot
	UsagePerformance
	Timestamp            time.Time `json:"timestamp"`
	AverageLatencyMs     *float64  `json:"avg_latency_ms"`
	AverageTTFTMs        *float64  `json:"avg_ttft_ms"`
	KnownCostUSD         *float64  `json:"known_cost_usd"`
	PricingCoverage      float64   `json:"pricing_coverage"`
	CacheReadRatio       float64   `json:"cache_read_ratio"`
	CacheHitRequestRatio float64   `json:"cache_hit_request_ratio"`
}

type UsageDimension struct {
	DimensionSnapshot
	Key              string   `json:"key"`
	Label            string   `json:"label"`
	AverageLatencyMs *float64 `json:"avg_latency_ms"`
	AverageTTFTMs    *float64 `json:"avg_ttft_ms"`
	ErrorRate        float64  `json:"error_rate"`
}

type UsageDimensions struct {
	Models    []UsageDimension `json:"models"`
	Providers []UsageDimension `json:"providers"`
	Accounts  []UsageDimension `json:"accounts"`
}

type UsageFilterOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type UsageFilterOptions struct {
	Providers []UsageFilterOption `json:"providers"`
	Models    []UsageFilterOption `json:"models"`
	Accounts  []UsageFilterOption `json:"accounts"`
	APIKeys   []UsageFilterOption `json:"api_keys"`
	Pools     []UsageFilterOption `json:"pools"`
}

type UsageDashboard struct {
	Summary     UsageSummary       `json:"summary"`
	Performance UsagePerformance   `json:"performance"`
	Health      UsageHealth        `json:"health"`
	Cost        UsageCost          `json:"cost"`
	Trend       []UsageTrendPoint  `json:"trend"`
	Dimensions  UsageDimensions    `json:"dimensions"`
	Filters     UsageFilterOptions `json:"filters"`
	Granularity string             `json:"granularity"`
	From        time.Time          `json:"from"`
	To          time.Time          `json:"to"`
}

func (q UsageQuery) matchesTime(event storedEvent) bool {
	return (q.From.IsZero() || !event.Detail.Timestamp.Before(q.From)) &&
		(q.To.IsZero() || event.Detail.Timestamp.Before(q.To)) &&
		(q.IncludeWarmup || event.Detail.Generate)
}

func (q UsageQuery) matches(event storedEvent) bool {
	d := event.Detail
	if !q.matchesTime(event) || (q.Provider != "" && q.Provider != d.Provider) ||
		(q.Model != "" && q.Model != event.Model) ||
		(q.Account != "" && q.Account != usageAccountKey(d)) ||
		(q.APIKey != "" && q.APIKey != usageAPIKey(event)) ||
		(q.Pool != "" && q.Pool != d.PoolID) ||
		(q.Status == "success" && d.Failed) || (q.Status == "failed" && !d.Failed) ||
		(q.StatusCode != 0 && q.StatusCode != d.StatusCode) {
		return false
	}
	if q.Search == "" {
		return true
	}
	for _, value := range []string{d.RequestID, event.Model, d.Alias, d.RequestedModel, d.UpstreamModel, d.UpstreamResponseModel, d.Provider, d.AuthID, d.AuthIndex, d.AuthType, d.Source, d.Endpoint, event.API, d.ClientKeyID, d.PoolID, d.ReasoningEffort, d.ClientTransport, d.UpstreamTransport, d.ClientIP, d.UserAgent} {
		if strings.Contains(strings.ToLower(value), q.Search) {
			return true
		}
	}
	return false
}

func usageAccountKey(detail RequestDetail) string {
	if value := stableAccountUsageIdentifier(detail); value != "" {
		return value
	}
	return detail.Provider + ":unknown"
}

func usageAccountLabel(detail RequestDetail) string {
	for _, value := range []string{detail.AuthID, detail.Source, detail.AuthIndex} {
		if value != "" {
			return value
		}
	}
	return detail.Provider + ":unknown"
}

func usageAPIKey(event storedEvent) string {
	if event.Detail.ClientKeyID != "" {
		return event.Detail.ClientKeyID
	}
	return event.API
}

func usageRecordID(event storedEvent) string {
	// A millisecond prefix enables indexed detail lookup; the suffix uses the
	// existing migration identity so IDs survive exports and reloads.
	sum := sha256.Sum256([]byte(dedupKey(event)))
	return fmt.Sprintf("%016x", uint64(event.Detail.Timestamp.UnixMilli())) + hex.EncodeToString(sum[:8])
}

func usageQueryBounds(events []storedEvent, query UsageQuery) (int, int) {
	start, end := 0, len(events)
	if !query.From.IsZero() {
		start = sort.Search(len(events), func(i int) bool { return !events[i].Detail.Timestamp.Before(query.From) })
	}
	if !query.To.IsZero() {
		end = sort.Search(len(events), func(i int) bool { return !events[i].Detail.Timestamp.Before(query.To) })
	}
	return min(start, end), end
}

func usageRecord(event storedEvent) UsageRecord {
	detail := event.Detail
	if detail.TokenBreakdown != nil {
		breakdown := *detail.TokenBreakdown
		detail.TokenBreakdown = &breakdown
	}
	if detail.CostUSD != nil {
		cost := *detail.CostUSD
		detail.CostUSD = &cost
	}
	return UsageRecord{RequestDetail: detail, ID: usageRecordID(event), Model: event.Model, API: event.API, Account: usageAccountKey(detail), ModelMatch: modelMatch(detail)}
}

// QueryRecords filters on the server and copies only the requested page. The
// default timestamp order uses the store's sorted index without a full sort.
func (s *RequestStatistics) QueryRecords(query UsageQuery, page, pageSize int, sortBy, order string) UsageRecordsPage {
	page = max(1, page)
	pageSize = max(1, min(200, pageSize))
	result := UsageRecordsPage{Items: make([]UsageRecord, 0, pageSize), Page: page, PageSize: pageSize}
	if s == nil {
		return result
	}
	query.Search = strings.ToLower(strings.TrimSpace(query.Search))
	s.mu.RLock()
	defer s.mu.RUnlock()
	start, end := usageQueryBounds(s.events, query)
	// Guard arithmetic even for direct SDK callers with an enormous page number.
	offset := len(s.events)
	if page-1 <= len(s.events)/pageSize {
		offset = (page - 1) * pageSize
	}
	if sortBy == "" || sortBy == "timestamp" {
		for position := range end - start {
			index := end - 1 - position
			if order == "asc" {
				index = start + position
			}
			event := s.events[index]
			if !query.matches(event) {
				continue
			}
			if result.Total >= offset && len(result.Items) < pageSize {
				result.Items = append(result.Items, usageRecord(event))
			}
			result.Total++
		}
	} else {
		// Keep only the best offset+pageSize indices, bounding both sorting work
		// and memory for normal pages instead of sorting every matching record.
		candidates := &usageRecordCandidates{events: s.events, sortBy: sortBy, order: order}
		limit := min(offset+pageSize, end-start)
		if offset >= end-start {
			limit = 0
		}
		for index := start; index < end; index++ {
			event := s.events[index]
			if query.matches(event) {
				result.Total++
				if len(candidates.indices) < limit {
					heap.Push(candidates, index)
				} else if limit > 0 && candidates.better(index, candidates.indices[0]) {
					candidates.indices[0] = index
					heap.Fix(candidates, 0)
				}
			}
		}
		indices := candidates.indices
		sort.Slice(indices, func(i, j int) bool { return candidates.better(indices[i], indices[j]) })
		for _, index := range indices[min(offset, len(indices)):min(offset+pageSize, len(indices))] {
			result.Items = append(result.Items, usageRecord(s.events[index]))
		}
	}
	result.TotalPages = (result.Total + pageSize - 1) / pageSize
	return result
}

type usageRecordCandidates struct {
	indices       []int
	events        []storedEvent
	sortBy, order string
}

func (h usageRecordCandidates) Len() int           { return len(h.indices) }
func (h usageRecordCandidates) Less(i, j int) bool { return h.better(h.indices[j], h.indices[i]) }
func (h usageRecordCandidates) Swap(i, j int) {
	h.indices[i], h.indices[j] = h.indices[j], h.indices[i]
}
func (h *usageRecordCandidates) Push(value any) { h.indices = append(h.indices, value.(int)) }
func (h *usageRecordCandidates) Pop() any {
	last := h.indices[len(h.indices)-1]
	h.indices = h.indices[:len(h.indices)-1]
	return last
}

func (h usageRecordCandidates) better(leftIndex, rightIndex int) bool {
	left, right := h.events[leftIndex].Detail, h.events[rightIndex].Detail
	if h.sortBy == "cost" && (left.CostUSD == nil) != (right.CostUSD == nil) {
		return left.CostUSD != nil
	}
	lv, rv := usageSortValue(left, h.sortBy), usageSortValue(right, h.sortBy)
	if lv == rv {
		return leftIndex > rightIndex
	}
	if h.order == "asc" {
		return lv < rv
	}
	return lv > rv
}

func usageSortValue(detail RequestDetail, sortBy string) float64 {
	switch sortBy {
	case "latency":
		return float64(detail.LatencyMs)
	case "ttft":
		return float64(detail.TTFTMs)
	case "tokens":
		return float64(detail.Tokens.TotalTokens)
	case "cost":
		if detail.CostUSD != nil {
			return *detail.CostUSD
		}
	}
	return 0
}

func (s *RequestStatistics) RecordByID(id string) (UsageRecord, bool) {
	if s == nil || len(id) != 32 {
		return UsageRecord{}, false
	}
	millis, err := strconv.ParseUint(id[:16], 16, 64)
	if err != nil {
		return UsageRecord{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	start := sort.Search(len(s.events), func(i int) bool { return s.events[i].Detail.Timestamp.UnixMilli() >= int64(millis) })
	for index := start; index < len(s.events) && s.events[index].Detail.Timestamp.UnixMilli() == int64(millis); index++ {
		if usageRecordID(s.events[index]) == id {
			return usageRecord(s.events[index]), true
		}
	}
	return UsageRecord{}, false
}

type dashboardAggregate struct {
	totals             DimensionSnapshot
	latencyTotal       int64
	latencyCount       int64
	ttftTotal          int64
	ttftCount          int64
	label              string
	latencies          []int64
	ttfts              []int64
	cacheHits          int64
	successGenerations int64
	throughputTokens   int64
	throughputDuration int64
	throughputSamples  int
}

func (a *dashboardAggregate) addSamples(detail RequestDetail) {
	if detail.Tokens.CacheReadTokens > 0 {
		a.cacheHits++
	}
	if detail.Failed || !detail.Generate {
		return
	}
	a.successGenerations++
	if detail.LatencyMs > 0 {
		a.latencies = append(a.latencies, detail.LatencyMs)
	}
	if detail.TTFTMs > 0 {
		a.ttfts = append(a.ttfts, detail.TTFTMs)
	}
	output := detail.Tokens.OutputTokens + detail.Tokens.ReasoningTokens
	if detail.TTFTMs > 0 && detail.LatencyMs > detail.TTFTMs && output > 0 && detail.TokenBreakdown != nil && detail.TokenBreakdown.Valid() && detail.TokenBreakdown.Quality == coreusage.TokenAccountingQualityComplete {
		a.throughputSamples++
		a.throughputTokens += output
		a.throughputDuration += detail.LatencyMs - detail.TTFTMs
	}
}

func (a *dashboardAggregate) performance() UsagePerformance {
	sort.Slice(a.latencies, func(i, j int) bool { return a.latencies[i] < a.latencies[j] })
	sort.Slice(a.ttfts, func(i, j int) bool { return a.ttfts[i] < a.ttfts[j] })
	result := UsagePerformance{
		LatencyP50Ms: dashboardPercentile(a.latencies, .50), LatencyP95Ms: dashboardPercentile(a.latencies, .95), LatencyP99Ms: dashboardPercentile(a.latencies, .99),
		TTFTP50Ms: dashboardPercentile(a.ttfts, .50), TTFTP95Ms: dashboardPercentile(a.ttfts, .95), TTFTP99Ms: dashboardPercentile(a.ttfts, .99),
		LatencySamples: len(a.latencies), TTFTSamples: len(a.ttfts), LatencyCoverage: dashboardRatio(int64(len(a.latencies)), a.successGenerations), TTFTCoverage: dashboardRatio(int64(len(a.ttfts)), a.successGenerations),
		ThroughputSamples: a.throughputSamples,
	}
	if a.throughputDuration > 0 {
		value := float64(a.throughputTokens) * 1000 / float64(a.throughputDuration)
		result.OutputTokensPerSecond = &value
	}
	return result
}

func (a *dashboardAggregate) add(event storedEvent) {
	d := event.Detail
	cost := 0.0
	if d.Billing.Priced && d.CostUSD != nil {
		cost = *d.CostUSD
	}
	a.totals = addDimension(a.totals, d, cost)
	if !d.Failed && d.Generate {
		if d.LatencyMs > 0 {
			a.latencyTotal += d.LatencyMs
			a.latencyCount++
		}
		if d.TTFTMs > 0 {
			a.ttftTotal += d.TTFTMs
			a.ttftCount++
		}
	}
}

func dashboardAverage(total, count int64) *float64 {
	if count == 0 {
		return nil
	}
	value := float64(total) / float64(count)
	return &value
}

func dashboardRatio(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func dashboardPercentile(sorted []int64, percentile float64) *float64 {
	if len(sorted) == 0 {
		return nil
	}
	index := max(0, int(math.Ceil(float64(len(sorted))*percentile))-1)
	value := float64(sorted[index])
	return &value
}

// Dashboard scans the retained time range once. It does not copy request details
// into the response or infer attempts, cancellations, or cache prices.
func (s *RequestStatistics) Dashboard(query UsageQuery, now time.Time) UsageDashboard {
	query.Search = strings.ToLower(strings.TrimSpace(query.Search))
	result := UsageDashboard{From: query.From, To: query.To,
		Health: UsageHealth{StatusCodes: make(map[string]int64)}, Trend: make([]UsageTrendPoint, 0)}
	if result.To.IsZero() {
		result.To = now.UTC()
	}
	query.To = result.To
	var summary dashboardAggregate
	models, providers, accounts := map[string]*dashboardAggregate{}, map[string]*dashboardAggregate{}, map[string]*dashboardAggregate{}
	optionMaps := make([]map[string]string, 5)
	for i := range optionMaps {
		optionMaps[i] = make(map[string]string)
	}
	bucketTotals := make(map[time.Time]*dashboardAggregate)
	statusCodes := make(map[int]int64)
	type accountIdentity struct{ provider, index, id, source string }
	accountOptions := make(map[accountIdentity]UsageFilterOption)
	if s != nil {
		s.mu.RLock()
		if result.From.IsZero() && len(s.events) > 0 {
			result.From = s.events[0].Detail.Timestamp
		}
		if result.From.IsZero() || result.From.After(result.To) {
			result.From = result.To.Add(-24 * time.Hour)
		}
		result.Granularity = "day"
		if result.To.Sub(result.From) <= 48*time.Hour {
			result.Granularity = "hour"
		}
		start, end := usageQueryBounds(s.events, query)
		for _, event := range s.events[start:end] {
			if !query.matchesTime(event) {
				continue
			}
			d := event.Detail
			identity := accountIdentity{d.Provider, d.AuthIndex, d.AuthID, d.Source}
			accountOption, ok := accountOptions[identity]
			if !ok {
				accountOption = UsageFilterOption{Value: usageAccountKey(d), Label: usageAccountLabel(d)}
				accountOptions[identity] = accountOption
			}
			optionMaps[0][d.Provider] = d.Provider
			optionMaps[1][event.Model] = event.Model
			optionMaps[2][accountOption.Value] = accountOption.Label
			optionMaps[3][usageAPIKey(event)] = usageAPIKey(event)
			if d.PoolID != "" {
				optionMaps[4][d.PoolID] = d.PoolID
			}
			if !query.matches(event) {
				continue
			}
			summary.add(event)
			summary.addSamples(d)
			result.Summary.Estimated = result.Summary.Estimated || d.Billing.Pricing.Estimated
			result.Summary.CacheWriteUnreported = result.Summary.CacheWriteUnreported || d.Billing.Reason == "cache_write_tokens_unreported"
			quality := &result.Summary.TokenQuality
			if d.TokenBreakdown == nil || !d.TokenBreakdown.Valid() {
				quality.Unavailable++
			} else {
				switch d.TokenBreakdown.Quality {
				case coreusage.TokenAccountingQualityComplete:
					quality.Complete++
				case coreusage.TokenAccountingQualityInconsistent:
					quality.Inconsistent++
				case coreusage.TokenAccountingQualityUnclassified:
					quality.Unclassified++
				}
			}
			statusCodes[d.StatusCode]++
			if d.StatusCode >= 400 && d.StatusCode < 500 {
				result.Health.ClientErrorCount++
			}
			if d.StatusCode >= 500 && d.StatusCode < 600 {
				result.Health.ServerErrorCount++
			}
			if d.StatusCode == 429 {
				result.Health.RateLimitedCount++
			}
			if d.Billing.Priced {
				result.Cost.Breakdown.InputUSD += d.Billing.Breakdown.InputUSD
				result.Cost.Breakdown.OutputUSD += d.Billing.Breakdown.OutputUSD
				result.Cost.Breakdown.CacheReadUSD += d.Billing.Breakdown.CacheReadUSD
				result.Cost.Breakdown.CacheWriteUSD += d.Billing.Breakdown.CacheWriteUSD
			}
			addDashboardDimension(models, event.Model, event.Model, event)
			addDashboardDimension(providers, d.Provider, d.Provider, event)
			addDashboardDimension(accounts, accountOption.Value, accountOption.Label, event)
			bucket := dashboardBucket(d.Timestamp, result.Granularity)
			if bucketTotals[bucket] == nil {
				bucketTotals[bucket] = &dashboardAggregate{}
			}
			bucketTotals[bucket].add(event)
			bucketTotals[bucket].addSamples(d)
		}
		s.mu.RUnlock()
	}
	if result.From.IsZero() {
		result.From = result.To.Add(-24 * time.Hour)
	}
	if result.Granularity == "" {
		result.Granularity = "hour"
	}
	result.Summary.DimensionSnapshot = summary.totals
	result.Summary.AverageLatencyMs = dashboardAverage(summary.latencyTotal, summary.latencyCount)
	result.Summary.AverageTTFTMs = dashboardAverage(summary.ttftTotal, summary.ttftCount)
	result.Health.SuccessRate = dashboardRatio(summary.totals.SuccessCount, summary.totals.TotalRequests)
	for code, count := range statusCodes {
		result.Health.StatusCodes[strconv.Itoa(code)] = count
	}
	result.Performance = summary.performance()
	result.Cost.PricedRequests = summary.totals.PricedRequests
	result.Cost.UnpricedRequests = summary.totals.UnpricedRequests
	result.Cost.PricingCoverage = dashboardRatio(summary.totals.PricedRequests, summary.totals.TotalRequests)
	result.Cost.CacheHitRequestRatio = dashboardRatio(summary.cacheHits, summary.totals.TotalRequests)
	input := summary.totals.Tokens.InputTokens + summary.totals.Tokens.CacheReadTokens + summary.totals.Tokens.CacheWriteTokens
	result.Cost.CacheReadRatio = dashboardRatio(summary.totals.Tokens.CacheReadTokens, input)
	if summary.totals.PricedRequests > 0 {
		value := summary.totals.TotalCostUSD
		result.Cost.TotalCostUSD = &value
		average := value / float64(summary.totals.PricedRequests)
		result.Cost.AverageCostUSD = &average
	}
	result.Dimensions = UsageDimensions{Models: dashboardDimensions(models), Providers: dashboardDimensions(providers), Accounts: dashboardDimensions(accounts)}
	result.Filters = UsageFilterOptions{Providers: dashboardOptions(optionMaps[0]), Models: dashboardOptions(optionMaps[1]), Accounts: dashboardOptions(optionMaps[2]), APIKeys: dashboardOptions(optionMaps[3]), Pools: dashboardOptions(optionMaps[4])}
	// Bound empty bucket filling for arbitrarily wide user-supplied ranges.
	step := 24 * time.Hour
	if result.Granularity == "hour" {
		step = time.Hour
	}
	start := dashboardBucket(result.From, result.Granularity)
	if result.To.Sub(start)/step <= 366 {
		for bucket := start; bucket.Before(result.To); bucket = bucket.Add(step) {
			if bucketTotals[bucket] == nil {
				bucketTotals[bucket] = &dashboardAggregate{}
			}
		}
	}
	for timestamp, value := range bucketTotals {
		point := UsageTrendPoint{DimensionSnapshot: value.totals, UsagePerformance: value.performance(), Timestamp: timestamp, AverageLatencyMs: dashboardAverage(value.latencyTotal, value.latencyCount), AverageTTFTMs: dashboardAverage(value.ttftTotal, value.ttftCount),
			PricingCoverage: dashboardRatio(value.totals.PricedRequests, value.totals.TotalRequests), CacheHitRequestRatio: dashboardRatio(value.cacheHits, value.totals.TotalRequests)}
		point.CacheReadRatio = dashboardRatio(value.totals.Tokens.CacheReadTokens, value.totals.Tokens.InputTokens+value.totals.Tokens.CacheReadTokens+value.totals.Tokens.CacheWriteTokens)
		if value.totals.PricedRequests > 0 {
			cost := value.totals.TotalCostUSD
			point.KnownCostUSD = &cost
		}
		result.Trend = append(result.Trend, point)
	}
	sort.Slice(result.Trend, func(i, j int) bool { return result.Trend[i].Timestamp.Before(result.Trend[j].Timestamp) })
	return result
}

func dashboardBucket(timestamp time.Time, granularity string) time.Time {
	timestamp = timestamp.UTC()
	if granularity == "hour" {
		return timestamp.Truncate(time.Hour)
	}
	return time.Date(timestamp.Year(), timestamp.Month(), timestamp.Day(), 0, 0, 0, 0, time.UTC)
}

func addDashboardDimension(values map[string]*dashboardAggregate, key, label string, event storedEvent) {
	if values[key] == nil {
		values[key] = &dashboardAggregate{label: label}
	}
	values[key].add(event)
}

func dashboardDimensions(values map[string]*dashboardAggregate) []UsageDimension {
	items := make([]UsageDimension, 0, len(values))
	for key, value := range values {
		items = append(items, UsageDimension{DimensionSnapshot: value.totals, Key: key, Label: value.label,
			AverageLatencyMs: dashboardAverage(value.latencyTotal, value.latencyCount), AverageTTFTMs: dashboardAverage(value.ttftTotal, value.ttftCount), ErrorRate: dashboardRatio(value.totals.FailureCount, value.totals.TotalRequests)})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].TotalRequests == items[j].TotalRequests {
			return items[i].Key < items[j].Key
		}
		return items[i].TotalRequests > items[j].TotalRequests
	})
	return items
}

func dashboardOptions(values map[string]string) []UsageFilterOption {
	items := make([]UsageFilterOption, 0, len(values))
	for value, label := range values {
		items = append(items, UsageFilterOption{Value: value, Label: label})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Label == items[j].Label {
			return items[i].Value < items[j].Value
		}
		return items[i].Label < items[j].Label
	})
	return items
}
