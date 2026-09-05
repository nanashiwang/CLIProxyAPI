package usage

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestRequestStatisticsPersistsBillingAndAccountTotals(t *testing.T) {
	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })

	path := filepath.Join(t.TempDir(), "usage.jsonl")
	stats := NewRequestStatistics()
	if errConfigure := stats.Configure(Options{StoragePath: path, RetentionDays: 30, MaxRecords: 100}); errConfigure != nil {
		t.Fatalf("Configure() error = %v", errConfigure)
	}

	ctx := internallogging.WithEndpoint(context.Background(), "/v1/responses")
	ctx = internallogging.WithRequestID(ctx, "req-1")
	stats.Record(ctx, coreusage.Record{
		Provider:     "openai",
		ExecutorType: "codex",
		Model:        "gpt-5.6",
		Alias:        "gpt-latest",
		AuthID:       "account@example.com",
		AuthIndex:    "3",
		AuthType:     "oauth",
		RequestedAt:  time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		Latency:      1500 * time.Millisecond,
		TTFT:         250 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:      120,
			OutputTokens:     40,
			ReasoningTokens:  10,
			CacheReadTokens:  20,
			CacheWriteTokens: 5,
			TotalTokens:      160,
			TokenBreakdown: coreusage.NewSubsetTokenBreakdown(
				120, 20, 5, 40, 10, 160,
			),
		},
		Billing: coreusage.Billing{
			Currency: "USD",
			Priced:   true,
			TotalUSD: 0.012345,
		},
	})

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 || snapshot.TotalTokens != 160 {
		t.Fatalf("snapshot totals = requests:%d tokens:%d", snapshot.TotalRequests, snapshot.TotalTokens)
	}
	if math.Abs(snapshot.TotalCostUSD-0.012345) > 1e-12 {
		t.Fatalf("total_cost_usd = %.12f", snapshot.TotalCostUSD)
	}
	account := snapshot.Accounts["account@example.com"]
	if account.TotalRequests != 1 || math.Abs(account.TotalCostUSD-0.012345) > 1e-12 {
		t.Fatalf("account totals = %+v", account)
	}
	model := snapshot.Models["gpt-5.6"]
	if model.Tokens.CacheReadTokens != 20 || model.Tokens.CacheWriteTokens != 5 || model.Tokens.ReasoningTokens != 10 {
		t.Fatalf("model token totals = %+v", model.Tokens)
	}

	reloaded := NewRequestStatistics()
	if errConfigure := reloaded.Configure(Options{StoragePath: path, RetentionDays: 30, MaxRecords: 100}); errConfigure != nil {
		t.Fatalf("reload Configure() error = %v", errConfigure)
	}
	reloadedSnapshot := reloaded.Snapshot()
	if reloadedSnapshot.TotalRequests != 1 || math.Abs(reloadedSnapshot.TotalCostUSD-0.012345) > 1e-12 {
		t.Fatalf("reloaded snapshot = %+v", reloadedSnapshot)
	}
}

func TestRequestStatisticsPrunesToMaxRecordsAndSupportsRange(t *testing.T) {
	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })

	stats := NewRequestStatistics()
	if errConfigure := stats.Configure(Options{StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), RetentionDays: 365, MaxRecords: 2}); errConfigure != nil {
		t.Fatalf("Configure() error = %v", errConfigure)
	}

	base := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		stats.Record(context.Background(), coreusage.Record{
			Provider:    "openai",
			Model:       "gpt-5.6",
			AuthID:      "account",
			RequestedAt: base.Add(time.Duration(index) * time.Hour),
			Detail: coreusage.Detail{
				InputTokens: 10,
				TotalTokens: 10,
			},
			Billing: coreusage.Billing{Currency: "USD", Priced: true, TotalUSD: float64(index + 1)},
		})
	}

	if status := stats.Status(); status.RecordCount != 2 {
		t.Fatalf("record_count = %d, want 2", status.RecordCount)
	}
	ranged := stats.SnapshotRange(base.Add(90*time.Minute), base.Add(150*time.Minute))
	if ranged.TotalRequests != 1 || math.Abs(ranged.TotalCostUSD-3) > 1e-12 {
		t.Fatalf("range snapshot = requests:%d cost:%f", ranged.TotalRequests, ranged.TotalCostUSD)
	}
}

