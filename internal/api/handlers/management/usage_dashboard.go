package management

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

// GetUsageDashboard applies one filter contract to summaries, trends and diagnostics.
func (h *Handler) GetUsageDashboard(c *gin.Context) {
	now := time.Now().UTC()
	query, err := parseUsageQuery(c, now)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_usage_query", "message": err.Error()})
		return
	}
	stats := internalusage.GetRequestStatistics()
	result := struct {
		internalusage.UsageDashboard
		Storage internalusage.StorageStatus `json:"storage"`
	}{UsageDashboard: stats.Dashboard(query, now), Storage: stats.Status()}
	c.JSON(http.StatusOK, result)
}

// GetUsageRecords returns a bounded page, never an entire retained snapshot.
func (h *Handler) GetUsageRecords(c *gin.Context) {
	query, err := parseUsageQuery(c, time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_usage_query", "message": err.Error()})
		return
	}
	page, err := parseUsagePageInteger(c.Query("page"), 1, 1, 2000000)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_page", "message": err.Error()})
		return
	}
	pageSize, err := parseUsagePageInteger(c.Query("page_size"), 25, 1, 200)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_page_size", "message": err.Error()})
		return
	}
	sortBy := strings.TrimSpace(c.DefaultQuery("sort", "timestamp"))
	switch sortBy {
	case "timestamp", "latency", "ttft", "tokens", "cost":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_sort", "message": "sort must be timestamp, latency, ttft, tokens, or cost"})
		return
	}
	order := strings.TrimSpace(c.DefaultQuery("order", "desc"))
	if order != "asc" && order != "desc" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_order", "message": "order must be asc or desc"})
		return
	}
	c.JSON(http.StatusOK, internalusage.GetRequestStatistics().QueryRecords(query, page, pageSize, sortBy, order))
}

// GetUsageRecord returns only persisted fields; no inferred attempts or traces.
func (h *Handler) GetUsageRecord(c *gin.Context) {
	record, ok := internalusage.GetRequestStatistics().RecordByID(strings.TrimSpace(c.Param("id")))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "usage_record_not_found"})
		return
	}
	c.JSON(http.StatusOK, record)
}

func parseUsageQuery(c *gin.Context, now time.Time) (internalusage.UsageQuery, error) {
	query := internalusage.UsageQuery{
		Provider: strings.TrimSpace(c.Query("provider")), Model: strings.TrimSpace(c.Query("model")),
		Account: strings.TrimSpace(c.Query("account")), APIKey: strings.TrimSpace(c.Query("api_key")),
		Pool: strings.TrimSpace(c.Query("pool")), Status: strings.TrimSpace(c.Query("status")),
		Search: strings.TrimSpace(c.Query("search")),
	}
	for _, value := range []string{query.Provider, query.Model, query.Account, query.APIKey, query.Pool, query.Search} {
		if len(value) > 512 {
			return query, fmt.Errorf("filter values must not exceed 512 bytes")
		}
	}
	if query.Status != "" && query.Status != "success" && query.Status != "failed" {
		return query, fmt.Errorf("status must be success or failed")
	}
	if value := strings.TrimSpace(c.Query("status_code")); value != "" {
		code, err := strconv.Atoi(value)
		if err != nil || code < 100 || code > 599 {
			return query, fmt.Errorf("status_code must be between 100 and 599")
		}
		query.StatusCode = code
	}
	if value := strings.TrimSpace(c.Query("include_warmup")); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return query, fmt.Errorf("include_warmup must be true or false")
		}
		query.IncludeWarmup = enabled
	}
	from, to, err := parseUsageTimeRange(c)
	if err != nil {
		return query, err
	}
	window := strings.TrimSpace(c.Query("range"))
	if window != "" && (!from.IsZero() || !to.IsZero()) {
		return query, fmt.Errorf("range cannot be combined with from or to")
	}
	if window == "" && from.IsZero() && to.IsZero() {
		window = "24h"
	}
	if window != "" {
		normalized, ok := normalizeUsageWindow(window)
		if !ok {
			return query, fmt.Errorf("range must be 24h, 7d, 30d, or all")
		}
		switch normalized {
		case "24h":
			from = now.Add(-24 * time.Hour)
		case "7d":
			from = now.Add(-7 * 24 * time.Hour)
		case "30d":
			from = now.Add(-30 * 24 * time.Hour)
		}
	}
	if to.IsZero() {
		to = now
	}
	if !from.IsZero() && !from.Before(to) {
		return query, fmt.Errorf("from must be earlier than to")
	}
	query.From, query.To = from, to
	return query, nil
}

func parseUsagePageInteger(value string, fallback, minimum, maximum int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("value must be between %d and %d", minimum, maximum)
	}
	return parsed, nil
}
