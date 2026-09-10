package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func BenchmarkRecordAtRetentionCapacity(b *testing.B) {
	previous := StatisticsEnabled()
	SetStatisticsEnabled(true)
	b.Cleanup(func() { SetStatisticsEnabled(previous) })
	stats := NewRequestStatistics()
	stats.options = normalizeOptions(Options{
		StoragePath: filepath.Join(b.TempDir(), "usage.jsonl"), RetentionDays: 30, MaxRecords: 200000,
	})
	// Reserve the ordinary slice growth margin so the benchmark measures steady-state
	// eviction rather than a coincidental first-append allocation.
	stats.events = make([]storedEvent, stats.options.MaxRecords, stats.options.MaxRecords+10000)
	record := coreusage.Record{
		Provider: "codex", Model: "test", AuthIndex: "account",
		RequestedAt: time.Now().UTC().Add(-time.Hour),
		Detail:      coreusage.Detail{InputTokens: 100, OutputTokens: 10, TotalTokens: 110},
		Billing:     coreusage.Billing{Currency: "USD", Priced: true, TotalUSD: 0.01},
	}
	for i := range stats.events {
		stats.events[i] = eventFromRecord(context.Background(), record)
	}
	stats.rebuildWindowCache(stats.events, time.Now().UTC())
	if err := stats.rewriteLocked(stats.events); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = stats.file.Close() })
	record.RequestedAt = time.Now().UTC()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats.Record(context.Background(), record)
	}
}
