// Package usage provides persistent request usage statistics for management APIs.
package usage

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	storageSchemaVersion       = 2
	defaultRetentionDays       = 90
	defaultMaxRecords          = 200000
	maximumMaxRecords          = 2000000
	maximumEventLineSize       = 4 << 20
	windowCacheRefreshInterval = 15 * time.Second
	windowCacheMaxAge          = 45 * time.Second
)

var statisticsEnabled atomic.Bool

func init() {
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

// Options configures persistent usage statistics storage.
type Options struct {
	StoragePath   string
	RetentionDays int
	MaxRecords    int
}

// LoggerPlugin collects usage records into a RequestStatistics store.
type LoggerPlugin struct {
	stats *RequestStatistics
}

// NewLoggerPlugin constructs a usage statistics plugin backed by the default store.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{stats: defaultRequestStatistics} }

// HandleUsage implements coreusage.Plugin.
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || p.stats == nil || !StatisticsEnabled() {
		return
	}
	p.stats.Record(ctx, record)
}

// SetStatisticsEnabled toggles persistent usage recording.
func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

// StatisticsEnabled reports whether persistent usage recording is enabled.
func StatisticsEnabled() bool { return statisticsEnabled.Load() }

// TokenStats contains mutually exclusive token accounting buckets.
type TokenStats struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// RequestDetail stores one persisted request event without secrets or failure bodies.
type RequestDetail struct {
	Timestamp           time.Time         `json:"timestamp"`
	LatencyMs           int64             `json:"latency_ms"`
	TTFTMs              int64             `json:"ttft_ms"`
	Provider            string            `json:"provider"`
	ExecutorType        string            `json:"executor_type"`
	Alias               string            `json:"alias"`
	Endpoint            string            `json:"endpoint"`
	Source              string            `json:"source"`
	AuthID              string            `json:"auth_id"`
	AuthIndex           string            `json:"auth_index"`
	AuthType            string            `json:"auth_type"`
	RequestID           string            `json:"request_id,omitempty"`
	ServiceTier         string            `json:"service_tier"`
	ResponseServiceTier string            `json:"response_service_tier,omitempty"`
	Tokens              TokenStats        `json:"tokens"`
	Failed              bool              `json:"failed"`
	StatusCode          int               `json:"status_code"`
	Generate            bool              `json:"generate"`
	Billing             coreusage.Billing `json:"billing"`
	CostUSD             *float64          `json:"cost_usd,omitempty"`
}

// DimensionSnapshot contains aggregate totals for one account, model, or provider.
type DimensionSnapshot struct {
	TotalRequests    int64      `json:"total_requests"`
	SuccessCount     int64      `json:"success_count"`
	FailureCount     int64      `json:"failure_count"`
	PricedRequests   int64      `json:"priced_requests"`
	UnpricedRequests int64      `json:"unpriced_requests"`
	TotalTokens      int64      `json:"total_tokens"`
	Tokens           TokenStats `json:"tokens"`
	TotalCostUSD     float64    `json:"total_cost_usd"`
}

// AccountUsageSnapshot contains lightweight totals for one stable credential identity.
type AccountUsageSnapshot struct {
	DimensionSnapshot
	Key                  string `json:"key"`
	AuthIndex            string `json:"auth_index,omitempty"`
	AuthID               string `json:"auth_id,omitempty"`
	Source               string `json:"source,omitempty"`
	Provider             string `json:"provider"`
	Estimated            bool   `json:"estimated"`
	CacheWriteUnreported bool   `json:"cache_write_unreported"`
}

// AccountUsageRange identifies one credential and time window to aggregate.
type AccountUsageRange struct {
	Key       string
	AuthIndex string
	From      time.Time
	To        time.Time
}

// AccountUsageRangeSnapshot contains totals for one requested credential time window.
type AccountUsageRangeSnapshot struct {
	DimensionSnapshot
	Key                  string    `json:"key"`
	AuthIndex            string    `json:"auth_index"`
	From                 time.Time `json:"from"`
	To                   time.Time `json:"to"`
	Estimated            bool      `json:"estimated"`
	CacheWriteUnreported bool      `json:"cache_write_unreported"`
}

// StatisticsSnapshot is an immutable view of retained usage events.
type StatisticsSnapshot struct {
	TotalRequests        int64 `json:"total_requests"`
	SuccessCount         int64 `json:"success_count"`
	FailureCount         int64 `json:"failure_count"`
	PricedRequests       int64 `json:"priced_requests"`
	UnpricedRequests     int64 `json:"unpriced_requests"`
	TotalTokens          int64 `json:"total_tokens"`
	Estimated            bool  `json:"estimated"`
	CacheWriteUnreported bool  `json:"cache_write_unreported"`

	Tokens       TokenStats `json:"tokens"`
	TotalCostUSD float64    `json:"total_cost_usd"`

	APIs      map[string]APISnapshot       `json:"apis"`
	Accounts  map[string]DimensionSnapshot `json:"accounts"`
	Providers map[string]DimensionSnapshot `json:"providers"`
	Models    map[string]DimensionSnapshot `json:"models"`

	RequestsByDay        map[string]int64   `json:"requests_by_day"`
	RequestsByHour       map[string]int64   `json:"requests_by_hour"`
	RequestsByHourWindow map[string]int64   `json:"requests_by_hour_window"`
	TokensByDay          map[string]int64   `json:"tokens_by_day"`
	TokensByHour         map[string]int64   `json:"tokens_by_hour"`
	TokensByHourWindow   map[string]int64   `json:"tokens_by_hour_window"`
	CostByDay            map[string]float64 `json:"cost_by_day"`
	CostByHour           map[string]float64 `json:"cost_by_hour"`
	CostByHourWindow     map[string]float64 `json:"cost_by_hour_window"`
}

