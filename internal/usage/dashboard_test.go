package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func dashboardTestEvent(timestamp time.Time, model, authIndex string, failed, generate bool, latency int64, priced bool) storedEvent {
	event := eventFromRecord(context.Background(), coreusage.Record{
		Provider: "openai", ExecutorType: "codex", Model: model, AuthID: "account-" + authIndex, AuthIndex: authIndex,
		APIKey: "synthetic-dashboard-key", RequestedAt: timestamp, Latency: time.Duration(latency) * time.Millisecond,
		TTFT: time.Duration(latency/10) * time.Millisecond, Failed: failed, Generate: coreusage.GenerateFlag(generate),
		Detail:  coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(100, 20, 10, 50, 15, 150)},
		Billing: coreusage.Billing{Priced: priced, TotalUSD: .15, Breakdown: coreusage.CostBreakdown{InputUSD: .10, OutputUSD: .05}},
	})
	event.Detail.ClientKeyID = "client-key-1"
	event.Detail.PoolID = "pool-1"
	return event
}

func TestDashboardSharedFiltersPaginationAndDetail(t *testing.T) {
	base := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	stats := NewRequestStatistics()
	stats.events = []storedEvent{
		dashboardTestEvent(base.Add(-time.Second), "model-a", "1", false, true, 800, true),
		dashboardTestEvent(base, "model-a", "1", false, true, 1000, true),
		dashboardTestEvent(base.Add(time.Minute), "model-b", "2", true, true, 9000, false),
		dashboardTestEvent(base.Add(2*time.Minute), "model-a", "1", false, false, 1, true),
		dashboardTestEvent(base.Add(time.Hour), "model-a", "1", false, true, 3000, false),
		dashboardTestEvent(base.Add(3*time.Hour), "model-a", "1", false, true, 1000, true),
	}
	stats.events[2].Detail.StatusCode = 429
	stats.events[4].Detail.TokenBreakdown = nil
	query := UsageQuery{From: base, To: base.Add(3 * time.Hour)}
	dashboard := stats.Dashboard(query, query.To)
	if dashboard.Summary.TotalRequests != 3 || dashboard.Summary.SuccessCount != 2 || dashboard.Summary.FailureCount != 1 {
		t.Fatalf("unexpected totals: %+v", dashboard.Summary)
	}
	if dashboard.Summary.TokenQuality.Complete != 2 || dashboard.Summary.TokenQuality.Unavailable != 1 {
		t.Fatalf("quality counts = %+v", dashboard.Summary.TokenQuality)
	}
	if dashboard.Performance.LatencySamples != 2 || *dashboard.Performance.LatencyP50Ms != 1000 || *dashboard.Performance.LatencyP95Ms != 3000 || *dashboard.Summary.AverageLatencyMs != 2000 {
		t.Fatalf("failed requests or warmup distorted latency: %+v", dashboard.Performance)
	}
	if dashboard.Performance.ThroughputSamples != 1 || math.Abs(*dashboard.Performance.OutputTokensPerSecond-50/.9) > 1e-8 {
		t.Fatalf("throughput = %+v", dashboard.Performance)
	}
	if dashboard.Health.RateLimitedCount != 1 || dashboard.Health.ClientErrorCount != 1 || dashboard.Health.SuccessRate != 2.0/3 {
		t.Fatalf("health = %+v", dashboard.Health)
	}
	if dashboard.Cost.PricedRequests != 1 || dashboard.Cost.UnpricedRequests != 2 || dashboard.Cost.TotalCostUSD == nil || *dashboard.Cost.TotalCostUSD != .15 {
		t.Fatalf("cost = %+v", dashboard.Cost)
	}
	if dashboard.Cost.CacheReadRatio != .2 || len(dashboard.Trend) != 3 || dashboard.Trend[2].LatencyP50Ms != nil || dashboard.Trend[1].KnownCostUSD != nil {
		t.Fatalf("trend or cache coverage = %+v / %+v", dashboard.Cost, dashboard.Trend)
	}
	if dashboard.Trend[0].LatencyP95Ms == nil || *dashboard.Trend[0].LatencyP95Ms != 1000 || dashboard.Trend[0].PricingCoverage != .5 {
		t.Fatalf("per-bucket quantiles or pricing = %+v", dashboard.Trend[0])
	}
	first := stats.QueryRecords(query, 1, 2, "timestamp", "desc")
	second := stats.QueryRecords(query, 2, 2, "timestamp", "desc")
	if first.Total != 3 || first.TotalPages != 2 || len(first.Items) != 2 || len(second.Items) != 1 || first.Items[0].ID == second.Items[0].ID {
		t.Fatalf("pages = %+v / %+v", first, second)
	}
	if first.Items[0].LatencyMs != 3000 || second.Items[0].LatencyMs != 1000 {
		t.Fatal("records not ordered newest first")
	}
	encodedPage, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var decodedPage UsageRecordsPage
	if err := json.Unmarshal(encodedPage, &decodedPage); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, decodedPage) {
		t.Fatalf("page JSON round trip lost detail or record fields: %+v", decodedPage)
	}
	detail, ok := stats.RecordByID(second.Items[0].ID)
	if !ok || !reflect.DeepEqual(detail, second.Items[0]) {
		t.Fatalf("detail = %+v, found = %v", detail, ok)
	}
	// Returned pointers must not allow callers to mutate the retained record.
	*detail.CostUSD = 99
	detail.TokenBreakdown.TotalTokens = 99
	persisted, _ := stats.RecordByID(second.Items[0].ID)
	if *persisted.CostUSD != .15 || persisted.TokenBreakdown.TotalTokens != 150 {
		t.Fatal("record result aliases mutable store state")
	}
	for _, tc := range []struct {
		name  string
		apply func(*UsageQuery)
		want  int
	}{
		{"model", func(q *UsageQuery) { q.Model = "model-a" }, 2},
		{"provider", func(q *UsageQuery) { q.Provider = "claude" }, 0},
		{"account", func(q *UsageQuery) { q.Account = "auth_index:2" }, 1},
		{"key", func(q *UsageQuery) { q.APIKey = "client-key-1" }, 3},
		{"pool", func(q *UsageQuery) { q.Pool = "pool-1" }, 3},
		{"status", func(q *UsageQuery) { q.Status = "failed" }, 1},
		{"status_code", func(q *UsageQuery) { q.StatusCode = 429 }, 1},
		{"search", func(q *UsageQuery) { q.Search = "ACCOUNT-2" }, 1},
		{"warmup", func(q *UsageQuery) { q.IncludeWarmup = true }, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filtered := query
			tc.apply(&filtered)
			page := stats.QueryRecords(filtered, 1, 25, "timestamp", "desc")
			view := stats.Dashboard(filtered, query.To)
			if page.Total != tc.want || int(view.Summary.TotalRequests) != tc.want {
				t.Fatalf("record/dashboard filters disagree: %d / %d want %d", page.Total, view.Summary.TotalRequests, tc.want)
			}
		})
	}
	query.Model = "model-a"
	if options := stats.Dashboard(query, query.To).Filters.Models; len(options) != 2 {
		t.Fatalf("selected dimension hid alternate filter choices: %+v", options)
	}
}

