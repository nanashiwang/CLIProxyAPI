package config

import (
	"fmt"
	"math"
)

// CodexQuotaGuard uses consumed percentages; nil fields retain protective defaults.
type CodexQuotaGuard struct {
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
	switch seconds {
	case 18000:
		percent, jitter, p, j = c.FiveHourUsedPercent, c.FiveHourJitter, 90, 5
	case 604800:
		percent, jitter, p, j = c.WeeklyUsedPercent, c.WeeklyJitter, 93, 3
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
	for _, seconds := range []int64{18000, 604800} {
		p, j := c.Window(seconds)
		if math.IsNaN(p) || math.IsInf(p, 0) || math.IsNaN(j) || math.IsInf(j, 0) || p <= 0 || p > 100 || j < 0 || p-j <= 0 || p+j > 100 {
			return fmt.Errorf("codex-quota-guard: threshold ± jitter must stay within (0, 100]")
		}
	}
	return nil
}