// APISnapshot contains totals grouped by client API key fingerprint or endpoint.
type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	TotalTokens   int64                    `json:"total_tokens"`
	TotalCostUSD  float64                  `json:"total_cost_usd"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot contains model totals and request-level details.
type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	TotalCostUSD  float64         `json:"total_cost_usd"`
	Details       []RequestDetail `json:"details"`
}

// StorageStatus describes the active persistent statistics store.
type SnapshotCacheStatus struct {
	Window      string    `json:"window"`
	Precomputed bool      `json:"precomputed"`
	ComputedAt  time.Time `json:"computed_at,omitempty"`
	AgeSeconds  int64     `json:"age_seconds"`
}

type StorageStatus struct {
	Enabled       bool       `json:"enabled"`
	StoragePath   string     `json:"storage_path,omitempty"`
	RetentionDays int        `json:"retention_days"`
	MaxRecords    int        `json:"max_records"`
	RecordCount   int        `json:"record_count"`
	FileSizeBytes int64      `json:"file_size_bytes"`
	OldestAt      *time.Time `json:"oldest_at,omitempty"`
	LatestAt      *time.Time `json:"latest_at,omitempty"`
	LoadedAt      *time.Time `json:"loaded_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

type storedEvent struct {
	Version int           `json:"version"`
	API     string        `json:"api"`
	Model   string        `json:"model"`
	Detail  RequestDetail `json:"detail"`
}

// RequestStatistics stores retained request events and their JSONL persistence state.
type cachedWindowSnapshot struct {
	snapshot   StatisticsSnapshot
	from       time.Time
	computedAt time.Time
}

type RequestStatistics struct {
	opMu sync.Mutex
	mu   sync.RWMutex

	events []storedEvent

	cacheMu      sync.RWMutex
	windowCache  map[string]cachedWindowSnapshot
	cacheStarted atomic.Bool

	persistMu sync.Mutex
	options   Options
	file      *os.File
	loadedAt  time.Time
	lastError string
}

var defaultRequestStatistics = NewRequestStatistics()

// GetRequestStatistics returns the process-wide statistics store.
func GetRequestStatistics() *RequestStatistics { return defaultRequestStatistics }

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		options:     normalizeOptions(Options{}),
		windowCache: make(map[string]cachedWindowSnapshot),
	}
}

// Configure applies persistence settings and loads retained events from disk.
func (s *RequestStatistics) Configure(options Options) error {
	if s == nil {
		return errors.New("usage statistics store is nil")
	}
	options = normalizeOptions(options)

	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	if s.options == options && s.file != nil {
		return nil
	}
	if s.file != nil {
		if errClose := s.file.Close(); errClose != nil {
			log.WithError(errClose).Debug("usage: failed to close previous statistics file")
		}
		s.file = nil
	}

	loaded, errLoad := loadEvents(options.StoragePath)
	if errLoad != nil && !os.IsNotExist(errLoad) {
		s.lastError = errLoad.Error()
		return errLoad
	}
	loaded = pruneEvents(loaded, options, time.Now())

	s.mu.Lock()
	s.events = loaded
	s.mu.Unlock()
	s.rebuildWindowCache(loaded, time.Now().UTC())
	s.options = options
	s.loadedAt = time.Now().UTC()
	s.lastError = ""

	if errOpen := s.openFileLocked(); errOpen != nil {
		s.lastError = errOpen.Error()
		return errOpen
	}
	if errRewrite := s.rewriteLocked(loaded); errRewrite != nil {
		s.lastError = errRewrite.Error()
		return errRewrite
	}
	return nil
}

// Record adds one request event and appends it to persistent storage.
func (s *RequestStatistics) Record(ctx context.Context, record coreusage.Record) {
	if s == nil || !StatisticsEnabled() {
		return
	}
	event := eventFromRecord(ctx, record)
	now := time.Now().UTC()

	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	s.events = insertEventSorted(s.events, event)
	before := len(s.events)
	s.events = pruneEvents(s.events, s.options, now)
	compacted := len(s.events) < before
	snapshot := append([]storedEvent(nil), s.events...)
	s.mu.Unlock()

	if compacted {
		s.rebuildWindowCache(snapshot, now)
	} else {
		s.updateCachedWindow(event, now)
	}

	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if s.file == nil {
		if errOpen := s.openFileLocked(); errOpen != nil {
			s.lastError = errOpen.Error()
			return
		}
	}
	var errPersist error
	if compacted {
		errPersist = s.rewriteLocked(snapshot)
	} else {
		errPersist = appendEvent(s.file, event)
	}
	if errPersist != nil {
		s.lastError = errPersist.Error()
		log.WithError(errPersist).Warn("usage: failed to persist statistics event")
		return
	}
	s.lastError = ""
}

