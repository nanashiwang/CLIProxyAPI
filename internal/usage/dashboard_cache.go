package usage

import (
	"encoding/json"
	"time"
)

type dashboardCacheEntry struct {
	version uint64
	created time.Time
	data    []byte
}

// Dashboard serializes expensive builds, releases the store lock before
// aggregation, and reuses immutable bounded snapshots of the same source version.
func (s *RequestStatistics) Dashboard(query UsageQuery, now time.Time) UsageDashboard {
	if s == nil {
		return buildDashboard(nil, query, now)
	}
	s.dashboardMu.Lock()
	defer s.dashboardMu.Unlock()
	keyQuery := query
	zone := "UTC"
	if query.Location != nil {
		zone = query.Location.String()
	}
	if query.CacheWindow != "" {
		keyQuery.From = time.Time{}
		keyQuery.To = time.Time{}
	}
	encoded, _ := json.Marshal(keyQuery)
	key := string(encoded) + "|" + zone + "|" + query.CacheWindow
	// Time-zero direct queries also depend on now, unlike explicit date filters.
	if query.CacheWindow == "" && query.To.IsZero() {
		key += now.UTC().Format(time.RFC3339Nano)
	}
	s.mu.RLock()
	version := s.version
	s.mu.RUnlock()
	if entry, ok := s.dashboardCache[key]; ok && entry.version == version && !now.Before(entry.created) && now.Sub(entry.created) < time.Second {
		var result UsageDashboard
		if json.Unmarshal(entry.data, &result) == nil {
			result.CacheHit = true
			return result
		}
	}
	if query.To.IsZero() {
		query.To = now.UTC()
	}
	s.mu.RLock()
	version = s.version
	start, end := usageQueryBounds(s.events, query)
	events := append([]storedEvent(nil), s.events[start:end]...)
	s.mu.RUnlock()
	result := buildDashboard(events, query, now)
	result.ComputedAt = now.UTC()
	data, err := json.Marshal(result)
	if err == nil && len(data) <= 2<<20 {
		if s.dashboardCache == nil {
			s.dashboardCache = make(map[string]dashboardCacheEntry)
		}
		if len(s.dashboardCache) >= 8 {
			var oldest string
			var at time.Time
			for k, e := range s.dashboardCache {
				if at.IsZero() || e.created.Before(at) {
					oldest = k
					at = e.created
				}
			}
			delete(s.dashboardCache, oldest)
		}
		s.dashboardCache[key] = dashboardCacheEntry{version: version, created: now, data: data}
	}
	return result
}