func TestRequestStatisticsMergeSnapshotDeduplicatesAndPersists(t *testing.T) {
	stats := NewRequestStatistics()
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if errConfigure := stats.Configure(Options{StoragePath: path, RetentionDays: 365, MaxRecords: 100}); errConfigure != nil {
		t.Fatalf("Configure() error = %v", errConfigure)
	}

	detail := RequestDetail{
		Timestamp:  time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		Provider:   "openai",
		AuthID:     "account",
		RequestID:  "req-import",
		Tokens:     TokenStats{InputTokens: 10, TotalTokens: 10},
		StatusCode: 200,
		Billing:    coreusage.Billing{Currency: "USD", Priced: true, TotalUSD: 0.5},
	}
	cost := 0.5
	detail.CostUSD = &cost
	snapshot := newSnapshot()
	snapshot.APIs["/v1/responses"] = APISnapshot{Models: map[string]ModelSnapshot{
		"gpt-5.6": {Details: []RequestDetail{detail}},
	}}

	first, errFirst := stats.MergeSnapshot(snapshot)
	if errFirst != nil {
		t.Fatalf("first MergeSnapshot() error = %v", errFirst)
	}
	second, errSecond := stats.MergeSnapshot(snapshot)
	if errSecond != nil {
		t.Fatalf("second MergeSnapshot() error = %v", errSecond)
	}
	if first.Added != 1 || first.Skipped != 0 || second.Added != 0 || second.Skipped != 1 {
		t.Fatalf("merge results = first:%+v second:%+v", first, second)
	}

	reloaded := NewRequestStatistics()
	if errConfigure := reloaded.Configure(Options{StoragePath: path, RetentionDays: 365, MaxRecords: 100}); errConfigure != nil {
		t.Fatalf("reload Configure() error = %v", errConfigure)
	}
	if got := reloaded.Snapshot().TotalRequests; got != 1 {
		t.Fatalf("reloaded requests = %d, want 1", got)
	}
}

func TestRequestStatisticsKeepsOutOfOrderRecordsSorted(t *testing.T) {
	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })

	stats := NewRequestStatistics()
	if errConfigure := stats.Configure(Options{StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), RetentionDays: 365, MaxRecords: 2}); errConfigure != nil {
		t.Fatalf("Configure() error = %v", errConfigure)
	}

	base := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{2 * time.Hour, 0, time.Hour} {
		stats.Record(context.Background(), coreusage.Record{
			Provider:    "openai",
			Model:       "gpt-5.6",
			RequestedAt: base.Add(offset),
			Detail:      coreusage.Detail{InputTokens: 10, TotalTokens: 10},
		})
	}

	status := stats.Status()
	if status.RecordCount != 2 || status.OldestAt == nil || status.LatestAt == nil {
		t.Fatalf("status = %+v", status)
	}
	if !status.OldestAt.Equal(base.Add(time.Hour)) || !status.LatestAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("range = %v..%v", status.OldestAt, status.LatestAt)
	}
	details := stats.Snapshot().APIs["openai"].Models["gpt-5.6"].Details
	if len(details) != 2 || !details[0].Timestamp.Equal(base.Add(time.Hour)) || !details[1].Timestamp.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("details = %+v", details)
	}
}

func TestStorageStatusOmitsEmptyTimestamps(t *testing.T) {
	stats := NewRequestStatistics()
	raw, errMarshal := json.Marshal(stats.Status())
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if strings.Contains(string(raw), "oldest_at") || strings.Contains(string(raw), "latest_at") || strings.Contains(string(raw), "loaded_at") {
		t.Fatalf("empty timestamps should be omitted: %s", raw)
	}
}

