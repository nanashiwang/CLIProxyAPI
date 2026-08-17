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
