package management

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestParseUsageQueryWindowsAndFilters(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		query     string
		from      time.Time
		wantError bool
	}{
		{"default", "", now.Add(-24 * time.Hour), false},
		{"week", "range=7d", now.Add(-7 * 24 * time.Hour), false},
		{"all", "range=all", time.Time{}, false},
		{"custom", "from=2026-09-18T12:00:00Z&to=2026-09-19T12:00:00Z", now.Add(-24 * time.Hour), false},
		{"conflict", "range=all&from=2026-09-18T12:00:00Z", time.Time{}, true},
		{"invalid range", "range=3d", time.Time{}, true},
		{"inverted", "from=2026-09-20T12:00:00Z", time.Time{}, true},
		{"bad status", "status=unknown", time.Time{}, true},
		{"bad status code", "status_code=99", time.Time{}, true},
		{"bad warmup", "include_warmup=perhaps", time.Time{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodGet, "/usage/dashboard?"+tc.query, nil)
			query, err := parseUsageQuery(ctx, now)
			if (err != nil) != tc.wantError {
				t.Fatalf("parse error = %v, want error=%v", err, tc.wantError)
			}
			if !tc.wantError && (!query.From.Equal(tc.from) || !query.To.Equal(now) || query.IncludeWarmup) {
				t.Fatalf("query = %+v", query)
			}
		})
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodGet, "/usage/dashboard?provider=codex&model=gpt&account=auth_index%3A1&api_key=key-1&pool=pool-1&status=failed&status_code=429&search=needle&include_warmup=true", nil)
	query, err := parseUsageQuery(ctx, now)
	if err != nil || query.Provider != "codex" || query.Model != "gpt" || query.Account != "auth_index:1" || query.APIKey != "key-1" || query.Pool != "pool-1" || query.Status != "failed" || query.StatusCode != 429 || query.Search != "needle" || !query.IncludeWarmup {
		t.Fatalf("parsed filters = %+v, err=%v", query, err)
	}
}

func TestUsageRecordsRejectInvalidPaginationAndSort(t *testing.T) {
	for _, query := range []string{"page=0", "page=2000001", "page_size=201", "page_size=-1", "sort=arbitrary", "order=up", "from=broken"} {
		t.Run(query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/usage/records?"+query, nil)
			(&Handler{}).GetUsageRecords(ctx)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestUsageDetailMissingRecordReturnsNotFound(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/usage/records/not-a-record", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "not-a-record"}}
	(&Handler{}).GetUsageRecord(ctx)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