func TestDashboardUnknownDataAndRecordSort(t *testing.T) {
	base := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	stats := NewRequestStatistics()
	stats.events = []storedEvent{
		dashboardTestEvent(base, "a", "1", true, true, 0, false),
		dashboardTestEvent(base.Add(time.Second), "a", "1", true, true, 2000, false),
	}
	query := UsageQuery{From: base, To: base.Add(time.Hour)}
	view := stats.Dashboard(query, query.To)
	if view.Cost.TotalCostUSD != nil || view.Cost.AverageCostUSD != nil || view.Summary.AverageLatencyMs != nil || view.Performance.LatencyP95Ms != nil || view.Performance.OutputTokensPerSecond != nil || view.Trend[0].KnownCostUSD != nil {
		t.Fatalf("missing observations fabricated: %+v", view)
	}
	stats.events = append(stats.events, dashboardTestEvent(base.Add(2*time.Second), "a", "1", false, true, 1000, true))
	for _, order := range []string{"asc", "desc"} {
		page := stats.QueryRecords(query, 1, 25, "cost", order)
		if page.Items[0].CostUSD == nil || page.Items[1].CostUSD != nil {
			t.Fatalf("unknown costs must sort last (%s): %+v", order, page.Items)
		}
	}
	page := stats.QueryRecords(query, 1, 2, "latency", "desc")
	if page.Items[0].LatencyMs != 2000 || page.Items[1].LatencyMs != 1000 {
		t.Fatalf("unexpected latency sort: %+v", page)
	}
	page = stats.QueryRecords(query, math.MaxInt, 200, "tokens", "asc")
	if len(page.Items) != 0 || page.Total != 3 {
		t.Fatalf("overflow page = %+v", page)
	}
	if _, ok := stats.RecordByID("invalid"); ok {
		t.Fatal("invalid record ID found")
	}
}