// Snapshot returns aggregate statistics for all retained events.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	return s.SnapshotRange(time.Time{}, time.Time{})
}

// SnapshotRange returns aggregate statistics in the half-open [from, to) range.
func (s *RequestStatistics) SnapshotRange(from, to time.Time) StatisticsSnapshot {
	result := newSnapshot()
	if s == nil {
		return result
	}

	s.mu.RLock()
	events := append([]storedEvent(nil), s.events...)
	s.mu.RUnlock()

	for _, event := range events {
		if !from.IsZero() && event.Detail.Timestamp.Before(from) {
			continue
		}
		if !to.IsZero() && !event.Detail.Timestamp.Before(to) {
			continue
		}
		addEventToSnapshot(&result, event)
	}
	return result
}

// StartBackgroundRefresh keeps the fixed usage windows warm for management pages.
func (s *RequestStatistics) StartBackgroundRefresh() {
	if s == nil || s.cacheStarted.Swap(true) {
		return
	}
	go func() {
		ticker := time.NewTicker(windowCacheRefreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.rebuildCachedWindows()
		}
	}()
}

// SnapshotWindow returns a precomputed fixed window plus only the latest request details.
// The aggregate totals are maintained in memory and refreshed in the background, so opening
// the usage page does not trigger a full request-history calculation.
func (s *RequestStatistics) SnapshotWindow(window string, recentLimit int) (StatisticsSnapshot, SnapshotCacheStatus) {
	window = normalizeUsageWindow(window)
	if s == nil {
		return newSnapshot(), SnapshotCacheStatus{Window: window}
	}
	if window == "" {
		return s.SnapshotRange(time.Time{}, time.Time{}), SnapshotCacheStatus{Window: window}
	}

	now := time.Now().UTC()
	s.cacheMu.RLock()
	cached, ok := s.windowCache[window]
	s.cacheMu.RUnlock()
	if !ok || cached.computedAt.IsZero() || now.Sub(cached.computedAt) > windowCacheMaxAge {
		s.rebuildCachedWindows()
		s.cacheMu.RLock()
		cached, ok = s.windowCache[window]
		s.cacheMu.RUnlock()
	}
	if !ok {
		return s.SnapshotRange(time.Time{}, time.Time{}), SnapshotCacheStatus{Window: window}
	}

	from := cached.from
	result := cloneSnapshot(cached.snapshot)
	s.mu.RLock()
	events := append([]storedEvent(nil), s.events...)
	s.mu.RUnlock()
	appendRecentDetails(&result, events, from, now, recentLimit)
	age := now.Sub(cached.computedAt)
	if age < 0 {
		age = 0
	}
	return result, SnapshotCacheStatus{
		Window:      window,
		Precomputed: true,
		ComputedAt:  cached.computedAt,
		AgeSeconds:  int64(age / time.Second),
	}
}

func normalizeUsageWindow(window string) string {
	switch strings.ToLower(strings.TrimSpace(window)) {
	case "24h", "today":
		return "24h"
	case "7d", "7days":
		return "7d"
	case "30d", "30days":
		return "30d"
	case "all":
		return "all"
	default:
		return ""
	}
}

func (s *RequestStatistics) rebuildCachedWindows() {
	if s == nil {
		return
	}
	s.mu.RLock()
	events := append([]storedEvent(nil), s.events...)
	s.mu.RUnlock()
	s.rebuildWindowCache(events, time.Now().UTC())
}

func (s *RequestStatistics) rebuildWindowCache(events []storedEvent, now time.Time) {
	if s == nil {
		return
	}
	windows := map[string]time.Time{
		"all": {},
		"24h": now.Add(-24 * time.Hour),
		"7d":  now.Add(-7 * 24 * time.Hour),
		"30d": now.Add(-30 * 24 * time.Hour),
	}
	results := make(map[string]cachedWindowSnapshot, len(windows))
	for window, from := range windows {
		results[window] = cachedWindowSnapshot{snapshot: newSnapshot(), from: from, computedAt: now}
	}
	for _, event := range events {
		for window, cached := range results {
			if window != "all" && event.Detail.Timestamp.Before(cached.from) {
				continue
			}
			addEventToAggregate(&cached.snapshot, event)
			results[window] = cached
		}
	}
	s.cacheMu.Lock()
	s.windowCache = results
	s.cacheMu.Unlock()
}

