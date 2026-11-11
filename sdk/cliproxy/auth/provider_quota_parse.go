package auth

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

func quotaNumber(v gjson.Result) (float64, bool) {
	if v.Type != gjson.Number && v.Type != gjson.String {
		return 0, false
	}
	n, err := strconv.ParseFloat(v.String(), 64)
	return n, err == nil && validCodexUsed(n)
}
func quotaFirst(v gjson.Result, keys ...string) gjson.Result {
	for _, key := range keys {
		if r := v.Get(key); r.Exists() && r.Type != gjson.Null {
			return r
		}
	}
	return gjson.Result{}
}
func quotaReset(v gjson.Result) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, v.String()); err == nil {
		return t
	}
	if n, ok := quotaNumber(v); ok && n > 0 && n <= 253402300799 {
		return time.Unix(int64(n), 0)
	}
	return time.Time{}
}
func parseProviderQuota(provider string, data []byte, now time.Time) []CodexQuotaWindow {
	if !gjson.ValidBytes(data) {
		return nil
	}
	root := gjson.ParseBytes(data)
	var windows []CodexQuotaWindow
	add := func(name string, used float64, seconds int64, reset time.Time) {
		if validCodexUsed(used) && seconds > 0 && len(name) <= 256 && len(windows) < 128 {
			windows = append(windows, CodexQuotaWindow{Name: name, UsedPercent: used, WindowSeconds: seconds, ResetAt: reset})
		}
	}
	switch strings.ToLower(provider) {
	case "claude":
		for _, key := range []string{"five_hour", "seven_day", "seven_day_oauth_apps", "seven_day_opus", "seven_day_sonnet", "seven_day_cowork", "iguana_necktie"} {
			w := root.Get(key)
			used, ok := quotaNumber(w.Get("utilization"))
			if !ok {
				continue
			}
			seconds := int64(604800)
			if key == "five_hour" {
				seconds = 18000
			}
			name := "claude:" + key
			if key == "iguana_necktie" {
				name = "claude:scoped:fable"
			}
			add(name, used, seconds, quotaReset(w.Get("resets_at")))
		}
		for _, w := range root.Get("limits").Array() {
			if w.Get("kind").String() != "weekly_scoped" || w.Get("is_active").Type == gjson.False {
				continue
			}
			name := w.Get("scope.model.display_name").String()
			used, ok := quotaNumber(w.Get("percent"))
			if ok && name != "" {
				name = strings.ToLower(name)
				if name == "fable 5" {
					name = "fable"
				}
				add("claude:scoped:"+name, used, 604800, quotaReset(w.Get("resets_at")))
			}
		}
	case "xai":
		c := root.Get("config")
		if !c.IsObject() {
			return nil
		}
		period := quotaFirst(c, "currentPeriod", "current_period")
		periodType := strings.ToLower(period.Get("type").String())
		if used, ok := quotaNumber(quotaFirst(c, "creditUsagePercent", "credit_usage_percent")); ok {
			seconds := int64(604800)
			name := "xai:weekly"
			if strings.Contains(periodType, "monthly") {
				seconds = 30 * 86400
				name = "xai:monthly-credit"
			}
			add(name, used, seconds, quotaReset(period.Get("end")))
		}
		for _, product := range quotaFirst(c, "productUsage", "product_usage").Array() {
			name := product.Get("product").String()
			if used, ok := quotaNumber(quotaFirst(product, "usagePercent", "usage_percent")); ok && name != "" {
				seconds := int64(604800)
				if strings.Contains(periodType, "monthly") {
					seconds = 30 * 86400
				}
				add(fmt.Sprintf("xai:product:%d:%s", seconds, name), used, seconds, quotaReset(period.Get("end")))
			}
		}
		limit := quotaFirst(c, "monthlyLimit", "monthly_limit")
		if limit.IsObject() {
			limit = limit.Get("val")
		}
		used := c.Get("used")
		if used.IsObject() {
			used = used.Get("val")
		}
		n, ok := quotaNumber(limit)
		u, valid := quotaNumber(used)
		if ok && valid && n > 0 {
			add("xai:monthly", u/n*100, 30*86400, quotaReset(quotaFirst(c, "billingPeriodEnd", "billing_period_end")))
		}
	case "kimi":
		items := root.Get("limits").Array()
		if w := root.Get("usage"); w.IsObject() {
			items = append(items, gjson.Parse(`{"name":"weekly-summary","window":{"duration":1,"timeUnit":"WEEK"},"detail":`+w.Raw+`}`))
		}
		for i, item := range items {
			detail := item.Get("detail")
			if !detail.IsObject() {
				detail = item
			}
			limit, ok := quotaNumber(detail.Get("limit"))
			if !ok || limit <= 0 {
				continue
			}
			used, valid := quotaNumber(detail.Get("used"))
			if !valid {
				if remaining, ok := quotaNumber(detail.Get("remaining")); ok && remaining <= limit {
					used = limit - remaining
					valid = true
				}
			}
			if !valid {
				continue
			}
			duration := quotaFirst(item, "window.duration", "duration")
			if !duration.Exists() {
				duration = detail.Get("duration")
			}
			unit := quotaFirst(item, "window.timeUnit", "timeUnit")
			if !unit.Exists() {
				unit = detail.Get("timeUnit")
			}
			d, valid := quotaNumber(duration)
			if !valid || d <= 0 || d > 366*86400 {
				continue
			}
			seconds := int64(d)
			switch strings.TrimSuffix(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(unit.String())), "TIME_UNIT_"), "S") {
			case "SECOND":
			case "MINUTE":
				seconds *= 60
			case "HOUR":
				seconds *= 3600
			case "DAY":
				seconds *= 86400
			case "WEEK":
				seconds *= 604800
			default:
				continue
			}
			if seconds <= 0 || seconds > 366*86400 {
				continue
			}
			reset := quotaReset(quotaFirst(detail, "reset_at", "resetAt", "reset_time", "resetTime"))
			if reset.IsZero() {
				if sec, ok := quotaNumber(quotaFirst(detail, "reset_in", "resetIn", "ttl")); ok && sec <= 366*86400 {
					reset = now.Add(time.Duration(sec) * time.Second)
				}
			}
			name := quotaFirst(item, "id", "name", "title").String()
			if name == "" {
				name = fmt.Sprintf("%d:%d", seconds, i)
			}
			add("kimi:"+name, used/limit*100, seconds, reset)
		}
	case "antigravity":
		for groupIndex, group := range root.Get("groups").Array() {
			groupID := quotaFirst(group, "groupId", "group_id", "id", "displayName", "display_name").String()
			if groupID == "" {
				groupID = fmt.Sprint(groupIndex)
			}
			for _, w := range group.Get("buckets").Array() {
				remaining, ok := quotaNumber(quotaFirst(w, "remainingFraction", "remaining_fraction"))
				if !ok || remaining > 1 {
					continue
				}
				kind := strings.ToLower(w.Get("window").String())
				seconds := int64(0)
				switch kind {
				case "5h", "five-hour", "five_hour":
					seconds = 18000
				case "weekly", "week":
					seconds = 604800
				}
				name := quotaFirst(w, "bucketId", "bucket_id").String()
				if name == "" {
					name = quotaFirst(group, "displayName", "display_name").String() + ":" + kind
				}
				add("antigravity:"+groupID+":"+name, (1-remaining)*100, seconds, quotaReset(quotaFirst(w, "resetTime", "reset_time")))
			}
		}
	}
	return windows
}
