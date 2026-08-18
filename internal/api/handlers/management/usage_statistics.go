package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

const (
	maximumUsageImportBytes   = 256 << 20
	maximumUsageAccountRanges = 500
)

type usageExportPayload struct {
	Version    int                              `json:"version"`
	ExportedAt time.Time                        `json:"exported_at"`
	Storage    internalusage.StorageStatus      `json:"storage"`
	Usage      internalusage.StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                              `json:"version"`
	Usage   internalusage.StatisticsSnapshot `json:"usage"`
}

type usageAccountRangeInput struct {
	Key       string `json:"key"`
	AuthIndex string `json:"auth_index"`
	From      string `json:"from"`
	To        string `json:"to"`
}

type usageAccountRangesPayload struct {
	Ranges []usageAccountRangeInput `json:"ranges"`
}

// GetUsageStatistics returns retained persistent usage statistics.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	from, to, errRange := parseUsageTimeRange(c)
	if errRange != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_time_range", "message": errRange.Error()})
		return
	}
	stats := internalusage.GetRequestStatistics()
	snapshot := stats.SnapshotRange(from, to)
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
		"storage":         stats.Status(),
	})
}

// GetUsageAccountSummaries returns lightweight totals grouped by stable credential identity.
func (h *Handler) GetUsageAccountSummaries(c *gin.Context) {
	from, to, errRange := parseUsageTimeRange(c)
	if errRange != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_time_range", "message": errRange.Error()})
		return
	}
	stats := internalusage.GetRequestStatistics()
	c.JSON(http.StatusOK, gin.H{
		"accounts": stats.AccountSnapshotsRange(from, to),
		"storage":  stats.Status(),
	})
}

// PostUsageAccountRanges returns totals for multiple credential windows in one scan.
func (h *Handler) PostUsageAccountRanges(c *gin.Context) {
	var payload usageAccountRangesPayload
	if errBind := c.ShouldBindJSON(&payload); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json", "message": errBind.Error()})
		return
	}
	if len(payload.Ranges) > maximumUsageAccountRanges {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too_many_ranges", "limit": maximumUsageAccountRanges})
		return
	}

	ranges := make([]internalusage.AccountUsageRange, 0, len(payload.Ranges))
	for index, input := range payload.Ranges {
		key := strings.TrimSpace(input.Key)
		authIndex := strings.TrimSpace(input.AuthIndex)
		if key == "" || authIndex == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_range", "index": index, "message": "key and auth_index are required"})
			return
		}
		from, errFrom := parseUsageTime(input.From)
		if errFrom != nil || from.IsZero() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_range", "index": index, "message": "from must be RFC3339 or Unix timestamp"})
			return
		}
		to, errTo := parseUsageTime(input.To)
		if errTo != nil || to.IsZero() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_range", "index": index, "message": "to must be RFC3339 or Unix timestamp"})
			return
		}
		if !from.Before(to) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_range", "index": index, "message": "from must be earlier than to"})
			return
		}
		ranges = append(ranges, internalusage.AccountUsageRange{Key: key, AuthIndex: authIndex, From: from, To: to})
	}

	stats := internalusage.GetRequestStatistics()
	c.JSON(http.StatusOK, gin.H{
		"ranges":  stats.AccountSnapshotsRanges(ranges),
		"storage": stats.Status(),
	})
}

// GetUsageStatisticsStatus returns persistent storage status.
func (h *Handler) GetUsageStatisticsStatus(c *gin.Context) {
	c.JSON(http.StatusOK, internalusage.GetRequestStatistics().Status())
}

// ExportUsageStatistics returns a complete retained snapshot for backup or migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	stats := internalusage.GetRequestStatistics()
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    2,
		ExportedAt: time.Now().UTC(),
		Storage:    stats.Status(),
		Usage:      stats.Snapshot(),
	})
}

// ImportUsageStatistics merges an exported snapshot into persistent storage.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, maximumUsageImportBytes))
	var payload usageImportPayload
	if errDecode := decoder.Decode(&payload); errDecode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json", "message": errDecode.Error()})
		return
	}
	if payload.Version != 0 && payload.Version != 1 && payload.Version != 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_version"})
		return
	}

	stats := internalusage.GetRequestStatistics()
	result, errMerge := stats.MergeSnapshot(payload.Usage)
	if errMerge != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usage_import_failed", "message": errMerge.Error()})
		return
	}
	snapshot := stats.Snapshot()
	c.JSON(http.StatusOK, gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
		"total_cost_usd":  snapshot.TotalCostUSD,
	})
}

// DeleteUsageStatistics clears all retained usage events.
func (h *Handler) DeleteUsageStatistics(c *gin.Context) {
	if errClear := internalusage.GetRequestStatistics().Clear(); errClear != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usage_clear_failed", "message": errClear.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func parseUsageTimeRange(c *gin.Context) (time.Time, time.Time, error) {
	from, errFrom := parseUsageTime(c.Query("from"))
	if errFrom != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid from: %w", errFrom)
	}
	to, errTo := parseUsageTime(c.Query("to"))
	if errTo != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid to: %w", errTo)
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("from must be earlier than to")
	}
	return from, to, nil
}

func parseUsageTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if parsed, errParse := time.Parse(time.RFC3339Nano, value); errParse == nil {
		return parsed.UTC(), nil
	}
	unixValue, errUnix := strconv.ParseInt(value, 10, 64)
	if errUnix != nil {
		return time.Time{}, errors.New("must be RFC3339 or Unix timestamp")
	}
	if unixValue > 100000000000 {
		return time.UnixMilli(unixValue).UTC(), nil
	}
	return time.Unix(unixValue, 0).UTC(), nil
}