func TestUsageHistoricalGenerationAndQualityCompatibility(t *testing.T) {
	var legacy, warmup RequestDetail
	if err := json.Unmarshal([]byte(`{"timestamp":"2026-09-19T10:00:00Z","tokens":{"total_tokens":10}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"generate":false}`), &warmup); err != nil {
		t.Fatal(err)
	}
	if !legacy.Generate || warmup.Generate || legacy.TokenBreakdown != nil {
		t.Fatalf("legacy generation/quality defaults changed: %+v / %+v", legacy, warmup)
	}
	event := dashboardTestEvent(legacy.Timestamp, "model", "1", false, true, 1000, true)
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var restored storedEvent
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if usageRecordID(event) != usageRecordID(restored) || restored.Detail.TokenBreakdown == nil || !restored.Detail.TokenBreakdown.Valid() {
		t.Fatal("canonical accounting or record ID lost on persistence round trip")
	}
	inconsistent := coreusage.NewSubsetTokenBreakdown(10, 20, 0, 5, 0, 15)
	event.Detail.TokenBreakdown = &inconsistent
	stats := NewRequestStatistics()
	stats.events = []storedEvent{event}
	if got := stats.Dashboard(UsageQuery{}, legacy.Timestamp.Add(time.Hour)).Summary.TokenQuality; got.Inconsistent != 1 || got.Complete != 0 {
		t.Fatalf("quality = %+v", got)
	}
}

func BenchmarkUsageDashboard200K(b *testing.B) {
	stats, query := benchmarkUsageStore()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stats.Dashboard(query, query.To)
	}
}

func BenchmarkUsageRecords200K(b *testing.B) {
	stats, query := benchmarkUsageStore()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stats.QueryRecords(query, 1, 25, "timestamp", "desc")
	}
}

func BenchmarkUsageRecordsSorted200K(b *testing.B) {
	stats, query := benchmarkUsageStore()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stats.QueryRecords(query, 1, 25, "latency", "desc")
	}
}

func BenchmarkUsageRecordDetail200K(b *testing.B) {
	stats, _ := benchmarkUsageStore()
	id := usageRecordID(stats.events[len(stats.events)/2])
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stats.RecordByID(id)
	}
}

func benchmarkUsageStore() (*RequestStatistics, UsageQuery) {
	stats := NewRequestStatistics()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	stats.events = make([]storedEvent, 200000)
	for i := range stats.events {
		stats.events[i] = dashboardTestEvent(base.Add(time.Duration(i)*time.Second), fmt.Sprintf("model-%d", i%8), fmt.Sprintf("%d", i%30), i%7 == 0, true, int64(1000+i%5000), i%9 != 0)
	}
	return stats, UsageQuery{From: base, To: base.Add(7 * 24 * time.Hour)}
}

func TestUsageSortedPagesKeepTiesAndUnknownCostsStable(t *testing.T) {
	base := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	stats := NewRequestStatistics()
	for i, latency := range []int64{1000, 2000, 1000, 3000, 2000, 1000} {
		event := dashboardTestEvent(base.Add(time.Duration(i)*time.Second), "model", "account", false, true, latency, i%2 == 0)
		event.Detail.RequestID = fmt.Sprintf("row-%d", i)
		stats.events = append(stats.events, event)
	}
	for _, tc := range []struct {
		sort, order string
		want        []string
	}{
		{"latency", "desc", []string{"row-3", "row-4", "row-1", "row-5", "row-2", "row-0"}},
		{"latency", "asc", []string{"row-5", "row-2", "row-0", "row-4", "row-1", "row-3"}},
		{"cost", "desc", []string{"row-4", "row-2", "row-0", "row-5", "row-3", "row-1"}},
		{"cost", "asc", []string{"row-4", "row-2", "row-0", "row-5", "row-3", "row-1"}},
	} {
		t.Run(tc.sort+"-"+tc.order, func(t *testing.T) {
			var got []string
			for page := 1; page <= 3; page++ {
				result := stats.QueryRecords(UsageQuery{}, page, 2, tc.sort, tc.order)
				if result.Total != 6 || result.TotalPages != 3 {
					t.Fatalf("unexpected pagination: %+v", result)
				}
				for _, item := range result.Items {
					got = append(got, item.RequestID)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pages = %v, want %v", got, tc.want)
			}
		})
	}
}
