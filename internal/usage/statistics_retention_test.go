package usage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func retentionRecord(index int) coreusage.Record {
	return coreusage.Record{
		Provider: "codex", Model: "test", AuthIndex: "account",
		RequestedAt: time.Now().UTC().Add(-time.Hour).Add(time.Duration(index) * time.Millisecond),
		Alias:       fmt.Sprintf("req-%d", index),
		Detail:      coreusage.Detail{InputTokens: 10, TotalTokens: 10},
		Billing:     coreusage.Billing{Currency: "USD", Priced: true, TotalUSD: 1},
	}
}

func newRetentionStore(t *testing.T) (*RequestStatistics, Options) {
	t.Helper()
	previous := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previous) })
	options := Options{StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), RetentionDays: 30, MaxRecords: 100}
	stats := NewRequestStatistics()
	if err := stats.Configure(options); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stats.file != nil {
			_ = stats.file.Close()
		}
	})
	for i := 0; i < options.MaxRecords; i++ {
		stats.Record(context.Background(), retentionRecord(i))
	}
	return stats, options
}

func fileInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func assertReloadedRetention(t *testing.T, options Options, wantFirst, wantLast string) {
	t.Helper()
	reloaded := NewRequestStatistics()
	if err := reloaded.Configure(options); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reloaded.file.Close() }()
	events := reloaded.events
	if len(events) != options.MaxRecords || events[0].Detail.Alias != wantFirst || events[len(events)-1].Detail.Alias != wantLast {
		t.Fatalf("reloaded %d events, endpoints %s..%s", len(events), events[0].Detail.Alias, events[len(events)-1].Detail.Alias)
	}
	account := reloaded.AccountSnapshotsRange(time.Time{}, time.Time{})
	if len(account) != 1 || account[0].TotalRequests != int64(options.MaxRecords) || account[0].TotalCostUSD != float64(options.MaxRecords) {
		t.Fatalf("account totals changed after reload: %+v", account)
	}
}

func TestRetentionAppendsBeforeCompactingAndReloads(t *testing.T) {
	stats, options := newRetentionStore(t)
	before := fileInfo(t, options.StoragePath)
	for i := 100; i < 109; i++ {
		stats.Record(context.Background(), retentionRecord(i))
	}
	if !os.SameFile(before, fileInfo(t, options.StoragePath)) {
		t.Fatal("file was rewritten for individual evictions")
	}
	if stats.diskRecords != 109 || len(stats.events) != 100 {
		t.Fatalf("disk/memory records = %d/%d", stats.diskRecords, len(stats.events))
	}
	// Simulate process exit before the next compaction; the append log is sufficient.
	_ = stats.file.Close()
	stats.file = nil
	assertReloadedRetention(t, options, "req-9", "req-108")
}

func TestRetentionCompactsAtBoundAndRecovers(t *testing.T) {
	stats, options := newRetentionStore(t)
	before := fileInfo(t, options.StoragePath)
	for i := 100; i < 110; i++ {
		stats.Record(context.Background(), retentionRecord(i))
	}
	if os.SameFile(before, fileInfo(t, options.StoragePath)) || stats.diskRecords != 100 {
		t.Fatal("obsolete disk records were not compacted at the bound")
	}
	_ = stats.file.Close()
	stats.file = nil
	assertReloadedRetention(t, options, "req-10", "req-109")
}

func TestRetentionCompactsOnElapsedInterval(t *testing.T) {
	stats, options := newRetentionStore(t)
	before := fileInfo(t, options.StoragePath)
	stats.lastCompactedAt = time.Now().Add(-storageCompactAfter)
	stats.Record(context.Background(), retentionRecord(100))
	if os.SameFile(before, fileInfo(t, options.StoragePath)) || stats.diskRecords != 100 {
		t.Fatal("low-traffic compaction was not triggered after the interval")
	}
}