func (s *RequestStatistics) updateCachedWindow(event storedEvent, now time.Time) {
	if s == nil {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	for window, cached := range s.windowCache {
		if cached.computedAt.IsZero() || (window != "all" && event.Detail.Timestamp.Before(cached.from)) {
			continue
		}
		addEventToAggregate(&cached.snapshot, event)
		cached.computedAt = now
		s.windowCache[window] = cached
	}
}

func cloneSnapshot(source StatisticsSnapshot) StatisticsSnapshot {
	result := source
	result.APIs = make(map[string]APISnapshot, len(source.APIs))
	for key, api := range source.APIs {
		models := api.Models
		api.Models = make(map[string]ModelSnapshot, len(models))
		for model, value := range models {
			value.Details = append([]RequestDetail(nil), value.Details...)
			api.Models[model] = value
		}
		result.APIs[key] = api
	}
	result.Accounts = cloneDimensions(source.Accounts)
	result.Providers = cloneDimensions(source.Providers)
	result.Models = cloneDimensions(source.Models)
	result.RequestsByDay = cloneInt64Map(source.RequestsByDay)
	result.RequestsByHour = cloneInt64Map(source.RequestsByHour)
	result.RequestsByHourWindow = cloneInt64Map(source.RequestsByHourWindow)
	result.TokensByDay = cloneInt64Map(source.TokensByDay)
	result.TokensByHour = cloneInt64Map(source.TokensByHour)
	result.TokensByHourWindow = cloneInt64Map(source.TokensByHourWindow)
	result.CostByDay = cloneFloatMap(source.CostByDay)
	result.CostByHour = cloneFloatMap(source.CostByHour)
	result.CostByHourWindow = cloneFloatMap(source.CostByHourWindow)
	return result
}

func cloneDimensions(source map[string]DimensionSnapshot) map[string]DimensionSnapshot {
	result := make(map[string]DimensionSnapshot, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneInt64Map(source map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneFloatMap(source map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func appendRecentDetails(snapshot *StatisticsSnapshot, events []storedEvent, from, to time.Time, limit int) {
	if snapshot == nil || limit <= 0 {
		return
	}
	added := 0
	for index := len(events) - 1; index >= 0 && added < limit; index-- {
		event := events[index]
		if event.Detail.Timestamp.Before(from) || !event.Detail.Timestamp.Before(to) {
			continue
		}
		api, ok := snapshot.APIs[event.API]
		if !ok {
			continue
		}
		model, ok := api.Models[event.Model]
		if !ok {
			continue
		}
		model.Details = append(model.Details, event.Detail)
		api.Models[event.Model] = model
		snapshot.APIs[event.API] = api
		added++
	}
}

// AccountSnapshotsRange returns lightweight credential totals in the half-open [from, to) range.
func (s *RequestStatistics) AccountSnapshotsRange(from, to time.Time) []AccountUsageSnapshot {
	if s == nil {
		return []AccountUsageSnapshot{}
	}

	s.mu.RLock()
	events := append([]storedEvent(nil), s.events...)
	s.mu.RUnlock()

	byAccount := make(map[string]AccountUsageSnapshot)
	for _, event := range events {
		detail := event.Detail
		if !from.IsZero() && detail.Timestamp.Before(from) {
			continue
		}
		if !to.IsZero() && !detail.Timestamp.Before(to) {
			continue
		}
		key := stableAccountUsageIdentifier(detail)
		if key == "" {
			continue
		}

		value := byAccount[key]
		value.Key = key
		if authIndex := strings.TrimSpace(detail.AuthIndex); authIndex != "" {
			value.AuthIndex = authIndex
		}
		if authID := strings.TrimSpace(detail.AuthID); authID != "" {
			value.AuthID = authID
		}
		if source := strings.TrimSpace(detail.Source); source != "" {
			value.Source = source
		}
		value.Provider = valueOrUnknown(detail.Provider)
		cost := 0.0
		if detail.CostUSD != nil {
			cost = *detail.CostUSD
		}
		value.DimensionSnapshot = addDimension(value.DimensionSnapshot, detail, cost)
		value.Estimated = value.Estimated || detail.Billing.Pricing.Estimated
		value.CacheWriteUnreported = value.CacheWriteUnreported || detail.Billing.Reason == "cache_write_tokens_unreported"
		byAccount[key] = value
	}

	result := make([]AccountUsageSnapshot, 0, len(byAccount))
	for _, value := range byAccount {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalCostUSD != result[j].TotalCostUSD {
			return result[i].TotalCostUSD > result[j].TotalCostUSD
		}
		if result[i].TotalTokens != result[j].TotalTokens {
			return result[i].TotalTokens > result[j].TotalTokens
		}
		return result[i].Key < result[j].Key
	})
	return result
}

// AccountSnapshotsRanges aggregates multiple credential windows in one event scan.
func (s *RequestStatistics) AccountSnapshotsRanges(ranges []AccountUsageRange) []AccountUsageRangeSnapshot {
	result := make([]AccountUsageRangeSnapshot, len(ranges))
	indexesByAuth := make(map[string][]int)
	for index, usageRange := range ranges {
		authIndex := strings.TrimSpace(usageRange.AuthIndex)
		result[index] = AccountUsageRangeSnapshot{
			Key:       strings.TrimSpace(usageRange.Key),
			AuthIndex: authIndex,
			From:      usageRange.From.UTC(),
			To:        usageRange.To.UTC(),
		}
		if authIndex != "" {
			indexesByAuth[authIndex] = append(indexesByAuth[authIndex], index)
		}
	}
	if s == nil || len(indexesByAuth) == 0 {
		return result
	}

	s.mu.RLock()
	events := append([]storedEvent(nil), s.events...)
	s.mu.RUnlock()

	for _, event := range events {
		detail := event.Detail
		indexes := indexesByAuth[strings.TrimSpace(detail.AuthIndex)]
		for _, index := range indexes {
			value := &result[index]
			if detail.Timestamp.Before(value.From) || !detail.Timestamp.Before(value.To) {
				continue
			}
			cost := 0.0
			if detail.CostUSD != nil {
				cost = *detail.CostUSD
			}
			value.DimensionSnapshot = addDimension(value.DimensionSnapshot, detail, cost)
			value.Estimated = value.Estimated || detail.Billing.Pricing.Estimated
			value.CacheWriteUnreported = value.CacheWriteUnreported || detail.Billing.Reason == "cache_write_tokens_unreported"
		}
	}
	return result
}

// Clear removes all in-memory and persisted usage events.
func (s *RequestStatistics) Clear() error {
	if s == nil {
		return nil
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	s.events = nil
	s.mu.Unlock()
	s.rebuildWindowCache(nil, time.Now().UTC())

	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if errRewrite := s.rewriteLocked(nil); errRewrite != nil {
		s.lastError = errRewrite.Error()
		return errRewrite
	}
	s.lastError = ""
	return nil
}

// MergeSnapshot merges request details from an exported snapshot and persists the result.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) (MergeResult, error) {
	result := MergeResult{}
	if s == nil {
		return result, nil
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	seen := make(map[string]struct{}, len(s.events))
	for _, event := range s.events {
		seen[dedupKey(event)] = struct{}{}
	}
	for apiName, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				event := normalizeStoredEvent(storedEvent{Version: storageSchemaVersion, API: apiName, Model: modelName, Detail: detail})
				key := dedupKey(event)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}
				s.events = append(s.events, event)
				result.Added++
			}
		}
	}
	sort.SliceStable(s.events, func(i, j int) bool {
		return s.events[i].Detail.Timestamp.Before(s.events[j].Detail.Timestamp)
	})
	s.events = pruneEvents(s.events, s.options, time.Now())
	events := append([]storedEvent(nil), s.events...)
	s.mu.Unlock()
	s.rebuildWindowCache(events, time.Now().UTC())

	s.persistMu.Lock()
	if errRewrite := s.rewriteLocked(events); errRewrite != nil {
		s.lastError = errRewrite.Error()
		s.persistMu.Unlock()
		return result, errRewrite
	}
	s.lastError = ""
	s.persistMu.Unlock()
	return result, nil
}

// Status returns persistent storage state without exposing record contents.
func (s *RequestStatistics) Status() StorageStatus {
	status := StorageStatus{Enabled: StatisticsEnabled()}
	if s == nil {
		return status
	}

	s.persistMu.Lock()
	status.StoragePath = s.options.StoragePath
	status.RetentionDays = s.options.RetentionDays
	status.MaxRecords = s.options.MaxRecords
	if !s.loadedAt.IsZero() {
		loadedAt := s.loadedAt
		status.LoadedAt = &loadedAt
	}
	status.LastError = s.lastError
	if info, errStat := os.Stat(s.options.StoragePath); errStat == nil {
		status.FileSizeBytes = info.Size()
	}
	s.persistMu.Unlock()

	s.mu.RLock()
	status.RecordCount = len(s.events)
	if len(s.events) > 0 {
		oldestAt := s.events[0].Detail.Timestamp
		latestAt := s.events[len(s.events)-1].Detail.Timestamp
		status.OldestAt = &oldestAt
		status.LatestAt = &latestAt
	}
	s.mu.RUnlock()
	return status
}

// MergeResult reports import deduplication counts.
type MergeResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

func normalizeOptions(options Options) Options {
	options.StoragePath = strings.TrimSpace(options.StoragePath)
	if options.RetentionDays <= 0 {
		options.RetentionDays = defaultRetentionDays
	}
	if options.MaxRecords <= 0 {
		options.MaxRecords = defaultMaxRecords
	} else if options.MaxRecords > maximumMaxRecords {
		options.MaxRecords = maximumMaxRecords
	}
	if options.StoragePath != "" {
		options.StoragePath = filepath.Clean(options.StoragePath)
	}
	return options
}

func (s *RequestStatistics) openFileLocked() error {
	if s.options.StoragePath == "" {
		return nil
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.options.StoragePath), 0o700); errMkdir != nil {
		return fmt.Errorf("create usage statistics directory: %w", errMkdir)
	}
	file, errOpen := os.OpenFile(s.options.StoragePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return fmt.Errorf("open usage statistics file: %w", errOpen)
	}
	s.file = file
	return nil
}

