package usage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestModelObservationPersistsWithoutChangingBillingOrRecordIdentity(t *testing.T) {
	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	stats := NewRequestStatistics()
	options := Options{StoragePath: path, RetentionDays: 30, MaxRecords: 100}
	if err := stats.Configure(options); err != nil {
		t.Fatal(err)
	}
	record := coreusage.Record{
		Provider: "kimi", Model: "kimi-k3[1m]", Alias: "coding",
		RequestedModel: " coding ", UpstreamModel: " k3 ",
		UpstreamResponseModel: " k3-2026 ", UpstreamResponseModelSource: "header",
		RequestedAt: time.Now().UTC().Add(-time.Minute),
		Detail:      coreusage.Detail{InputTokens: 10, TotalTokens: 10},
		Billing:     coreusage.Billing{Priced: true, TotalUSD: 0.25},
	}
	stats.Record(context.Background(), record)
	page := stats.QueryRecords(UsageQuery{}, 1, 20, "timestamp", "desc")
	if len(page.Items) != 1 {
		t.Fatalf("records = %+v", page)
	}
	got := page.Items[0]
	if got.RequestedModel != "coding" || got.UpstreamModel != "k3" || got.UpstreamResponseModel != "k3-2026" || got.UpstreamResponseModelSource != "header" || got.ModelMatch != "mismatch" {
		t.Fatalf("captured model facts = %+v", got)
	}
	if got.Model != "kimi-k3[1m]" || got.Billing.TotalUSD != 0.25 || stats.Snapshot().Models["kimi-k3[1m]"].TotalRequests != 1 {
		t.Fatal("wire model observation changed the existing pricing or aggregation model")
	}
	withoutObservation := stats.events[0]
	withoutObservation.Detail.RequestedModel = ""
	withoutObservation.Detail.UpstreamModel = ""
	withoutObservation.Detail.UpstreamResponseModel = ""
	withoutObservation.Detail.UpstreamResponseModelSource = ""
	if usageRecordID(withoutObservation) != got.ID {
		t.Fatal("additive observations changed stable record identity")
	}
	reloaded := NewRequestStatistics()
	if err := reloaded.Configure(options); err != nil {
		t.Fatal(err)
	}
	detail, ok := reloaded.RecordByID(got.ID)
	if !ok || !reflect.DeepEqual(detail, got) {
		t.Fatalf("disk round trip lost observation: %+v", detail)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var decoded UsageRecord
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, got) {
		t.Fatalf("API round trip lost observation: %+v, %v", decoded, err)
	}
	for _, needle := range []string{"coding", "k3-2026"} {
		if result := reloaded.QueryRecords(UsageQuery{Search: needle}, 1, 20, "timestamp", "desc"); result.Total != 1 {
			t.Fatalf("search cannot find model observation %q", needle)
		}
	}
	exported := reloaded.Snapshot()
	imported := NewRequestStatistics()
	if _, err := imported.MergeSnapshot(exported); err != nil {
		t.Fatal(err)
	}
	if result := imported.QueryRecords(UsageQuery{}, 1, 20, "timestamp", "desc"); len(result.Items) != 1 || !reflect.DeepEqual(result.Items[0], got) {
		t.Fatalf("export/import lost model observation: %+v", result)
	}
}

func TestModelMatchDoesNotInferFactsFromLegacyAliasesOrPricing(t *testing.T) {
	for _, tc := range []struct {
		name, requested, sent, returned, want string
	}{
		{"legacy", "", "", "", "unknown"},
		{"mapped alias", "coding", "gpt-5", "gpt-5", "matched"},
		{"upstream differs", "coding", "gpt-5", "gpt-5-mini", "mismatch"},
		{"missing response", "gpt-5", "gpt-5", "", "unknown"},
		{"missing wire request", "gpt-5", "", "gpt-5", "unknown"},
		{"snapshot suffix preserved", "gpt-5", "gpt-5", "gpt-5-2026-01-01", "mismatch"},
		{"case preserved", "gpt-5", "GPT-5", "gpt-5", "mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := RequestDetail{Alias: "gpt-5", RequestedModel: tc.requested, UpstreamModel: tc.sent, UpstreamResponseModel: tc.returned}
			normalizeModelObservation(&detail)
			if got := modelMatch(detail); got != tc.want {
				t.Fatalf("match = %s, want %s", got, tc.want)
			}
		})
	}
	var legacy storedEvent
	if err := json.Unmarshal([]byte(`{"model":"gpt-5","detail":{"alias":"gpt-5","billing":{"pricing":{"matched_model":"gpt-5"}}}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy = normalizeStoredEvent(legacy)
	if record := usageRecord(legacy); record.ModelMatch != "unknown" || record.UpstreamModel != "" || record.UpstreamResponseModel != "" || record.RequestedModel != "" {
		t.Fatalf("legacy record invented facts: %+v", record)
	}
}

func TestImportedModelObservationIsBoundedAndSanitized(t *testing.T) {
	for _, value := range []string{"gpt\x00-5", "gpt\n5", strings.Repeat("x", 257)} {
		event := normalizeStoredEvent(storedEvent{Detail: RequestDetail{
			RequestedModel: value, UpstreamModel: value, UpstreamResponseModel: value, UpstreamResponseModelSource: "body",
		}})
		if event.Detail.RequestedModel != "" || event.Detail.UpstreamModel != "" || event.Detail.UpstreamResponseModel != "" || event.Detail.UpstreamResponseModelSource != "" || modelMatch(event.Detail) != "unknown" {
			t.Fatalf("invalid model accepted: %+v", event.Detail)
		}
	}
	event := normalizeStoredEvent(storedEvent{Detail: RequestDetail{UpstreamResponseModel: "gpt-5", UpstreamResponseModelSource: "untrusted arbitrary data"}})
	if event.Detail.UpstreamResponseModelSource != "" {
		t.Fatal("unrecognized source preserved")
	}
}
