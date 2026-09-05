package config

import (
	"path/filepath"
	"testing"
)

func TestParseConfigBytesUsageStatisticsDefaults(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("host: 127.0.0.1\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.UsageStatistics.RetentionDays != 90 || cfg.UsageStatistics.MaxRecords != 200000 {
		t.Fatalf("UsageStatistics = %+v", cfg.UsageStatistics)
	}
}

func TestParseConfigBytesUsageStatisticsOverrides(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
usage-statistics-enabled: true
usage-statistics:
  storage-file: data/custom-usage.jsonl
  retention-days: 30
  max-records: 50000
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.UsageStatisticsEnabled || cfg.UsageStatistics.StorageFile != "data/custom-usage.jsonl" ||
		cfg.UsageStatistics.RetentionDays != 30 || cfg.UsageStatistics.MaxRecords != 50000 {
		t.Fatalf("UsageStatistics = %+v", cfg.UsageStatistics)
	}
}

func TestLoadConfigOptionalMissingUsageStatisticsDefaults(t *testing.T) {
	cfg, errLoad := LoadConfigOptional(filepath.Join(t.TempDir(), "missing.yaml"), true)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if cfg.UsageStatistics.RetentionDays != 90 || cfg.UsageStatistics.MaxRecords != 200000 {
		t.Fatalf("UsageStatistics = %+v", cfg.UsageStatistics)
	}
}