func TestAccountSnapshotsRangeGroupsByStableAuthIndex(t *testing.T) {
	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })

	stats := NewRequestStatistics()
	if errConfigure := stats.Configure(Options{StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), RetentionDays: 365, MaxRecords: 100}); errConfigure != nil {
		t.Fatalf("Configure() error = %v", errConfigure)
	}

	base := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	for index, cost := range []float64{0.25, 0.75} {
		stats.Record(context.Background(), coreusage.Record{
			Provider:    "codex",
			Model:       "gpt-5.6-luna",
			AuthID:      "codex-account.json",
			AuthIndex:   "stable-index",
			AuthType:    "oauth",
			Source:      "account@example.com",
			RequestedAt: base.Add(time.Duration(index) * time.Hour),
			Detail:      coreusage.Detail{InputTokens: 100, TotalTokens: 100},
			Billing: coreusage.Billing{
				Currency: "USD",
				Priced:   true,
				Reason: func() string {
					if index == 1 {
						return "cache_write_tokens_unreported"
					}
					return ""
				}(),
				TotalUSD: cost,
				Pricing: coreusage.PricingSnapshot{
					Estimated: index == 1,
				},
			},
		})
	}
	stats.Record(context.Background(), coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5.6-luna",
		AuthIndex:   "outside-range",
		RequestedAt: base.Add(-24 * time.Hour),
		Detail:      coreusage.Detail{InputTokens: 50, TotalTokens: 50},
		Billing:     coreusage.Billing{Currency: "USD", Priced: true, TotalUSD: 5},
	})

	accounts := stats.AccountSnapshotsRange(base, base.Add(3*time.Hour))
	if len(accounts) != 1 {
		t.Fatalf("accounts = %+v, want one account", accounts)
	}
	account := accounts[0]
	if account.Key != "auth_index:stable-index" || account.AuthIndex != "stable-index" {
		t.Fatalf("identity = %+v", account)
	}
	if account.Provider != "codex" || account.AuthID != "codex-account.json" || account.Source != "account@example.com" {
		t.Fatalf("metadata = %+v", account)
	}
	if account.TotalRequests != 2 || account.TotalTokens != 200 || math.Abs(account.TotalCostUSD-1) > 1e-12 {
		t.Fatalf("totals = %+v", account.DimensionSnapshot)
	}
	if !account.Estimated || !account.CacheWriteUnreported {
		t.Fatalf("pricing flags = %+v", account)
	}
}

func TestAccountSnapshotsRangesAggregatesMultipleWindowsInOnePass(t *testing.T) {
	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })

	stats := NewRequestStatistics()
	if errConfigure := stats.Configure(Options{StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), RetentionDays: 365, MaxRecords: 100}); errConfigure != nil {
		t.Fatalf("Configure() error = %v", errConfigure)
	}

	base := time.Date(2026, 8, 13, 11, 30, 0, 0, time.UTC)
	for index, record := range []struct {
		authIndex string
		cost      float64
		estimated bool
	}{
		{authIndex: "account-a", cost: 1.25},
		{authIndex: "account-a", cost: 2.75, estimated: true},
		{authIndex: "account-b", cost: 9},
	} {
		stats.Record(context.Background(), coreusage.Record{
			Provider:    "codex",
			Model:       "gpt-5.6-luna",
			AuthIndex:   record.authIndex,
			RequestedAt: base.Add(time.Duration(index+1) * time.Hour),
			Detail:      coreusage.Detail{InputTokens: 100, TotalTokens: 100},
			Billing: coreusage.Billing{
				Currency: "USD",
				Priced:   true,
				TotalUSD: record.cost,
				Pricing:  coreusage.PricingSnapshot{Estimated: record.estimated},
			},
		})
	}

	ranges := stats.AccountSnapshotsRanges([]AccountUsageRange{
		{Key: "account-a:week", AuthIndex: "account-a", From: base, To: base.Add(4 * time.Hour)},
		{Key: "account-a:short", AuthIndex: "account-a", From: base.Add(2 * time.Hour), To: base.Add(4 * time.Hour)},
		{Key: "account-c:empty", AuthIndex: "account-c", From: base, To: base.Add(4 * time.Hour)},
	})
	if len(ranges) != 3 {
		t.Fatalf("ranges = %+v", ranges)
	}
	if ranges[0].TotalRequests != 2 || math.Abs(ranges[0].TotalCostUSD-4) > 1e-12 || !ranges[0].Estimated {
		t.Fatalf("weekly range = %+v", ranges[0])
	}
	if ranges[1].TotalRequests != 1 || math.Abs(ranges[1].TotalCostUSD-2.75) > 1e-12 {
		t.Fatalf("short range = %+v", ranges[1])
	}
	if ranges[2].TotalRequests != 0 || ranges[2].AuthIndex != "account-c" {
		t.Fatalf("empty range = %+v", ranges[2])
	}
}