func (s *RequestStatistics) rewriteLocked(events []storedEvent) error {
	if s.options.StoragePath == "" {
		return nil
	}
	if s.file != nil {
		if errClose := s.file.Close(); errClose != nil {
			log.WithError(errClose).Debug("usage: failed to close statistics file before rewrite")
		}
		s.file = nil
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.options.StoragePath), 0o700); errMkdir != nil {
		return fmt.Errorf("create usage statistics directory: %w", errMkdir)
	}
	tempPath := s.options.StoragePath + ".tmp"
	file, errCreate := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if errCreate != nil {
		return fmt.Errorf("create usage statistics temp file: %w", errCreate)
	}
	writer := bufio.NewWriterSize(file, 64<<10)
	var writeErr error
	for _, event := range events {
		raw, errMarshal := json.Marshal(event)
		if errMarshal != nil {
			writeErr = fmt.Errorf("marshal usage statistics event: %w", errMarshal)
			break
		}
		if _, errWrite := writer.Write(append(raw, '\n')); errWrite != nil {
			writeErr = fmt.Errorf("write usage statistics event: %w", errWrite)
			break
		}
	}
	if writeErr == nil {
		if errFlush := writer.Flush(); errFlush != nil {
			writeErr = fmt.Errorf("flush usage statistics file: %w", errFlush)
		}
	}
	if errClose := file.Close(); writeErr == nil && errClose != nil {
		writeErr = fmt.Errorf("close usage statistics temp file: %w", errClose)
	}
	if writeErr != nil {
		_ = os.Remove(tempPath)
		return writeErr
	}
	if errRename := os.Rename(tempPath, s.options.StoragePath); errRename != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace usage statistics file: %w", errRename)
	}
	return s.openFileLocked()
}

