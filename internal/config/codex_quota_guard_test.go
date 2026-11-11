package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestCodexQuotaGuardConfig(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{"{}", true},
		{"five-hour-used-percent: 90\nfive-hour-jitter: 0", true},
		{"weekly-used-percent: 99", false},
		{"five-hour-jitter: -1", false},
		{"five-hour-used-percent: .nan", false},
		{"weekly-used-percent: 0", false},
	} {
		var c CodexQuotaGuard
		if err := yaml.Unmarshal([]byte(tc.raw), &c); err != nil {
			t.Fatal(err)
		}
		if (c.Validate() == nil) != tc.valid {
			t.Fatal(tc.raw)
		}
	}
	var c CodexQuotaGuard
	p, j := c.Window(18000)
	if !c.IsEnabled() || p != 90 || j != 5 {
		t.Fatal(c)
	}
	p, j = c.Window(604800)
	if p != 93 || j != 3 {
		t.Fatal(p, j)
	}
}

func TestCommonQuotaGuardLegacyOverlay(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("codex-quota-guard:\n  enabled: false\n  weekly-used-percent: 88\nquota-guard:\n  enabled: true\n  monthly-used-percent: 95\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	c := cfg.EffectiveQuotaGuard()
	if !c.IsEnabled() {
		t.Fatal("common enabled ignored")
	}
	p, _ := c.Window(604800)
	if p != 88 {
		t.Fatal("legacy value lost")
	}
	p, j := c.Window(30 * 86400)
	if p != 95 || j != 3 {
		t.Fatal("monthly value lost")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
