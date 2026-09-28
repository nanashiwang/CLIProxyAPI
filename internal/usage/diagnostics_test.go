package usage

import (
	"context"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"path/filepath"
	"testing"
	"time"
)

func TestDiagnosticsRetainedChainReloadPruneAndImmutability(t *testing.T) {
	SetStatisticsEnabled(true)
	stats := NewRequestStatistics()
	opts := Options{StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), MaxRecords: 2, RetentionDays: 7}
	if err := stats.Configure(opts); err != nil {
		t.Fatal(err)
	}
	root := coreusage.WithExecutionDiagnostics(context.Background())
	a := coreusage.StartDiagnosticAttempt(root, "p", "first", "m")
	record := coreusage.Record{RequestedAt: time.Now(), Provider: "p", AuthID: "first", Model: "m", Failed: true, Fail: coreusage.Failure{StatusCode: 429}}
	record.Diagnostics = coreusage.CaptureExecutionDiagnostics(a, record)
	stats.Record(a, record)
	id := usageRecordID(stats.events[0])
	coreusage.FinishDiagnosticAttempt(a, context.Canceled)
	b := coreusage.StartDiagnosticAttempt(root, "p", "second", "m")
	record.AuthID = "second"
	record.RequestedAt = time.Now()
	record.Failed = false
	record.Diagnostics = coreusage.CaptureExecutionDiagnostics(b, record)
	stats.Record(b, record)
	detail, ok := stats.RecordByID(id)
	if !ok || len(detail.Diagnostics.Attempts) != 2 || len(stats.events) != 2 {
		t.Fatal("detail did not join retained chain without extra accounting records")
	}
	detail.Diagnostics.Attempts[0].AuthID = "mutated"
	again, _ := stats.RecordByID(id)
	if again.Diagnostics.Attempts[0].AuthID == "mutated" {
		t.Fatal("detail aliases store")
	}
	reloaded := NewRequestStatistics()
	if err := reloaded.Configure(opts); err != nil {
		t.Fatal(err)
	}
	saved, ok := reloaded.RecordByID(id)
	if !ok || len(saved.Diagnostics.Attempts) != 2 {
		t.Fatal("chain lost on reload")
	}
	snapshot := stats.Snapshot()
	imported := NewRequestStatistics()
	if _, err := imported.MergeSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if item, ok := imported.RecordByID(id); !ok || len(item.Diagnostics.Attempts) != 2 {
		t.Fatal("chain lost on import")
	}
	for i := 0; i < 2; i++ {
		stats.Record(context.Background(), coreusage.Record{RequestedAt: time.Now(), Model: "other"})
	}
	if len(stats.diagnosticTraces) != 0 {
		t.Fatal("trace retained after last usage eviction")
	}
	if err := reloaded.Clear(); err != nil || len(reloaded.diagnosticTraces) != 0 {
		t.Fatal("clear retained trace")
	}
}