func appendEvent(file *os.File, event storedEvent) error {
	if file == nil {
		return nil
	}
	raw, errMarshal := json.Marshal(event)
	if errMarshal != nil {
		return fmt.Errorf("marshal usage event: %w", errMarshal)
	}
	if _, errWrite := file.Write(append(raw, '\n')); errWrite != nil {
		return fmt.Errorf("append usage event: %w", errWrite)
	}
	return nil
}

func loadEvents(path string) ([]storedEvent, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, errOpen
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Debug("usage: failed to close statistics file after loading")
		}
	}()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maximumEventLineSize)
	events := make([]storedEvent, 0)
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var event storedEvent
		if errUnmarshal := json.Unmarshal(raw, &event); errUnmarshal != nil {
			log.WithError(errUnmarshal).WithField("line", line).Warn("usage: ignored invalid persisted event")
			continue
		}
		events = append(events, normalizeStoredEvent(event))
	}
	if errScan := scanner.Err(); errScan != nil {
		return nil, fmt.Errorf("scan usage statistics file: %w", errScan)
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Detail.Timestamp.Before(events[j].Detail.Timestamp)
	})
	return events, nil
}

func insertEventSorted(events []storedEvent, event storedEvent) []storedEvent {
	if len(events) == 0 || !event.Detail.Timestamp.Before(events[len(events)-1].Detail.Timestamp) {
		return append(events, event)
	}
	index := sort.Search(len(events), func(i int) bool {
		return events[i].Detail.Timestamp.After(event.Detail.Timestamp)
	})
	sortedEvents := make([]storedEvent, 0, len(events)+1)
	sortedEvents = append(sortedEvents, events[:index]...)
	sortedEvents = append(sortedEvents, event)
	sortedEvents = append(sortedEvents, events[index:]...)
	return sortedEvents
}

func pruneEvents(events []storedEvent, options Options, now time.Time) []storedEvent {
	if len(events) == 0 {
		return nil
	}
	start := 0
	if options.RetentionDays > 0 {
		cutoff := now.AddDate(0, 0, -options.RetentionDays)
		for start < len(events) && events[start].Detail.Timestamp.Before(cutoff) {
			start++
		}
	}
	if options.MaxRecords > 0 && len(events)-start > options.MaxRecords {
		start = len(events) - options.MaxRecords
	}
	if start == 0 {
		return events
	}
	return append([]storedEvent(nil), events[start:]...)
}

