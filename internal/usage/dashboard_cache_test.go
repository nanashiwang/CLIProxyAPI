package usage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestDashboardCacheInvalidatesAndDetachesResults(t *testing.T) {
	old := StatisticsEnabled()
	SetStatisticsEnabled(true)
	defer SetStatisticsEnabled(old)
	s := NewRequestStatistics()
	if err := s.Configure(Options{StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), MaxRecords: 2, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s.file != nil {
			_ = s.file.Close()
		}
	})
	now := time.Now().UTC()
	q := UsageQuery{From: now.Add(-time.Hour), To: now.Add(time.Hour)}
	r := coreusage.Record{Provider: "test", Model: "one", RequestedAt: now, Detail: coreusage.Detail{InputTokens: 1}}
	s.Record(context.Background(), r)
	first := s.Dashboard(q, now)
	second := s.Dashboard(q, now.Add(time.Millisecond))
	if first.CacheHit || !second.CacheHit || second.Summary.TotalRequests != 1 {
		t.Fatal("snapshot not cached")
	}
	second.Filters.Models[0].Label = "changed"
	if s.Dashboard(q, now.Add(2*time.Millisecond)).Filters.Models[0].Label == "changed" {
		t.Fatal("cached result aliases caller")
	}
	r.RequestedAt = now.Add(-time.Minute)
	s.Record(context.Background(), r)
	if got := s.Dashboard(q, now.Add(3*time.Millisecond)); got.CacheHit || got.Summary.TotalRequests != 2 {
		t.Fatal("late write did not invalidate")
	}
	exported := s.Snapshot()
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if got := s.Dashboard(q, now); got.CacheHit || got.Summary.TotalRequests != 0 {
		t.Fatal("clear did not invalidate")
	}
	if _, err := s.MergeSnapshot(exported); err != nil {
		t.Fatal(err)
	}
	if got := s.Dashboard(q, now); got.CacheHit || got.Summary.TotalRequests != 2 {
		t.Fatal("import did not invalidate")
	}
	r.RequestedAt = now.Add(time.Minute)
	s.Record(context.Background(), r)
	if got := s.Dashboard(q, now); got.CacheHit || got.Summary.TotalRequests != 2 {
		t.Fatal("retention did not invalidate")
	}
	if got := s.Dashboard(q, now.Add(2*time.Second)); got.CacheHit {
		t.Fatal("snapshot outlived freshness bound")
	}
	if err := s.Configure(Options{StoragePath: filepath.Join(t.TempDir(), "other.jsonl")}); err != nil {
		t.Fatal(err)
	}
	if got := s.Dashboard(q, now); got.CacheHit || got.Summary.TotalRequests != 0 {
		t.Fatal("reconfigure did not invalidate")
	}
}

func TestDashboardCacheConcurrentWritesAndQueries(t *testing.T) {
	old := StatisticsEnabled()
	SetStatisticsEnabled(true)
	defer SetStatisticsEnabled(old)
	s := NewRequestStatistics()
	s.options.StoragePath = filepath.Join(t.TempDir(), "usage.jsonl")
	t.Cleanup(func() {
		if s.file != nil {
			_ = s.file.Close()
		}
	})
	now := time.Now().UTC()
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				s.Record(context.Background(), coreusage.Record{RequestedAt: now, Model: "test"})
				s.Dashboard(UsageQuery{From: now.Add(-time.Hour), To: now.Add(time.Hour)}, now)
			}
		}()
	}
	wg.Wait()
	if got := s.Dashboard(UsageQuery{From: now.Add(-time.Hour), To: now.Add(time.Hour)}, now); got.Summary.TotalRequests != 80 {
		t.Fatalf("lost events: %d", got.Summary.TotalRequests)
	}
}

func TestLegacyFirstByteIsNotInOutputPerformance(t *testing.T) {
	now := time.Now().UTC()
	e := dashboardTestEvent(now, "test", "1", false, true, 1000, true)
	e.Detail.Timing = nil
	s := NewRequestStatistics()
	s.events = []storedEvent{e}
	got := s.Dashboard(UsageQuery{To: now.Add(time.Second)}, now)
	if got.Performance.TTFTSamples != 0 || got.Performance.OutputTokensPerSecond != nil || got.Performance.LocalOutputTokensPerSecond != nil || got.Summary.AverageTTFTMs != nil {
		t.Fatal("legacy first-byte observation became output timing")
	}
}

func BenchmarkDashboardSnapshot(b *testing.B) {
	now := time.Now().UTC()
	s := NewRequestStatistics()
	for i := 0; i < 50000; i++ {
		s.events = append(s.events, dashboardTestEvent(now.Add(time.Duration(i-50000)*time.Second), "test", "1", false, true, 1000, true))
	}
	q := UsageQuery{From: now.Add(-24 * time.Hour), To: now}
	for _, cached := range []bool{false, true} {
		name := "build"
		if cached {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			s.Dashboard(q, now)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !cached {
					s.dashboardCache = nil
				}
				s.Dashboard(q, now)
			}
		})
	}
}

func TestEmptyDashboardKeepsCalendarGranularity(t *testing.T) {
	now := time.Now().UTC()
	s := NewRequestStatistics()
	got := s.Dashboard(UsageQuery{From: now.Add(-7 * 24 * time.Hour), To: now}, now)
	if got.Granularity != "day" || len(got.Trend) > 8 {
		t.Fatalf("empty range lost calendar granularity: %s/%d", got.Granularity, len(got.Trend))
	}
}
