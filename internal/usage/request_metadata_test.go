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

func TestRequestMetadataPersistsExportsSearchesWithoutChangingIdentity(t *testing.T) {
	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })
	options := Options{StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), RetentionDays: 30, MaxRecords: 100}
	stats := NewRequestStatistics()
	if err := stats.Configure(options); err != nil {
		t.Fatal(err)
	}
	record := coreusage.Record{
		Provider: "codex", Model: "billing-model", AuthType: "oauth", ReasoningEffort: "xhigh",
		ClientTransport: "http", UpstreamTransport: "sse", ClientIP: "192.0.2.9", UserAgent: "columns-agent/1",
		RequestedAt: time.Now().UTC().Add(-time.Minute), Detail: coreusage.Detail{InputTokens: 2, OutputTokens: 1, TotalTokens: 3},
		Billing: coreusage.Billing{Priced: true, TotalUSD: 0.125},
	}
	stats.Record(context.Background(), record)
	page := stats.QueryRecords(UsageQuery{}, 1, 20, "timestamp", "desc")
	if len(page.Items) != 1 {
		t.Fatalf("records=%+v", page)
	}
	got := page.Items[0]
	if got.ClientTransport != "http" || got.UpstreamTransport != "sse" || got.ClientIP != "192.0.2.9" || got.UserAgent != "columns-agent/1" {
		t.Fatalf("metadata missing: %+v", got)
	}
	if got.Model != record.Model || got.Billing.TotalUSD != 0.125 || got.Tokens.TotalTokens != 3 || got.ReasoningEffort != "xhigh" {
		t.Fatal("existing metrics changed")
	}
	withoutMetadata := stats.events[0]
	withoutMetadata.Detail.ClientTransport, withoutMetadata.Detail.UpstreamTransport = "", ""
	withoutMetadata.Detail.ClientIP, withoutMetadata.Detail.UserAgent = "", ""
	if usageRecordID(withoutMetadata) != got.ID || dedupKey(withoutMetadata) != dedupKey(stats.events[0]) {
		t.Fatal("new display fields changed event identity")
	}
	reloaded := NewRequestStatistics()
	if err := reloaded.Configure(options); err != nil {
		t.Fatal(err)
	}
	loaded, ok := reloaded.RecordByID(got.ID)
	if !ok || !reflect.DeepEqual(loaded, got) {
		t.Fatalf("JSONL lost metadata: %+v", loaded)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var decoded UsageRecordsPage
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, page) {
		t.Fatalf("API DTO lost metadata: %v %+v", err, decoded)
	}
	for _, needle := range []string{"sse", "192.0.2.9", "columns-agent", "xhigh", "oauth"} {
		if result := reloaded.QueryRecords(UsageQuery{Search: needle}, 1, 20, "timestamp", "desc"); result.Total != 1 {
			t.Fatalf("metadata search %q returned %+v", needle, result)
		}
	}
	exportJSON, err := json.Marshal(reloaded.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var exported StatisticsSnapshot
	if err := json.Unmarshal(exportJSON, &exported); err != nil {
		t.Fatal(err)
	}
	imported := NewRequestStatistics()
	if _, err := imported.MergeSnapshot(exported); err != nil {
		t.Fatal(err)
	}
	if importedPage := imported.QueryRecords(UsageQuery{}, 1, 20, "timestamp", "desc"); len(importedPage.Items) != 1 || !reflect.DeepEqual(importedPage.Items[0], got) {
		t.Fatalf("export/import changed request detail: %+v", importedPage)
	}
}

func TestRequestMetadataLegacyUnknownAndImportValidation(t *testing.T) {
	var legacy storedEvent
	if err := json.Unmarshal([]byte(`{"api":"POST /v1/responses","model":"gpt-5","detail":{"provider":"codex","executor_type":"CodexWebsocketsExecutor","endpoint":"POST /v1/responses"}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy = normalizeStoredEvent(legacy)
	if legacy.Detail.ClientTransport != "" || legacy.Detail.UpstreamTransport != "" || legacy.Detail.ClientIP != "" || legacy.Detail.UserAgent != "" {
		t.Fatal("legacy facts inferred from provider/endpoint")
	}
	encoded, _ := json.Marshal(usageRecord(legacy))
	for _, field := range []string{"client_transport", "upstream_transport", "client_ip", "user_agent"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("missing optional field %s was emitted", field)
		}
	}
	invalid := normalizeStoredEvent(storedEvent{Detail: RequestDetail{
		ClientTransport: "WS", UpstreamTransport: "grpc", ClientIP: "192.0.2.1, 198.51.100.2", UserAgent: "\x00User\nAgent\r" + strings.Repeat("x", 600),
	}})
	if invalid.Detail.ClientTransport != "" || invalid.Detail.UpstreamTransport != "" || invalid.Detail.ClientIP != "" {
		t.Fatalf("invalid metadata survived import: %+v", invalid.Detail)
	}
	if len(invalid.Detail.UserAgent) > 512 || strings.ContainsAny(invalid.Detail.UserAgent, "\x00\r\n") || !strings.HasPrefix(invalid.Detail.UserAgent, "UserAgent") {
		t.Fatalf("unsafe UA survived import: %q", invalid.Detail.UserAgent)
	}
}
