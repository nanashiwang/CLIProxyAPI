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
