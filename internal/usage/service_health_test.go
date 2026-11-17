package usage

import (
	"context"
	"encoding/json"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"testing"
	"time"
)

func TestServiceHealthClassificationAndPersistence(t *testing.T) {
	cases := []struct {
		category string
		status   int
		failed   bool
		want     string
	}{
		{"", 200, false, "succeeded"}, {"cancelled", 500, true, "cancelled"}, {"", 499, true, "cancelled"},
		{"stream_incomplete", 408, true, "incomplete"}, {"authentication", 401, true, "failed"},
		{"rate_limited", 429, true, "failed"}, {"timeout", 408, true, "failed"}, {"invalid_request", 400, true, "caller_error"},
		{"", 502, true, "failed"}, {"unknown", 0, true, "unknown"},
	}
	for _, tc := range cases {
		if got := serviceOutcome(tc.failed, tc.category, tc.status); got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
	event := eventFromRecord(context.Background(), coreusage.Record{Failed: true, Fail: coreusage.Failure{Category: "cancelled"}})
	if event.Detail.ServiceOutcome != "cancelled" {
		t.Fatalf("record: %+v", event.Detail)
	}
	data, _ := json.Marshal(event)
	var restored storedEvent
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if normalizeStoredEvent(restored).Detail.ServiceOutcome != "cancelled" {
		t.Fatal("persistence lost outcome")
	}
	legacy := normalizeStoredEvent(storedEvent{Detail: RequestDetail{Failed: true}})
	if legacy.Detail.ServiceOutcome != "unknown" {
		t.Fatal("invented legacy evidence")
	}
}

func TestDashboardServiceHealthKeepsAccounting(t *testing.T) {
	base := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	outcomes := []string{"succeeded", "failed", "incomplete", "cancelled", "caller_error", "unknown"}
	stats := NewRequestStatistics()
	for i, outcome := range outcomes {
		event := dashboardTestEvent(base.Add(time.Duration(i)*time.Minute), "m", "a", i != 0, true, 100, true)
		event.Detail.ServiceOutcome = outcome
		stats.events = append(stats.events, event)
	}
	query := UsageQuery{From: base, To: base.Add(2 * time.Hour)}
	dashboard := stats.Dashboard(query, query.To)
	h := dashboard.Health.Service
	if h.Samples != 3 || h.SuccessRate == nil || *h.SuccessRate != 1.0/3 || *h.ErrorRate != 2.0/3 || h.Cancelled != 1 || h.CallerErrors != 1 || h.Unknown != 1 {
		t.Fatalf("health %+v", h)
	}
	if dashboard.Summary.TotalRequests != 6 || dashboard.Summary.FailureCount != 5 || dashboard.Health.SuccessRate != 1.0/6 {
		t.Fatal("legacy totals changed")
	}
	if dashboard.Trend[0].Service.Samples != 3 || dashboard.Dimensions.Models[0].Service.Samples != 3 {
		t.Fatal("dimensions/trends diverged")
	}
	if dashboard.Trend[1].Service.SuccessRate != nil || dashboard.Trend[1].Service.ErrorRate != nil {
		t.Fatal("no evidence became measured zero")
	}
	empty := (ServiceHealth{Cancelled: 1, CallerErrors: 1, Unknown: 1}).rates()
	if empty.SuccessRate != nil || empty.ErrorRate != nil {
		t.Fatal("excluded outcomes supplied rate")
	}
}