func eventFromRecord(ctx context.Context, record coreusage.Record) storedEvent {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	tokens := TokenStats{
		InputTokens:      detail.TokenBreakdown.Input.UncachedTokens,
		OutputTokens:     detail.TokenBreakdown.Output.NonReasoningTokens,
		ReasoningTokens:  detail.TokenBreakdown.Output.ReasoningTokens,
		CachedTokens:     detail.TokenBreakdown.Input.CacheReadTokens,
		CacheReadTokens:  detail.TokenBreakdown.Input.CacheReadTokens,
		CacheWriteTokens: detail.TokenBreakdown.Input.CacheWriteTokens,
		TotalTokens:      detail.TokenBreakdown.TotalTokens,
	}
	if !detail.TokenBreakdown.Valid() {
		tokens = TokenStats{
			InputTokens:      detail.InputTokens,
			OutputTokens:     detail.OutputTokens,
			ReasoningTokens:  detail.ReasoningTokens,
			CachedTokens:     detail.CachedTokens,
			CacheReadTokens:  detail.CacheReadTokens,
			CacheWriteTokens: detail.CacheWriteTokens,
			TotalTokens:      detail.TotalTokens,
		}
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CacheReadTokens + tokens.CacheWriteTokens
	}
	failed := record.Failed || !resolveSuccess(ctx)
	statusCode := record.Fail.StatusCode
	if statusCode <= 0 {
		statusCode = internallogging.GetResponseStatus(ctx)
	}
	if statusCode <= 0 {
		if failed {
			statusCode = 500
		} else {
			statusCode = 200
		}
	}
	endpoint := strings.TrimSpace(internallogging.GetEndpoint(ctx))
	provider := valueOrUnknown(record.Provider)
	model := valueOrUnknown(record.Model)
	alias := strings.TrimSpace(record.Alias)
	if alias == "" {
		alias = model
	}
	serviceTier := strings.TrimSpace(record.ServiceTier)
	if serviceTier == "" {
		serviceTier = strings.TrimSpace(record.RequestServiceTier)
	}
	if serviceTier == "" {
		serviceTier = coreusage.ServiceTierFromContext(ctx)
	}
	billing := record.Billing
	var costUSD *float64
	if billing.Priced {
		cost := billing.TotalUSD
		costUSD = &cost
	}
	return normalizeStoredEvent(storedEvent{
		Version: storageSchemaVersion,
		API:     apiIdentifier(record.APIKey, endpoint, provider),
		Model:   model,
		Detail: RequestDetail{
			Timestamp:           timestamp.UTC(),
			LatencyMs:           durationMilliseconds(record.Latency),
			TTFTMs:              durationMilliseconds(record.TTFT),
			Provider:            provider,
			ExecutorType:        valueOrUnknown(record.ExecutorType),
			Alias:               alias,
			Endpoint:            endpoint,
			Source:              strings.TrimSpace(record.Source),
			AuthID:              strings.TrimSpace(record.AuthID),
			AuthIndex:           strings.TrimSpace(record.AuthIndex),
			AuthType:            valueOrUnknown(record.AuthType),
			RequestID:           strings.TrimSpace(internallogging.GetRequestID(ctx)),
			ServiceTier:         serviceTier,
			ResponseServiceTier: strings.TrimSpace(record.ResponseServiceTier),
			Tokens:              tokens,
			Failed:              failed,
			StatusCode:          statusCode,
			Generate:            coreusage.GenerateEnabled(record.Generate),
			Billing:             billing,
			CostUSD:             costUSD,
		},
	})
}

func normalizeStoredEvent(event storedEvent) storedEvent {
	event.Version = storageSchemaVersion
	event.API = strings.TrimSpace(event.API)
	if event.API == "" {
		event.API = "unknown"
	}
	event.Model = valueOrUnknown(event.Model)
	if event.Detail.Timestamp.IsZero() {
		event.Detail.Timestamp = time.Now().UTC()
	} else {
		event.Detail.Timestamp = event.Detail.Timestamp.UTC()
	}
	event.Detail.Provider = valueOrUnknown(event.Detail.Provider)
	event.Detail.ExecutorType = valueOrUnknown(event.Detail.ExecutorType)
	event.Detail.AuthType = valueOrUnknown(event.Detail.AuthType)
	if event.Detail.Alias == "" {
		event.Detail.Alias = event.Model
	}
	if event.Detail.StatusCode <= 0 {
		if event.Detail.Failed {
			event.Detail.StatusCode = 500
		} else {
			event.Detail.StatusCode = 200
		}
	}
	event.Detail.Tokens = normalizeTokens(event.Detail.Tokens)
	if event.Detail.Billing.Priced && event.Detail.CostUSD == nil {
		cost := event.Detail.Billing.TotalUSD
		event.Detail.CostUSD = &cost
	}
	return event
}

func normalizeTokens(tokens TokenStats) TokenStats {
	if tokens.CacheReadTokens == 0 && tokens.CachedTokens != 0 {
		tokens.CacheReadTokens = tokens.CachedTokens
	}
	if tokens.CachedTokens == 0 && tokens.CacheReadTokens != 0 {
		tokens.CachedTokens = tokens.CacheReadTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CacheReadTokens + tokens.CacheWriteTokens
	}
	return tokens
}

func newSnapshot() StatisticsSnapshot {
	return StatisticsSnapshot{
		APIs:                 make(map[string]APISnapshot),
		Accounts:             make(map[string]DimensionSnapshot),
		Providers:            make(map[string]DimensionSnapshot),
		Models:               make(map[string]DimensionSnapshot),
		RequestsByDay:        make(map[string]int64),
		RequestsByHour:       make(map[string]int64),
		RequestsByHourWindow: make(map[string]int64),
		TokensByDay:          make(map[string]int64),
		TokensByHour:         make(map[string]int64),
		TokensByHourWindow:   make(map[string]int64),
		CostByDay:            make(map[string]float64),
		CostByHour:           make(map[string]float64),
		CostByHourWindow:     make(map[string]float64),
	}
}

func addEventToSnapshot(snapshot *StatisticsSnapshot, event storedEvent) {
	addEventToAggregate(snapshot, event)
	detail := event.Detail
	apiSnapshot := snapshot.APIs[event.API]
	modelSnapshot := apiSnapshot.Models[event.Model]
	modelSnapshot.Details = append(modelSnapshot.Details, detail)
	apiSnapshot.Models[event.Model] = modelSnapshot
	snapshot.APIs[event.API] = apiSnapshot
}