func TestRetentionCompactionFailureKeepsAppendedData(t *testing.T) {
	stats, options := newRetentionStore(t)
	before := fileInfo(t, options.StoragePath)
	if err := os.Mkdir(options.StoragePath+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 100; i < 110; i++ {
		stats.Record(context.Background(), retentionRecord(i))
	}
	if stats.lastError == "" || stats.file == nil || !os.SameFile(before, fileInfo(t, options.StoragePath)) {
		t.Fatal("failed compaction did not preserve the append file")
	}
	events, err := loadEvents(options.StoragePath)
	if err != nil || len(events) != 110 || events[109].Detail.Alias != "req-109" {
		t.Fatalf("new event missing after compaction failure: count=%d err=%v", len(events), err)
	}
	if err := os.Remove(options.StoragePath + ".tmp"); err != nil {
		t.Fatal(err)
	}
	stats.Record(context.Background(), retentionRecord(110))
	if stats.lastError != "" || stats.diskRecords != 100 {
		t.Fatalf("compaction retry failed: %s", stats.lastError)
	}
	_ = stats.file.Close()
	stats.file = nil
	assertReloadedRetention(t, options, "req-11", "req-110")
}

func TestRetentionCacheDoesNotRebuildOrInflatePerEviction(t *testing.T) {
	stats, _ := newRetentionStore(t)
	before, _ := stats.SnapshotWindow("all", 0)
	for i := 100; i < 109; i++ {
		stats.Record(context.Background(), retentionRecord(i))
	}
	after, _ := stats.SnapshotWindow("all", 0)
	if after.TotalRequests != before.TotalRequests || after.TotalCostUSD != before.TotalCostUSD || stats.cacheRefreshPending.Load() {
		t.Fatal("individual evictions scheduled a rebuild or inflated cached totals")
	}
	if stats.cacheNeedsRefresh(time.Now()) || !stats.cacheNeedsRefresh(time.Now().Add(windowCacheRefreshAfter)) {
		t.Fatal("retention cache does not respect the refresh interval")
	}
	stats.rebuildCachedWindows()
	after, _ = stats.SnapshotWindow("all", 0)
	if after.TotalRequests != 100 || stats.windowCache["all"].retentionStale {
		t.Fatal("cache did not recover after a batched rebuild")
	}
}

func TestRetentionConcurrentReadsWritesAndRebuilds(t *testing.T) {
	stats, _ := newRetentionStore(t)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 100; i < 160; i++ {
			stats.Record(context.Background(), retentionRecord(i))
		}
	})
	wg.Go(func() {
		for i := 0; i < 30; i++ {
			stats.rebuildCachedWindows()
		}
	})
	wg.Go(func() {
		for i := 0; i < 60; i++ {
			stats.SnapshotWindow("7d", 20)
			stats.AccountSnapshotsRange(time.Time{}, time.Time{})
		}
	})
	wg.Wait()
	stats.rebuildCachedWindows()
	snapshot, _ := stats.SnapshotWindow("all", 0)
	if snapshot.TotalRequests != 100 || snapshot.TotalCostUSD != 100 {
		t.Fatalf("concurrent updates lost or duplicated usage: requests=%d cost=%f", snapshot.TotalRequests, snapshot.TotalCostUSD)
	}
}

func TestRetentionPruneClearsReferencesWithoutCopyingHistory(t *testing.T) {
	now := time.Now()
	events := make([]storedEvent, 101, 150)
	for i := range events {
		events[i] = storedEvent{API: "test", Detail: RequestDetail{Timestamp: now}}
	}
	wantFirst := &events[1]
	retained := pruneEvents(events, Options{MaxRecords: 100}, now)
	if len(retained) != 100 || &retained[0] != wantFirst || events[0].API != "" {
		t.Fatal("pruning copied history or kept an evicted reference")
	}
	expired := pruneEvents(retained, Options{RetentionDays: 1}, now.Add(48*time.Hour))
	if expired != nil || events[100].API != "" {
		t.Fatal("expired records retain their backing array or references")
	}
}

func TestRetentionReloadPrunesOutOfOrderAppends(t *testing.T) {
	stats, options := newRetentionStore(t)
	stats.Record(context.Background(), retentionRecord(-100000))
	stats.Record(context.Background(), retentionRecord(2000))
	if stats.diskRecords != 102 || len(stats.events) != 100 {
		t.Fatal("unexpected retention before reload")
	}
	_ = stats.file.Close()
	stats.file = nil
	assertReloadedRetention(t, options, "req-1", "req-2000")
}

func TestRetentionClearCannotRestoreObsoleteAppendData(t *testing.T) {
	stats, options := newRetentionStore(t)
	stats.Record(context.Background(), retentionRecord(100))
	if err := stats.Clear(); err != nil {
		t.Fatal(err)
	}
	if stats.diskRecords != 0 || len(stats.events) != 0 {
		t.Fatal("clear retained pending compaction state")
	}
	_ = stats.file.Close()
	stats.file = nil
	reloaded := NewRequestStatistics()
	if err := reloaded.Configure(options); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reloaded.file.Close() }()
	if len(reloaded.events) != 0 {
		t.Fatal("cleared append data reappeared after restart")
	}
}
