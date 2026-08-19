package usage

import (
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// OptionsFromConfig resolves usage statistics paths relative to the configuration file.
func OptionsFromConfig(cfg config.UsageStatisticsConfig, configPath string) Options {
	baseDir := "."
	if trimmedPath := strings.TrimSpace(configPath); trimmedPath != "" {
		baseDir = filepath.Dir(trimmedPath)
	}
	storagePath := strings.TrimSpace(cfg.StorageFile)
	if storagePath == "" {
		storagePath = filepath.Join(baseDir, "data", "usage_statistics.jsonl")
	} else if !filepath.IsAbs(storagePath) {
		storagePath = filepath.Join(baseDir, storagePath)
	}
	return Options{
		StoragePath:   filepath.Clean(storagePath),
		RetentionDays: cfg.RetentionDays,
		MaxRecords:    cfg.MaxRecords,
	}
}

// ConfigureDefault configures the process-wide usage statistics store.
func ConfigureDefault(cfg config.UsageStatisticsConfig, configPath string) error {
	if err := defaultRequestStatistics.Configure(OptionsFromConfig(cfg, configPath)); err != nil {
		return err
	}
	defaultRequestStatistics.StartBackgroundRefresh()
	return nil
}