func TestSnapshotWindowUsesPrecomputedAggregatesAndLimitsDetails(t *testing.T) {
	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })

	stats := NewRequestStatistics()
	if errConfigure := stats.Configure(Options{StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), RetentionDays: 365, MaxRecords: 100}); errConfigure != nil {
		t.Fatalf("Configure() error = %v", errConfigure)
	}

	now := time.Now().UTC()
	for _, event := range []struct {
		at        time.Time
		cost      float64
		reason    string
		estimated bool
	}{
		{at: now.Add(-2 * time.Hour), cost: 0.25},
		{at: now.Add(-2 * 24 * time.Hour), cost: 0.5, reason: "cache_write_tokens_unreported", estimated: true},
		{at: now.Add(-10 * 24 * time.Hour), cost: 1},
	} {
		stats.Record(context.Background(), coreusage.Record{
			Provider:    "openai",
			Model:       "gpt-5.6",
			AuthID:      "account",
			RequestedAt: event.at,
			Detail:      coreusage.Detail{InputTokens: 10, TotalTokens: 10},
			Billing: coreusage.Billing{
				Currency: "USD",
				Priced:   true,
				Reason:   event.reason,
				TotalUSD: event.cost,
				Pricing:  coreusage.PricingSnapshot{Estimated: event.estimated},
			},
		})
	}

	snapshot, cache := stats.SnapshotWindow("7d", 1)
	if !cache.Precomputed || cache.Window != "7d" || cache.AgeSeconds < 0 {
		t.Fatalf("cache status = %+v", cache)
	}
	if snapshot.TotalRequests != 2 || math.Abs(snapshot.TotalCostUSD-0.75) > 1e-12 {
		t.Fatalf("7d snapshot = requests:%d cost:%f", snapshot.TotalRequests, snapshot.TotalCostUSD)
	}
	if !snapshot.Estimated || !snapshot.CacheWriteUnreported {
		t.Fatalf("estimate flags = estimated:%t cache_write_unreported:%t", snapshot.Estimated, snapshot.CacheWriteUnreported)
	}
	if len(snapshot.APIs["openai"].Models["gpt-5.6"].Details) != 1 {
		t.Fatalf("details count = %d, want 1", len(snapshot.APIs["openai"].Models["gpt-5.6"].Details))
	}
	if len(snapshot.CostByDay) != 2 {
		t.Fatalf("cost_by_day buckets = %d, want 2", len(snapshot.CostByDay))
	}
}

func TestSnapshotWindowRefreshesAfterClear(t *testing.T) {
	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })

	stats := NewRequestStatistics()
	if errConfigure := stats.Configure(Options{StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), RetentionDays: 365, MaxRecords: 100}); errConfigure != nil {
		t.Fatalf("Configure() error = %v", errConfigure)
	}
	stats.Record(context.Background(), coreusage.Record{
		Provider:    "openai",
		Model:       "gpt-5.6",
		RequestedAt: time.Now().UTC(),
		Detail:      coreusage.Detail{InputTokens: 10, TotalTokens: 10},
		Billing:     coreusage.Billing{Currency: "USD", Priced: true, TotalUSD: 1},
	})
	if snapshot, _ := stats.SnapshotWindow("24h", 0); snapshot.TotalRequests != 1 {
		t.Fatalf("before clear requests = %d", snapshot.TotalRequests)
	}
	if errClear := stats.Clear(); errClear != nil {
		t.Fatalf("Clear() error = %v", errClear)
	}
	if snapshot, _ := stats.SnapshotWindow("24h", 0); snapshot.TotalRequests != 0 {
		t.Fatalf("after clear requests = %d", snapshot.TotalRequests)
	}
}
