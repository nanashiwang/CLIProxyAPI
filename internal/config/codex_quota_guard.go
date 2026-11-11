package config

import (
	"fmt"
	"math"
)

// CodexQuotaGuard retains its Go name for compatibility and uses consumed percentages; nil fields retain protective defaults.
type CodexQuotaGuard struct {
	MonthlyUsedPercent  *float64 `yaml:"monthly-used-percent,omitempty" json:"monthly-used-percent,omitempty"`
	MonthlyJitter       *float64 `yaml:"monthly-jitter,omitempty" json:"monthly-jitter,omitempty"`
	Enabled             *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	FiveHourUsedPercent *float64 `yaml:"five-hour-used-percent,omitempty" json:"five-hour-used-percent,omitempty"`
	FiveHourJitter      *float64 `yaml:"five-hour-jitter,omitempty" json:"five-hour-jitter,omitempty"`
	WeeklyUsedPercent   *float64 `yaml:"weekly-used-percent,omitempty" json:"weekly-used-percent,omitempty"`
	WeeklyJitter        *float64 `yaml:"weekly-jitter,omitempty" json:"weekly-jitter,omitempty"`
}

func (c CodexQuotaGuard) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }
func (c CodexQuotaGuard) Window(seconds int64) (float64, float64) {
	var percent, jitter *float64
	var p, j float64
	switch {
	case seconds > 0 && seconds < 604800:
		percent, jitter, p, j = c.FiveHourUsedPercent, c.FiveHourJitter, 90, 5
	case seconds >= 604800 && seconds < 28*86400:
		percent, jitter, p, j = c.WeeklyUsedPercent, c.WeeklyJitter, 93, 3
	case seconds >= 28*86400:
		percent, jitter, p, j = c.MonthlyUsedPercent, c.MonthlyJitter, 93, 3
	default:
		return 0, 0
	}
	if percent != nil {
		p = *percent
	}
	if jitter != nil {
		j = *jitter
	}
	return p, j
}
func (c CodexQuotaGuard) Validate() error {
	for _, seconds := range []int64{18000, 604800, 30 * 86400} {
		p, j := c.Window(seconds)
		if math.IsNaN(p) || math.IsInf(p, 0) || math.IsNaN(j) || math.IsInf(j, 0) || p <= 0 || p > 100 || j < 0 || p-j <= 0 || p+j > 100 {
			return fmt.Errorf("quota-guard: threshold ± jitter must stay within (0, 100]")
		}
	}
	return nil
}

// EffectiveQuotaGuard overlays the common policy on the legacy Codex settings.
func (cfg *Config) EffectiveQuotaGuard() CodexQuotaGuard {
	if cfg == nil {
		return CodexQuotaGuard{}
	}
	c := cfg.CodexQuotaGuard
	if q := cfg.QuotaGuard; q != nil {
		if q.Enabled != nil {
			c.Enabled = q.Enabled
		}
		if q.FiveHourUsedPercent != nil {
			c.FiveHourUsedPercent = q.FiveHourUsedPercent
		}
		if q.FiveHourJitter != nil {
			c.FiveHourJitter = q.FiveHourJitter
		}
		if q.WeeklyUsedPercent != nil {
			c.WeeklyUsedPercent = q.WeeklyUsedPercent
		}
		if q.WeeklyJitter != nil {
			c.WeeklyJitter = q.WeeklyJitter
		}
		if q.MonthlyUsedPercent != nil {
			c.MonthlyUsedPercent = q.MonthlyUsedPercent
		}
		if q.MonthlyJitter != nil {
			c.MonthlyJitter = q.MonthlyJitter
		}
	}
	return c
}