func addEventToAggregate(snapshot *StatisticsSnapshot, event storedEvent) {
	detail := event.Detail
	cost := 0.0
	if detail.CostUSD != nil {
		cost = *detail.CostUSD
	}
	snapshot.TotalRequests++
	if detail.Failed {
		snapshot.FailureCount++
	} else {
		snapshot.SuccessCount++
	}
	if detail.Billing.Priced {
		snapshot.PricedRequests++
	} else {
		snapshot.UnpricedRequests++
	}
	snapshot.Estimated = snapshot.Estimated || detail.Billing.Pricing.Estimated
	snapshot.CacheWriteUnreported = snapshot.CacheWriteUnreported || detail.Billing.Reason == "cache_write_tokens_unreported"
	snapshot.TotalTokens += detail.Tokens.TotalTokens
	addTokenStats(&snapshot.Tokens, detail.Tokens)
	snapshot.TotalCostUSD += cost

	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := fmt.Sprintf("%02d", detail.Timestamp.Hour())
	hourWindowKey := detail.Timestamp.Format("2006-01-02T15:00:00Z")
	snapshot.RequestsByDay[dayKey]++
	snapshot.RequestsByHour[hourKey]++
	snapshot.RequestsByHourWindow[hourWindowKey]++
	snapshot.TokensByDay[dayKey] += detail.Tokens.TotalTokens
	snapshot.TokensByHour[hourKey] += detail.Tokens.TotalTokens
	snapshot.TokensByHourWindow[hourWindowKey] += detail.Tokens.TotalTokens
	snapshot.CostByDay[dayKey] += cost
	snapshot.CostByHour[hourKey] += cost
	snapshot.CostByHourWindow[hourWindowKey] += cost

	apiSnapshot := snapshot.APIs[event.API]
	if apiSnapshot.Models == nil {
		apiSnapshot.Models = make(map[string]ModelSnapshot)
	}
	apiSnapshot.TotalRequests++
	apiSnapshot.TotalTokens += detail.Tokens.TotalTokens
	apiSnapshot.TotalCostUSD += cost
	modelSnapshot := apiSnapshot.Models[event.Model]
	modelSnapshot.TotalRequests++
	modelSnapshot.TotalTokens += detail.Tokens.TotalTokens
	modelSnapshot.TotalCostUSD += cost
	apiSnapshot.Models[event.Model] = modelSnapshot
	snapshot.APIs[event.API] = apiSnapshot

	accountKey := accountIdentifier(detail)
	snapshot.Accounts[accountKey] = addDimension(snapshot.Accounts[accountKey], detail, cost)
	snapshot.Providers[detail.Provider] = addDimension(snapshot.Providers[detail.Provider], detail, cost)
	snapshot.Models[event.Model] = addDimension(snapshot.Models[event.Model], detail, cost)
}
func addDimension(value DimensionSnapshot, detail RequestDetail, cost float64) DimensionSnapshot {
	value.TotalRequests++
	if detail.Failed {
		value.FailureCount++
	} else {
		value.SuccessCount++
	}
	if detail.Billing.Priced {
		value.PricedRequests++
	} else {
		value.UnpricedRequests++
	}
	value.TotalTokens += detail.Tokens.TotalTokens
	addTokenStats(&value.Tokens, detail.Tokens)
	value.TotalCostUSD += cost
	return value
}

func addTokenStats(dst *TokenStats, value TokenStats) {
	dst.InputTokens += value.InputTokens
	dst.OutputTokens += value.OutputTokens
	dst.ReasoningTokens += value.ReasoningTokens
	dst.CachedTokens += value.CachedTokens
	dst.CacheReadTokens += value.CacheReadTokens
	dst.CacheWriteTokens += value.CacheWriteTokens
	dst.TotalTokens += value.TotalTokens
}

func stableAccountUsageIdentifier(detail RequestDetail) string {
	if authIndex := strings.TrimSpace(detail.AuthIndex); authIndex != "" {
		return "auth_index:" + authIndex
	}
	provider := valueOrUnknown(detail.Provider)
	if authID := strings.TrimSpace(detail.AuthID); authID != "" {
		return provider + ":auth_id:" + authID
	}
	if source := strings.TrimSpace(detail.Source); source != "" {
		return provider + ":source:" + source
	}
	return ""
}

func accountIdentifier(detail RequestDetail) string {
	if detail.AuthID != "" {
		return detail.AuthID
	}
	if detail.Source != "" {
		return detail.Source
	}
	if detail.AuthIndex != "" {
		return detail.Provider + ":" + detail.AuthIndex
	}
	return detail.Provider + ":unknown"
}

func apiIdentifier(apiKey, endpoint, provider string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		sum := sha256.Sum256([]byte(apiKey))
		return "key:" + hex.EncodeToString(sum[:6])
	}
	if endpoint != "" {
		return endpoint
	}
	return valueOrUnknown(provider)
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func durationMilliseconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}

func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	return status == 0 || status < 400
}

func dedupKey(event storedEvent) string {
	detail := event.Detail
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d|%d",
		event.API,
		event.Model,
		detail.Timestamp.UTC().Format(time.RFC3339Nano),
		detail.RequestID,
		detail.AuthID,
		detail.Source,
		detail.Failed,
		detail.Tokens.InputTokens,
		detail.Tokens.OutputTokens,
		detail.Tokens.ReasoningTokens,
		detail.Tokens.CacheReadTokens,
		detail.Tokens.CacheWriteTokens,
		detail.Tokens.TotalTokens,
	)
}
